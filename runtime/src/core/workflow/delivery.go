package workflow

import "encoding/json"

const NodeGitCommit NodeType = "git_commit"

type GitCommitExecutor struct {
	WorkspaceInput Identifier `json:"workspaceInput"`
	ChangesetInput Identifier `json:"changesetInput"`
	TextInput      Identifier `json:"textInput"`
}
type GitCommitNode struct {
	Common   NodeFields
	Executor GitCommitExecutor
}

func (GitCommitNode) isNode() {}
func (GitCommitNode) Type() NodeType {
	return NodeGitCommit
}
func (n GitCommitNode) Fields() NodeFields {
	return n.Common
}
func (n GitCommitNode) MarshalJSON() ([]byte, error) {
	return json.Marshal(nodeObject(n.Common, n.Type(), "gitCommit", n.Executor))
}

const NodeGitPush NodeType = "git_push"

type GitPushExecutor struct {
	WorkspaceInput Identifier `json:"workspaceInput"`
	CommitInput    Identifier `json:"commitInput"`
	Remote         string     `json:"remote"`
}
type GitPushNode struct {
	Common   NodeFields
	Executor GitPushExecutor
}

func (GitPushNode) isNode() {}
func (GitPushNode) Type() NodeType {
	return NodeGitPush
}
func (n GitPushNode) Fields() NodeFields {
	return n.Common
}
func (n GitPushNode) MarshalJSON() ([]byte, error) {
	return json.Marshal(nodeObject(n.Common, n.Type(), "gitPush", n.Executor))
}

const NodeCreatePR NodeType = "create_pr"

type CreatePRExecutor struct {
	WorkspaceInput Identifier `json:"workspaceInput"`
	BranchInput    Identifier `json:"branchInput"`
	TextInput      Identifier `json:"textInput"`
	Base           string     `json:"base"`
	Draft          bool       `json:"draft"`
}
type CreatePRNode struct {
	Common   NodeFields
	Executor CreatePRExecutor
}

func (CreatePRNode) isNode() {}
func (CreatePRNode) Type() NodeType {
	return NodeCreatePR
}
func (n CreatePRNode) Fields() NodeFields {
	return n.Common
}
func (n CreatePRNode) MarshalJSON() ([]byte, error) {
	return json.Marshal(nodeObject(n.Common, n.Type(), "createPR", n.Executor))
}
