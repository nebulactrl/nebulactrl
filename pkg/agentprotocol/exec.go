package agentprotocol

// Interactive streams: terminal sessions (exec with a TTY) and the file
// browser. Unlike commands they are ephemeral: nothing is persisted or
// redelivered, a session ends when either side closes it or the connection
// drops, and every frame of one exchange carries the exchange's ID in
// Envelope.ID (the session ID of a terminal, the request ID of a file
// operation). Neither side ever records stream content.
//
// Flow control. A sender may have at most StreamWindow unacknowledged payload
// bytes in flight; the receiver sends stream_ack{streamId, bytes} after it
// consumed bytes (wrote terminal output to the browser, stored a file chunk).
// Without it a `cat` of a large file would flood the agent's shared outbound
// queue, which also carries heartbeats and deployment events.
const (
	// Control plane → agent.
	TypeExecOpen   = "exec_open"
	TypeExecInput  = "exec_input"
	TypeExecResize = "exec_resize"
	TypeExecClose  = "exec_close"
	TypeFSRequest  = "fs_request"

	// Agent → control plane.
	TypeExecOpened = "exec_opened"
	TypeExecOutput = "exec_output"
	TypeExecExit   = "exec_exit"
	TypeFSResponse = "fs_response"

	// Both directions.
	TypeStreamAck = "stream_ack"
	TypeFSData    = "fs_data"
)

// IsStreamType reports whether typ belongs to the interactive stream family.
func IsStreamType(typ string) bool {
	switch typ {
	case TypeExecOpen, TypeExecInput, TypeExecResize, TypeExecClose, TypeFSRequest, TypeVolFileRequest, TypeDumpRestoreRequest,
		TypeExecOpened, TypeExecOutput, TypeExecExit, TypeFSResponse, TypeStreamAck, TypeFSData, TypeDumpRestoreEvent,
		TypeTunnelOpen, TypeTunnelOpened, TypeTunnelAck, TypeTunnelClose:
		return true
	}
	return false
}

// Stream limits shared by both ends.
const (
	// StreamWindow is the most unacknowledged payload bytes a sender keeps in flight.
	StreamWindow = 256 << 10
	// ExecChunk is the most terminal bytes in one exec_output / exec_input frame.
	ExecChunk = 16 << 10
	// FSChunk is the most file bytes in one fs_data frame.
	FSChunk = 128 << 10
	// MaxStreamQueueBytes bounds the encoded bytes of agent frames the control
	// plane holds for one stream that its owner has not consumed yet. A sender
	// within StreamWindow stays far below it; a frame or a backlog above it ends
	// the stream.
	MaxStreamQueueBytes = 2 << 20
	// MaxUploadBytes is the largest file the browser may upload.
	MaxUploadBytes = 50 << 20
	// MaxFileReadBytes caps a file read (preview or download).
	MaxFileReadBytes = 1 << 30
	// MaxListEntries caps one directory listing.
	MaxListEntries = 2000
	// MinTermSize and MaxTermSize bound the terminal's columns and rows.
	MinTermSize = 1
	MaxTermSize = 1000
)

// DefaultTerm is the TERM value of terminal sessions.
const DefaultTerm = "xterm-256color"

// ExecOpen starts an interactive session: in a replica container of a
// service, or - with Host - a shell on the server itself.
type ExecOpen struct {
	SessionID string `json:"sessionId"`
	// ServiceID names the service of a container session; empty for Host.
	ServiceID string `json:"serviceId"`
	// Replica is the replica index (0-based) of the service's current deployment.
	Replica int `json:"replica"`
	// Process is the process of a multi-process service the replica belongs
	// to; Replica is then the index within the process. "" is the default process.
	Process string `json:"process,omitempty"`
	// Command is the argv to run. Empty picks bash when the image has it, else sh.
	// A host session never takes a command: it is always the host's login shell.
	Command []string `json:"command,omitempty"`
	Cols    int      `json:"cols"`
	Rows    int      `json:"rows"`
	// Host asks for a shell on the server itself (the opt-in host shell; the
	// control plane has checked role, 2FA and the server's setting). An agent
	// on the host spawns the shell itself; an agent in a container runs it
	// through a short-lived privileged helper container (LabelManagedHostShell).
	Host bool `json:"host,omitempty"`
}

// Host shell helper containers (agent running in a container).
const (
	// HostShellContainerPrefix names the helper: nebulactrl-hostshell-<sessionId8>.
	HostShellContainerPrefix = "nebulactrl-hostshell-"
	// LabelManagedHostShell is the value of LabelManaged on a helper container.
	// It is not "true", so the service sweep never mistakes a helper for a
	// service container; the host-shell sweep removes helpers of dead sessions.
	LabelManagedHostShell = "hostshell"
	// LabelHostShellSession carries the session ID on a helper container.
	LabelHostShellSession = "nebulactrl.hostshell.session"
)

// ExecOpened answers ExecOpen. Error is empty when the session runs.
type ExecOpened struct {
	SessionID string `json:"sessionId"`
	Error     string `json:"error,omitempty"`
	// Code classifies a failure: not_found (no such running replica) or failed.
	Code string `json:"code,omitempty"`
	// Container is the name of the container the session runs in.
	Container string `json:"container,omitempty"`
	// Command is the argv that actually runs.
	Command []string `json:"command,omitempty"`
}

// ExecInput carries terminal input (keystrokes, pasted text).
type ExecInput struct {
	SessionID string `json:"sessionId"`
	Data      []byte `json:"data"`
}

// ExecResize changes the terminal size.
type ExecResize struct {
	SessionID string `json:"sessionId"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
}

// ExecClose ends a session; the agent kills the process.
type ExecClose struct {
	SessionID string `json:"sessionId"`
}

// ExecOutput carries terminal output (a TTY has one merged stream).
type ExecOutput struct {
	SessionID string `json:"sessionId"`
	Data      []byte `json:"data"`
}

// ExecExit reports that the session's process ended.
type ExecExit struct {
	SessionID string `json:"sessionId"`
	ExitCode  int    `json:"exitCode"`
	// Error is set when the stream broke instead of the process exiting.
	Error string `json:"error,omitempty"`
}

// StreamAck credits Bytes of payload back to the sender of a stream.
type StreamAck struct {
	StreamID string `json:"streamId"`
	Bytes    int    `json:"bytes"`
}

// File operations.
const (
	FSList  = "list"
	FSRead  = "read"
	FSWrite = "write"
)

// FSRequest is one file-browser operation in a replica container. Paths are
// absolute paths inside the container; the agent never touches the host
// filesystem (it reads and writes through the Docker API).
type FSRequest struct {
	RequestID string `json:"requestId"`
	Op        string `json:"op"`
	ServiceID string `json:"serviceId"`
	Replica   int    `json:"replica"`
	// Process is the process of a multi-process service the replica belongs
	// to; Replica is then the index within the process. "" is the default process.
	Process string `json:"process,omitempty"`
	Path    string `json:"path"`
	// Size is the length of the file an FSWrite request streams in fs_data frames.
	Size int64 `json:"size,omitempty"`
	// Mode is the permission bits of an uploaded file (default 0644).
	Mode uint32 `json:"mode,omitempty"`
	// MaxBytes caps an FSRead (0 = MaxFileReadBytes).
	MaxBytes int64 `json:"maxBytes,omitempty"`
}

// File entry types.
const (
	FSTypeFile  = "file"
	FSTypeDir   = "dir"
	FSTypeLink  = "link"
	FSTypeOther = "other"
)

// FSEntry describes one file or directory.
type FSEntry struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Size       int64  `json:"size"`
	Mode       string `json:"mode,omitempty"`  // octal permission bits, e.g. "0755"
	MTime      int64  `json:"mtime,omitempty"` // unix seconds
	LinkTarget string `json:"linkTarget,omitempty"`
}

// FS response stages.
const (
	// FSReady: an FSRead was accepted (fs_data frames follow) or an FSWrite is
	// ready to receive fs_data frames.
	FSReady = "ready"
	// FSDone: the operation finished (FSList carries its entries; an FSWrite
	// its final outcome).
	FSDone = "done"
)

// FS error codes.
const (
	FSErrNotFound   = "not_found"
	FSErrNotDir     = "not_a_directory"
	FSErrIsDir      = "is_a_directory"
	FSErrTooLarge   = "too_large"
	FSErrNoReplica  = "replica_not_running"
	FSErrPermission = "permission_denied"
	FSErrFailed     = "failed"
)

// FSResponse answers an FSRequest. Error is empty on success.
type FSResponse struct {
	RequestID string `json:"requestId"`
	Stage     string `json:"stage,omitempty"`
	Error     string `json:"error,omitempty"`
	Code      string `json:"code,omitempty"`
	// Path is the resolved absolute path (symlinks of the final element followed for reads).
	Path string `json:"path,omitempty"`
	// Entries and Truncated answer FSList (sorted: directories first, then by name).
	Entries   []FSEntry `json:"entries,omitempty"`
	Truncated bool      `json:"truncated,omitempty"`
	// Entry describes the file of an FSRead (name, size, mode, mtime).
	Entry *FSEntry `json:"entry,omitempty"`
}

// FSData carries file bytes: agent → control plane for an FSRead, control
// plane → agent for an FSWrite. EOF marks the last frame (Data may be empty).
// Error aborts an FSRead midway.
type FSData struct {
	RequestID string `json:"requestId"`
	Data      []byte `json:"data,omitempty"`
	EOF       bool   `json:"eof,omitempty"`
	Error     string `json:"error,omitempty"`
}
