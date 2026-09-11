package cli

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"

	"darkstar/src/adapters/delivery/githubcli"
	"darkstar/src/adapters/provider/workflowtools"
	gitadapter "darkstar/src/adapters/repository/git"
	"darkstar/src/core/nodes"
	"darkstar/src/core/runexecution"
	"darkstar/src/core/workflow"
	"darkstar/src/ports/delivery"
	"darkstar/src/ports/repository"
)

type deliveryText struct {
	CommitSubject     string `json:"commitSubject"`
	CommitBody        string `json:"commitBody"`
	PRTitle           string `json:"prTitle"`
	PRBody            string `json:"prBody"`
	ChangesetSnapshot string `json:"changesetSnapshot"`
}
type workflowCommit struct {
	SHA            string `json:"sha"`
	Branch         string `json:"branch"`
	WorkspaceID    string `json:"workspaceId"`
	SnapshotDigest string `json:"snapshotDigest"`
}
type publishedWorkflowBranch struct {
	workflowCommit
	Remote string `json:"remote"`
}
type workflowPR struct {
	URL     string `json:"url"`
	Number  int    `json:"number"`
	HeadSHA string `json:"headSha"`
	Base    string `json:"base"`
}

// Each visit pins its immutable mutation intent before invoking an adapter.
// Results are stored separately so a lost response can be reconciled on retry.
func deliveryJournal[T any](ctx context.Context, db *sql.DB, id, digest string, prepare func() (T, error)) (T, error) {
	var value T
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS workflow_delivery_intents(id TEXT PRIMARY KEY,digest TEXT NOT NULL,intent TEXT NOT NULL)`)
	if err != nil {
		return value, err
	}
	var savedDigest, raw string
	err = db.QueryRowContext(ctx, `SELECT digest,intent FROM workflow_delivery_intents WHERE id=?`, id).Scan(&savedDigest, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		value, err = prepare()
		if err != nil {
			return value, err
		}
		encoded, encodeErr := json.Marshal(value)
		if encodeErr != nil {
			return value, encodeErr
		}
		_, err = db.ExecContext(ctx, `INSERT OR IGNORE INTO workflow_delivery_intents(id,digest,intent) VALUES(?,?,?)`, id, digest, string(encoded))
		if err != nil {
			return value, err
		}
		err = db.QueryRowContext(ctx, `SELECT digest,intent FROM workflow_delivery_intents WHERE id=?`, id).Scan(&savedDigest, &raw)
	}
	if err != nil {
		return value, err
	}
	if digest != savedDigest {
		return value, errors.New("delivery inputs changed after the operation was prepared")
	}
	err = json.Unmarshal([]byte(raw), &value)
	return value, err
}

type workflowDeliveryServices struct {
	wiring  *daemonProviderWiring
	request runexecution.AttemptRequestContext
}

func (w *daemonProviderWiring) ExecuteDeliveryNode(ctx context.Context, r runexecution.AttemptRequestContext) (json.RawMessage, error) {
	if err := w.authorizeWorkspaceProject(r); err != nil {
		return nil, err
	}
	services := nodes.BuiltinServices{Delivery: workflowDeliveryServices{w, r}}
	engine, err := w.pinnedNodeEngine(r)
	if err != nil {
		return nil, err
	}
	if engine != nil {
		return engine.Execute(ctx, r.Node, r.NodeInputs, services)
	}
	handler, err := nodes.Lookup(r.Node)
	if err != nil {
		return nil, err
	}
	return handler.(nodes.DeterministicHandler).Execute(ctx, r.NodeInputs, services)
}

func (s workflowDeliveryServices) Execute(ctx context.Context, node workflow.Node, inputs nodes.Inputs) (json.RawMessage, error) {
	if s.request.Attempt.VisitID == "" {
		return nil, errors.New("delivery requires a durable node visit")
	}
	db, err := s.wiring.workspaceDB(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = db.Close()
	}()
	identity, _ := json.Marshal([]string{s.request.Run.RunID, s.request.Attempt.VisitID, string(node.Type())})
	id := fmt.Sprintf("workflow-%x", sha256.Sum256(identity))
	intent, _ := json.Marshal(struct {
		Node   workflow.Node
		Inputs nodes.Inputs
	}{node, inputs})
	digest := fmt.Sprintf("%x", sha256.Sum256(intent))
	switch n := node.(type) {
	case workflow.GitCommitNode:
		return s.commit(ctx, db, id, digest, n, inputs)
	case workflow.GitPushNode:
		return s.push(ctx, db, id, digest, n, inputs)
	case workflow.CreatePRNode:
		return s.createPR(ctx, db, id, digest, n, inputs)
	default:
		return nil, errors.New("unsupported delivery node")
	}
}

func (s workflowDeliveryServices) commit(ctx context.Context, db *sql.DB, id, digest string, n workflow.GitCommitNode, inputs nodes.Inputs) (json.RawMessage, error) {
	var changes struct {
		Disposition    string   `json:"disposition"`
		Files          []string `json:"files"`
		SnapshotDigest string   `json:"snapshotDigest"`
	}
	var text deliveryText
	if err := json.Unmarshal(inputs[n.Executor.ChangesetInput], &changes); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(inputs[n.Executor.TextInput], &text); err != nil {
		return nil, err
	}
	if changes.Disposition != "changed" || len(changes.SnapshotDigest) != 64 || text.ChangesetSnapshot != changes.SnapshotDigest {
		return nil, errors.New("commit requires a changed, sealed changeset and matching delivery text")
	}
	manager, err := gitadapter.New("")
	if err != nil {
		return nil, err
	}
	candidate, err := deliveryJournal(ctx, db, id, digest, func() (repository.Candidate, error) {
		p, err := s.wiring.resolvePreparedWorkspace(ctx, s.request, inputs[n.Executor.WorkspaceInput])
		if err != nil {
			return repository.Candidate{}, err
		}
		head := p.HeadSHA
		if head == "" {
			head = p.BaseSHA
		}
		return manager.CaptureCandidate(ctx, repository.CaptureCandidateRequest{RepositoryPath: p.Repository, WorktreePath: p.Path, BranchName: p.Branch, ExpectedParentSHA: head})
	})
	if err != nil {
		return nil, err
	}
	p, err := s.wiring.resolveDeliveryWorkspace(ctx, s.request, inputs[n.Executor.WorkspaceInput], true)
	if err != nil {
		return nil, err
	}
	snapshot, err := workflowtools.WorkspaceDigest(ctx, p.Path)
	if err != nil {
		return nil, err
	}
	if snapshot != changes.SnapshotDigest {
		return nil, errors.New("workspace content changed since the connected changeset was sealed")
	}
	sort.Strings(changes.Files)
	if !slices.Equal(changes.Files, candidate.Manifest) {
		return nil, errors.New("commit candidate contains files outside the connected changeset")
	}
	committed, err := manager.CommitWorkflowCandidate(ctx, repository.WorkflowCommitRequest{Candidate: candidate, OperationID: id, Owner: repository.Ownership{DeliveryLineID: s.request.Run.RunID, WorkItemID: s.request.WorkItem.WorkItemID}, Subject: text.CommitSubject, Body: text.CommitBody})
	if err != nil {
		return nil, err
	}
	// Reconciliation may find an older owned commit; never roll the accepted head back.
	if p.HeadSHA != "" && p.HeadSHA != candidate.ParentSHA && p.HeadSHA != committed.CommitSHA {
		return nil, errors.New("workspace advanced beyond this commit operation")
	}
	head, err := manager.ResolveBase(ctx, repository.ResolveBaseRequest{RepositoryPath: p.Path, BaseRef: "HEAD"})
	if err != nil {
		return nil, err
	}
	if head.CommitSHA != committed.CommitSHA {
		return nil, errors.New("workspace advanced beyond the reconciled commit")
	}
	p.HeadSHA = committed.CommitSHA
	raw, _ := json.Marshal(p)
	_, err = db.ExecContext(ctx, `UPDATE workflow_workspaces SET record=? WHERE id=? AND run_id=?`, string(raw), p.ID, s.request.Run.RunID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Commit workflowCommit `json:"commit"`
	}{workflowCommit{committed.CommitSHA, p.Branch, p.ID, snapshot}})
}

type publicationIntent struct {
	Destination            delivery.BranchRef
	PreviousSHA            string
	PreviousOwnerOperation string
}

func (s workflowDeliveryServices) push(ctx context.Context, db *sql.DB, id, digest string, n workflow.GitPushNode, inputs nodes.Inputs) (json.RawMessage, error) {
	p, err := s.wiring.resolvePreparedWorkspace(ctx, s.request, inputs[n.Executor.WorkspaceInput])
	if err != nil {
		return nil, err
	}
	var commit workflowCommit
	if err = json.Unmarshal(inputs[n.Executor.CommitInput], &commit); err != nil {
		return nil, err
	}
	if commit.WorkspaceID != p.ID || commit.Branch != p.Branch || commit.SHA != p.HeadSHA || len(commit.SnapshotDigest) != 64 {
		return nil, errors.New("push input must match the workspace commit")
	}
	adapter, err := githubcli.New(githubcli.Options{})
	if err != nil {
		return nil, err
	}
	_, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS workflow_publications(workspace_id TEXT NOT NULL,remote TEXT NOT NULL,record TEXT NOT NULL,PRIMARY KEY(workspace_id,remote))`)
	if err != nil {
		return nil, err
	}
	planned, err := deliveryJournal(ctx, db, id, digest, func() (publicationIntent, error) {
		health, err := adapter.ProbeHealth(ctx, delivery.HealthRequest{LocalRepository: p.Path, RemoteName: n.Executor.Remote})
		if err != nil {
			return publicationIntent{}, err
		}
		if _, ok := health.Outcome.(delivery.HealthReady); !ok {
			return publicationIntent{}, errors.New("GitHub remote is not ready for publication")
		}
		plan := publicationIntent{Destination: delivery.BranchRef{Repository: health.Repository, Name: p.Branch}}
		var prior string
		err = db.QueryRowContext(ctx, `SELECT record FROM workflow_publications WHERE workspace_id=? AND remote=?`, p.ID, n.Executor.Remote).Scan(&prior)
		if err == nil {
			var previous publicationIntent
			if err = json.Unmarshal([]byte(prior), &previous); err != nil {
				return plan, err
			}
			if previous.Destination != plan.Destination {
				return plan, errors.New("remote repository changed since previous publication")
			}
			plan = previous
		} else if !errors.Is(err, sql.ErrNoRows) {
			return plan, err
		}
		return plan, nil
	})
	if err != nil {
		return nil, err
	}
	owner := delivery.BranchOwner{DeliveryLineID: s.request.Run.RunID, WorkItemID: s.request.WorkItem.WorkItemID}
	var expected delivery.RemoteBranchExpectation = delivery.RemoteBranchMissing{}
	if planned.PreviousSHA != "" {
		expected = delivery.OwnedRemoteBranchAt{CommitSHA: planned.PreviousSHA, Ownership: delivery.BranchOwnershipEvidence{Owner: owner, EstablishedByOperationID: planned.PreviousOwnerOperation}}
	}
	result, err := adapter.PublishBranch(ctx, delivery.PublishBranchRequest{OperationID: id, LocalRepository: p.Path, RemoteName: n.Executor.Remote, Owner: owner, Timing: delivery.PublishWorkflowCommit{CommitSHA: commit.SHA}, Destination: planned.Destination, ExpectedRemote: expected})
	if err != nil {
		return nil, err
	}
	planned.PreviousSHA = result.CommitSHA
	planned.PreviousOwnerOperation = result.Ownership.EstablishedByOperationID
	raw, _ := json.Marshal(planned)
	_, err = db.ExecContext(ctx, `INSERT INTO workflow_publications(workspace_id,remote,record) VALUES(?,?,?) ON CONFLICT(workspace_id,remote) DO UPDATE SET record=excluded.record`, p.ID, n.Executor.Remote, string(raw))
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Branch publishedWorkflowBranch `json:"branch"`
	}{publishedWorkflowBranch{commit, n.Executor.Remote}})
}

func (s workflowDeliveryServices) createPR(ctx context.Context, db *sql.DB, id, digest string, n workflow.CreatePRNode, inputs nodes.Inputs) (json.RawMessage, error) {
	p, err := s.wiring.resolvePreparedWorkspace(ctx, s.request, inputs[n.Executor.WorkspaceInput])
	if err != nil {
		return nil, err
	}
	var branch publishedWorkflowBranch
	var text deliveryText
	if err = json.Unmarshal(inputs[n.Executor.BranchInput], &branch); err != nil {
		return nil, err
	}
	if err = json.Unmarshal(inputs[n.Executor.TextInput], &text); err != nil {
		return nil, err
	}
	if branch.WorkspaceID != p.ID || branch.Branch != p.Branch || branch.SHA != p.HeadSHA || text.ChangesetSnapshot != branch.SnapshotDigest || len(branch.SnapshotDigest) != 64 {
		return nil, errors.New("PR inputs must describe the connected workspace and published snapshot")
	}
	adapter, err := githubcli.New(githubcli.Options{})
	if err != nil {
		return nil, err
	}
	coordinates, err := deliveryJournal(ctx, db, id, digest, func() (delivery.ChangeRequestCoordinates, error) {
		var raw string
		if err := db.QueryRowContext(ctx, `SELECT record FROM workflow_publications WHERE workspace_id=? AND remote=?`, p.ID, branch.Remote).Scan(&raw); err != nil {
			return delivery.ChangeRequestCoordinates{}, errors.New("connect a branch published by this run")
		}
		var published publicationIntent
		if err := json.Unmarshal([]byte(raw), &published); err != nil {
			return delivery.ChangeRequestCoordinates{}, err
		}
		if published.PreviousSHA != branch.SHA {
			return delivery.ChangeRequestCoordinates{}, errors.New("published branch changed since the connected output")
		}
		health, err := adapter.ProbeHealth(ctx, delivery.HealthRequest{LocalRepository: p.Path, RemoteName: branch.Remote})
		if err != nil {
			return delivery.ChangeRequestCoordinates{}, err
		}
		if health.Repository != published.Destination.Repository {
			return delivery.ChangeRequestCoordinates{}, errors.New("PR remote differs from the published repository")
		}
		base := n.Executor.Base
		if base == "remote_default" {
			base = health.BaseBranch.Name
		}
		return delivery.ChangeRequestCoordinates{Base: delivery.BranchRef{Repository: health.Repository, Name: base}, Head: published.Destination}, nil
	})
	if err != nil {
		return nil, err
	}
	result, err := adapter.CreateChangeRequest(ctx, delivery.CreateChangeRequestRequest{OperationID: id, Coordinates: coordinates, Owner: delivery.ChangeRequestOwner{DeliveryLineID: s.request.Run.RunID, WorkItemID: s.request.WorkItem.WorkItemID}, Title: text.PRTitle, Intent: delivery.CreateWorkflowChangeRequest{HeadSHA: branch.SHA, Body: text.PRBody, Draft: n.Executor.Draft}})
	if err != nil {
		return nil, err
	}
	var pr delivery.ChangeRequest
	switch outcome := result.Outcome.(type) {
	case delivery.ChangeRequestCreated:
		pr = outcome.ChangeRequest
	case delivery.ChangeRequestReconciled:
		pr = outcome.ChangeRequest
	default:
		return nil, errors.New("unexpected PR creation outcome")
	}
	number, err := strconv.Atoi(pr.Ref.ID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		PullRequest workflowPR `json:"pull_request"`
	}{workflowPR{pr.URL, number, branch.SHA, coordinates.Base.Name}})
}
