package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	valueschemaadapter "darkstar/src/adapters/valueschema/jsonschema"
	"darkstar/src/core/artifactops"
	"darkstar/src/core/featurebrief"
	"darkstar/src/core/identity"
	"darkstar/src/core/investigation"
	"darkstar/src/core/investigationrunner"
	"darkstar/src/core/repositoryscope"
	"darkstar/src/core/runexecution"
	"darkstar/src/ports/artifactregistry"
	"darkstar/src/ports/extension"
	"darkstar/src/ports/provider"
)

type investigationProviders struct {
	wiring *daemonProviderWiring
	once   sync.Once
	native extension.Ref
	err    error
}

func (p *investigationProviders) nativeRef() (extension.Ref, error) {
	p.once.Do(func() {
		executable, err := os.Executable()
		if err != nil {
			p.err = err
			return
		}
		file, err := os.Open(executable)
		if err != nil {
			p.err = err
			return
		}
		defer func() {
			_ = file.Close()
		}()
		hash := sha256.New()
		if _, err := io.Copy(hash, file); err != nil {
			p.err = err
			return
		}
		p.native = extension.Ref{ID: "darkstar/codex-native", Version: "1.0.0", Digest: hex.EncodeToString(hash.Sum(nil))}
	})
	return p.native, p.err
}

func (p *investigationProviders) resolve(ctx context.Context, _ string) (investigation.ProviderSelection, error) {
	name := p.wiring.DefaultWorkflowProvider()
	pin, exists := p.wiring.ExtensionPins()["provider:"+name]
	if !exists {
		if name != runexecution.ProviderCodex {
			return investigation.ProviderSelection{}, errors.New("investigation provider requires a pinned implementation")
		}
		var err error
		pin, err = p.nativeRef()
		if err != nil {
			return investigation.ProviderSelection{}, err
		}
	}
	selection := investigation.ProviderSelection{Provider: name, Extension: &pin}
	attemptID := identity.Random("attempt_")
	defer p.wiring.ReleaseProvider(attemptID)
	adapter, err := p.provider(ctx, selection, attemptID, false)
	if err != nil {
		return investigation.ProviderSelection{}, err
	}
	manifest, err := adapter.Capabilities(ctx)
	if err != nil {
		return investigation.ProviderSelection{}, err
	}
	selection.CapabilityFingerprint = manifest.Fingerprint
	return selection, nil
}

func (p *investigationProviders) provider(ctx context.Context, selected investigation.ProviderSelection, attemptID string, resume bool) (provider.Provider, error) {
	ref := selected.Extension
	if ref == nil {
		return nil, errors.New("investigation provider implementation pin is missing")
	}
	if ref.ID == "darkstar/codex-native" {
		current, err := p.nativeRef()
		if err != nil {
			return nil, err
		}
		if selected.Provider != runexecution.ProviderCodex || current != *ref {
			return nil, errors.New("exact native investigation provider implementation is unavailable")
		}
		ref = nil
	}
	return p.wiring.Provider(ctx, runexecution.ProviderRequest{Provider: selected.Provider, Ref: ref, AttemptID: attemptID, Resume: resume, Scenario: runexecution.ScenarioWorkflow})
}

func (service *daemonAPIService) configureInvestigations(ctx context.Context, ownerID string, scopes *repositoryscope.Service, evidence investigationrunner.EvidenceReader, artifacts *artifactops.Service) error {
	providers := &investigationProviders{wiring: service.providerWiring}
	briefs := featurebrief.Validator{Schema: valueschemaadapter.Validator{}, Load: func(ctx context.Context, reference featurebrief.Reference) (json.RawMessage, error) {
		content, err := artifacts.OriginalContent(ctx, artifactregistry.VersionRef{ArtifactID: reference.ArtifactID, Version: reference.Version})
		if err != nil {
			return nil, err
		}
		defer func() {
			_ = content.Reader.Close()
		}()
		if content.Digest != reference.SHA256 || content.Size > investigationrunner.MaxContentBytes {
			return nil, errors.New("feature brief source digest or size does not match allowed immutable content")
		}
		return io.ReadAll(io.LimitReader(content.Reader, investigationrunner.MaxContentBytes+1))
	}}
	worker, err := investigationrunner.New(investigationrunner.Dependencies{
		Provider: providers.provider, ResolveProvider: providers.resolve, ReleaseProvider: providers.wiring.ReleaseProvider,
		Evidence: evidence, Artifacts: artifacts, ValidateBrief: briefs.Validate, Schema: valueschemaadapter.Validator{},
	})
	if err != nil {
		return err
	}
	limit, err := configuredQueueLimit(service.paths, service.projectRoot)
	if err != nil {
		return err
	}
	scheduler, err := investigation.New(service.database, scopes, worker, investigation.Options{
		OwnerID: ownerID, GlobalConcurrency: limit, Admission: &service.investigationAdmission, OtherActive: service.executions.OccupiedRunSlots,
		GlobalLimit: func() (int, error) {
			return configuredQueueLimit(service.paths, service.projectRoot)
		},
	})
	if err != nil {
		return err
	}
	if err := service.server.SetInvestigations(scheduler); err != nil {
		_ = scheduler.Close()
		return err
	}
	service.investigations = scheduler
	workerContext, cancel := context.WithCancel(ctx)
	service.investigationCancel = cancel
	service.investigationDone = make(chan error, 1)
	go func() {
		service.investigationDone <- scheduler.Run(workerContext)
	}()
	return nil
}

func (service *daemonAPIService) stopInvestigations() error {
	if service.investigations == nil {
		return nil
	}
	service.investigationCancel()
	closeErr := service.investigations.Close()
	runErr := <-service.investigationDone
	service.investigations = nil
	service.investigationCancel = nil
	service.investigationDone = nil
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, investigation.ErrClosed) {
		runErr = nil
	}
	if err := errors.Join(closeErr, runErr); err != nil {
		return fmt.Errorf("stop investigation scheduler: %w", err)
	}
	return nil
}
