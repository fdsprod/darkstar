// Package platform defines operating-system strategy operations required by the
// runtime. Core code uses this package and contains no OS conditionals.
package platform

import (
	"context"
	"io"
	"time"
)

// PathResolver owns application-data path semantics independently of the other
// operating-system capabilities.
type PathResolver interface {
	ResolvePaths(context.Context, PathRequest) (Paths, error)
}

// Strategy groups the OS capabilities whose implementations must agree on path,
// file, process-ownership, and executable-identity semantics.
type Strategy interface {
	PathResolver
	AcquireDaemonLock(context.Context, LockRequest) (DaemonLock, error)
	PublishEndpoint(context.Context, EndpointRequest) error
	InspectProcess(context.Context, ProcessIdentity) (ProcessObservation, error)
	StartOwnedProcess(context.Context, ProcessRequest) (OwnedProcess, error)
	RequestGracefulStop(context.Context, GracefulStopRequest) (StopObservation, error)
	TerminateOwnedTree(context.Context, TerminateRequest) (StopObservation, error)
	OpenTerminal(context.Context, TerminalRequest) (Terminal, error)
	AtomicReplace(context.Context, AtomicReplaceRequest) error
	ResolveExecutable(context.Context, ExecutableSpec) (Executable, error)
}

type PathRequest struct {
	ApplicationName string
}

type Paths struct {
	Config  string
	Data    string
	Cache   string
	Logs    string
	Runtime string
}

type LockRequest struct {
	Path      string
	OwnerHint ProcessIdentity
}

// DaemonLock must remain open for the daemon lifetime. Closing releases only the
// exact lock represented by this handle.
type DaemonLock interface {
	Path() string
	ExistingOwner() *ProcessIdentity
	Close() error
}

type EndpointRequest struct {
	Path  string
	State EndpointState
}

type EndpointState struct {
	SchemaVersion    int
	PID              int
	ProcessStartTime time.Time
	Port             int
	Token            string
	CreatedAt        time.Time
}

// ProcessIdentity deliberately requires more than a PID. Empty fields mean the
// caller lacks sufficient ownership proof and must not terminate the process.
type ProcessIdentity struct {
	HostID           string
	HostBootID       string
	PID              int
	ProcessStartTime time.Time
	ExecutableDigest string
	CommandDigest    string
	OwnerNonce       string
	AttemptID        string
}

type ProcessOutcome interface{ isProcessOutcome() }

type ProcessRunning struct{}

func (ProcessRunning) isProcessOutcome() {}

type ProcessExited struct{ ExitCode int }

func (ProcessExited) isProcessOutcome() {}

type ProcessAbsent struct{}

func (ProcessAbsent) isProcessOutcome() {}

type ProcessUncertain struct{}

func (ProcessUncertain) isProcessOutcome() {}

type ProcessObservation struct {
	Outcome     ProcessOutcome
	Identity    ProcessIdentity
	ObservedAt  time.Time
	EvidenceRef string
}

type ProcessRequest struct {
	AttemptID    string
	OwnerNonce   string
	Executable   Executable
	Arguments    []string
	Directory    string
	Environment  map[string]string
	RedirectedIO bool
}

type OwnedProcess interface {
	Identity() ProcessIdentity
	StandardInput() io.WriteCloser
	StandardOutput() io.ReadCloser
	StandardError() io.ReadCloser
	Wait(context.Context) (ProcessObservation, error)
	Close() error
}

type GracefulStopRequest struct {
	Identity ProcessIdentity
	Method   string
	Deadline time.Time
}

type TerminateRequest struct {
	Identity ProcessIdentity
	Reason   string
}

type StopDisposition string

const (
	StopNotRequested StopDisposition = "not_requested"
	StopRequested    StopDisposition = "requested"
	StopTerminated   StopDisposition = "terminated"
	StopUncertain    StopDisposition = "uncertain"
)

type StopObservation struct {
	Disposition StopDisposition
	Observation ProcessObservation
}

type TerminalRequest struct {
	Process ProcessRequest
	Columns int
	Rows    int
}

type Terminal interface {
	Process() OwnedProcess
	Input() io.WriteCloser
	Output() io.ReadCloser
	Resize(context.Context, int, int) error
	Close() error
}

type AtomicReplaceRequest struct {
	Path        string
	Content     []byte
	Permissions FilePermissions
}

type FilePermissions struct {
	UserOnly bool
}

type ExecutableSpec struct {
	ConfiguredPath       string
	Name                 string
	AllowedRoots         []string
	RequiredVersion      string
	RequireTrustedSigner bool
}

type Executable struct {
	Path         string
	FileID       string
	Digest       string
	Version      string
	Source       string
	Signer       string
	IsScriptShim bool
}
