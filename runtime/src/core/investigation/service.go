package investigation

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"darkstar/src/core/identity"
	"darkstar/src/core/repositoryscope"
	"darkstar/src/ports/statestore"
)

type Service struct {
	store          statestore.InvestigationStore
	scopes         *repositoryscope.Service
	worker         Worker
	options        Options
	mu             sync.Mutex
	active         map[string]context.CancelFunc
	closed         bool
	running        bool
	schedulerError string
	stop           chan struct{}
	wake           chan struct{}
	workers        sync.WaitGroup
}

func New(store statestore.Store, scopes *repositoryscope.Service, worker Worker, options Options) (*Service, error) {
	persistence, ok := store.(statestore.InvestigationStore)
	if !ok || scopes == nil || worker == nil || strings.TrimSpace(options.OwnerID) == "" {
		return nil, errors.New("investigations require durable state, scopes, a worker, and daemon ownership")
	}
	if options.GlobalConcurrency <= 0 {
		options.GlobalConcurrency = 2
	}
	if options.PollInterval <= 0 {
		options.PollInterval = 250 * time.Millisecond
	}
	if options.LeaseDuration < 3*time.Second {
		options.LeaseDuration = 30 * time.Second
	}
	return &Service{store: persistence, scopes: scopes, worker: worker, options: options, active: map[string]context.CancelFunc{}, stop: make(chan struct{}), wake: make(chan struct{}, 1)}, nil
}

func (s *Service) Prepare(ctx context.Context, request PrepareRequest, key string) (View, error) {
	if !validKey(key) || request.ScopeID == "" || (request.Task.Kind != "text" && request.Task.Kind != "feature_brief") {
		return View{}, ErrInvalidRequest
	}
	if request.Task.Kind == "text" && (strings.TrimSpace(request.Task.Text) == "" || len(request.Task.Text) > 128*1024 || request.Task.FeatureBrief != nil) {
		return View{}, ErrInvalidRequest
	}
	if request.Task.Kind == "feature_brief" && (request.Task.Text != "" || request.Task.FeatureBrief == nil || request.Task.FeatureBrief.ArtifactID == "" || request.Task.FeatureBrief.Version == 0 || len(request.Task.FeatureBrief.SHA256) != 64) {
		return View{}, ErrInvalidRequest
	}
	if request.Concurrency == 0 {
		request.Concurrency = 2
	}
	if request.Concurrency < 1 || request.Concurrency > 8 {
		return View{}, ErrInvalidRequest
	}
	id := identity.Deterministic("investigation_", "investigation.prepare/"+key)
	digest := statestore.RepositoryScopeContentDigest(request)
	previous, err := s.store.Investigation(ctx, id)
	if err == nil {
		if previous.RequestDigest != digest {
			return View{}, ErrConflict
		}
		return s.Get(ctx, id)
	}
	if !errors.Is(err, statestore.ErrNotFound) {
		return View{}, err
	}
	scope, err := s.scopes.Get(ctx, request.ScopeID)
	if err != nil {
		return View{}, err
	}
	task, err := s.worker.ResolveTask(ctx, request.Task, scope.Scope.ProjectID)
	if err != nil {
		return View{}, errors.Join(ErrUnavailable, err)
	}
	selection := ProviderSelection{Provider: "daemon", CapabilityFingerprint: statestore.RepositoryScopeContentDigest("investigation-synthesis/v1")}
	if scope.Scope.Mode != statestore.InvestigationScopeNone {
		selection, err = s.worker.ResolveProvider(ctx, scope.Scope.ProjectID)
		if err != nil {
			return View{}, errors.Join(ErrUnavailable, err)
		}
	}
	ids := make([]string, 0, len(scope.Scope.Repositories))
	for _, entry := range scope.Scope.Repositories {
		ids = append(ids, entry.Repository.RepositoryID)
	}
	sort.Strings(ids)
	now := time.Now().UTC()
	collection := statestore.InvestigationCollection{SchemaVersion: 1, CollectionID: id, ProjectID: scope.Scope.ProjectID, ScopeID: scope.Scope.ScopeID, ScopeDigest: scope.Scope.Digest, Task: task, Provider: selection, RepositoryIDs: ids, Concurrency: request.Concurrency, RequestDigest: digest, CreatedAt: now, Status: "prepared", Revision: 1, UpdatedAt: now}
	if _, err = s.store.CreateInvestigation(ctx, collection); err != nil {
		return View{}, mapConflict(err)
	}
	return s.Get(ctx, id)
}

func (s *Service) Get(ctx context.Context, id string) (View, error) {
	collection, err := s.store.Investigation(ctx, id)
	if err != nil {
		return View{}, err
	}
	units, err := s.store.InvestigationUnits(ctx, id)
	if err != nil {
		return View{}, err
	}
	attempts, err := s.store.InvestigationAttempts(ctx, id)
	s.mu.Lock()
	schedulerError := s.schedulerError
	s.mu.Unlock()
	return View{SchemaVersion: 1, Collection: collection, Units: units, Attempts: attempts, SchedulerError: schedulerError}, err
}

func (s *Service) Start(ctx context.Context, id string, expected uint64, key string) (View, error) {
	return s.transition(ctx, id, expected, "start", key)
}

func (s *Service) Retry(ctx context.Context, id string, expected uint64, key string) (View, error) {
	return s.transition(ctx, id, expected, "retry", key)
}

func (s *Service) Cancel(ctx context.Context, id string, expected uint64, key string) (View, error) {
	return s.transition(ctx, id, expected, "cancel", key)
}

func validKey(key string) bool {
	return strings.TrimSpace(key) != "" && len(key) <= 128
}

func mapConflict(err error) error {
	if errors.Is(err, statestore.ErrRepositoryScopeConflict) {
		return errors.Join(ErrConflict, err)
	}
	return err
}

func (s *Service) transition(ctx context.Context, id string, expected uint64, command, key string) (View, error) {
	if !validKey(key) || expected == 0 {
		return View{}, ErrInvalidRequest
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return View{}, ErrClosed
	}
	if _, err := s.store.TransitionInvestigation(ctx, id, expected, command, key); err != nil {
		return View{}, mapConflict(err)
	}
	s.signal()
	return s.Get(ctx, id)
}

func (s *Service) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run claims bounded work and observes abandoned claims. It never redispatches
// an attempt whose provider-start outcome is uncertain.
func (s *Service) Run(ctx context.Context) error {
	s.mu.Lock()
	if s.closed || s.running {
		s.mu.Unlock()
		return ErrClosed
	}
	s.running = true
	s.mu.Unlock()
	ticker := time.NewTicker(s.options.PollInterval)
	defer ticker.Stop()
	for {
		err := s.tick(ctx)
		s.mu.Lock()
		s.schedulerError = ""
		if err != nil && ctx.Err() == nil {
			s.schedulerError = err.Error()
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return s.Close()
		case <-s.stop:
			return nil
		case <-ticker.C:
		case <-s.wake:
		}
	}
}

func (s *Service) tick(ctx context.Context) error {
	collections, err := s.store.Investigations(ctx)
	if err != nil {
		return err
	}
	for _, collection := range collections {
		if collection.Status != "cancelling" {
			continue
		}
		attempts, readErr := s.store.InvestigationAttempts(ctx, collection.CollectionID)
		if readErr != nil {
			return readErr
		}
		s.mu.Lock()
		for _, attempt := range attempts {
			if cancel := s.active[attempt.AttemptID]; cancel != nil {
				cancel()
			}
		}
		s.mu.Unlock()
	}
	for {
		limit, limitErr := s.globalLimit()
		if limitErr != nil {
			return limitErr
		}
		s.mu.Lock()
		full := len(s.active) >= limit || s.closed
		s.mu.Unlock()
		if full {
			return nil
		}
		claim, found, claimErr := s.claim(ctx)
		if claimErr != nil || !found {
			return claimErr
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			state, reason := "cancelled", "daemon closed before worker dispatch"
			if claim.Reconcile {
				state, reason = "uncertain", "daemon closed before provider reconciliation"
			}
			_, err := s.store.CompleteInvestigationAttempt(context.Background(), claim.Attempt.AttemptID, s.options.OwnerID, state, reason)
			return err
		}
		if _, exists := s.active[claim.Attempt.AttemptID]; exists {
			s.mu.Unlock()
			return nil
		}
		workerContext, cancel := context.WithCancel(context.Background())
		s.active[claim.Attempt.AttemptID] = cancel
		s.workers.Add(1)
		s.mu.Unlock()
		go s.execute(workerContext, claim)
	}
}

func (s *Service) claim(ctx context.Context) (statestore.InvestigationClaim, bool, error) {
	if s.options.Admission != nil {
		s.options.Admission.Lock()
		defer s.options.Admission.Unlock()
	}
	available, err := s.globalLimit()
	if err != nil {
		return statestore.InvestigationClaim{}, false, err
	}
	if s.options.OtherActive != nil {
		occupied, err := s.options.OtherActive(ctx)
		if err != nil {
			return statestore.InvestigationClaim{}, false, err
		}
		available -= occupied
	}
	if available < 0 {
		available = 0
	}
	return s.store.ClaimInvestigation(ctx, s.options.OwnerID, available, s.options.LeaseDuration)
}

func (s *Service) globalLimit() (int, error) {
	if s.options.GlobalLimit != nil {
		limit, err := s.options.GlobalLimit()
		if err != nil {
			return 0, err
		}
		if limit < 1 {
			return 0, errors.New("investigation global concurrency must be positive")
		}
		return limit, nil
	}
	return s.options.GlobalConcurrency, nil
}

func (s *Service) Close() error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.stop)
	}
	for _, cancel := range s.active {
		cancel()
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-time.After(20 * time.Second):
		return fmt.Errorf("%w: investigation cancellation remains unconfirmed", ErrUnavailable)
	}
}
