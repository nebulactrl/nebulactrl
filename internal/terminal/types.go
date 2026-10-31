// Package terminal serves the web terminal and the file browser: an
// interactive shell and file access inside a replica container of a service,
// relayed browser ⇄ control plane ⇄ agent ⇄ Docker.
//
// The browser talks to the control plane over one WebSocket per terminal
// session (GET /api/v1/services/{id}/terminal/ws) and plain HTTP for files.
// The control plane relays to the agent's existing connection as ephemeral
// stream frames (pkg/agentprotocol/exec.go). Access needs the Owner, Admin or
// Developer role and the deployments:write scope; production additionally
// needs an explicit confirmation. Every session is audited (terminal.open and
// terminal.close with the replica and the duration); the terminal's input and
// output are relayed, never stored or logged.
package terminal

import (
	"time"

	"github.com/google/uuid"
)

// Limits and defaults.
const (
	// MaxSessionsPerUser bounds the concurrent terminal sessions of one
	// principal.
	MaxSessionsPerUser = 3
	// IdleTimeout ends a session without keyboard input.
	IdleTimeout = 15 * time.Minute
	// IdleWarning is how long before the idle timeout the client is warned.
	IdleWarning = time.Minute
	// PreviewLimit caps the bytes the in-browser file preview reads.
	PreviewLimit = 1 << 20
	// MaxCommandArgs and MaxCommandArgLen bound a user-chosen command.
	MaxCommandArgs   = 32
	MaxCommandArgLen = 4096
	// MaxReplica bounds the replica index a request may name.
	MaxReplica = 999
)

// Audit actions.
const (
	ActionOpen           = "terminal.open"
	ActionClose          = "terminal.close"
	ActionFileRead       = "file.read"
	ActionFileUpload     = "file.upload"
	ActionSettingsUpdate = "project.terminal_settings.update"
)

// Session end reasons recorded in terminal.close.
const (
	ReasonExit              = "exit"
	ReasonClientClosed      = "client_closed"
	ReasonIdle              = "idle"
	ReasonAgentDisconnected = "agent_disconnected"
	ReasonError             = "error"
)

// Error codes sent to the browser (WebSocket error messages) and returned by
// the file endpoints.
const (
	CodeConfirmationRequired = "confirmation_required"
	CodeTooManySessions      = "too_many_sessions"
	CodeServerOffline        = "server_offline"
	CodeNotDeployed          = "not_deployed"
	CodeReplicaNotRunning    = "replica_not_running"
	CodeProcessNotFound      = "not_found" // the service has no process of the requested name
	CodeOpenFailed           = "open_failed"
	CodeInvalidRequest       = "invalid_request"
	CodeProductionReadOnly   = "production_read_only"
)

// ReplicaView is one replica container of a service.
type ReplicaView struct {
	Index   int    `json:"index" doc:"0-based replica index within its process."`
	Process string `json:"process" doc:"The process the replica belongs to; empty for a service without processes."`
	State   string `json:"state" doc:"Observed container state: running, exited, restarting, …"`
	Health  string `json:"health,omitempty"`
	Running bool   `json:"running" doc:"The replica runs, so a shell or the file browser can attach to it."`
}

// FileWritePolicy says whether the file browser may write.
type FileWritePolicy struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty" doc:"Why writes are off (production services are read-only unless an admin allows writes for the project)."`
}

// InfoView is an alias: the schema is named TerminalInfoView so it cannot
// collide with other domains' InfoView in the OpenAPI registry.
type InfoView = TerminalInfoView

// TerminalInfoView is what the UI needs before opening a terminal or the file browser.
type TerminalInfoView struct {
	ServiceID          uuid.UUID       `json:"serviceId"`
	Available          bool            `json:"available" doc:"A terminal can be opened now."`
	Reason             string          `json:"reason,omitempty" doc:"Why not, when unavailable."`
	Production         bool            `json:"production" doc:"The service runs in a production environment: opening a terminal needs confirmProduction."`
	Replicas           []ReplicaView   `json:"replicas"`
	ServerName         string          `json:"serverName,omitempty"`
	MaxSessions        int             `json:"maxSessions"`
	ActiveSessions     int             `json:"activeSessions" doc:"Terminal sessions the caller has open right now."`
	IdleTimeoutSeconds int             `json:"idleTimeoutSeconds"`
	FileWrites         FileWritePolicy `json:"fileWrites"`
	MaxUploadBytes     int64           `json:"maxUploadBytes"`
	PreviewBytes       int64           `json:"previewBytes"`
}

// FileEntry is one entry of a directory listing.
type FileEntry struct {
	Name       string     `json:"name"`
	Type       string     `json:"type" enum:"file,dir,link,other"`
	Size       int64      `json:"size"`
	Mode       string     `json:"mode,omitempty" doc:"Octal permission bits, e.g. 0755."`
	ModifiedAt *time.Time `json:"modifiedAt,omitempty"`
	LinkTarget string     `json:"linkTarget,omitempty"`
}

// FileListing is a directory listing inside a replica container.
type FileListing struct {
	Path      string      `json:"path" doc:"The resolved directory path."`
	Replica   int         `json:"replica"`
	Entries   []FileEntry `json:"entries"`
	Truncated bool        `json:"truncated" doc:"The directory has more entries than are listed."`
	Writable  bool        `json:"writable" doc:"Uploads are allowed here."`
	// ReadOnlyReason explains Writable=false.
	ReadOnlyReason string `json:"readOnlyReason,omitempty"`
}

// SettingsView is an alias of TerminalSettingsView (see InfoView).
type SettingsView = TerminalSettingsView

// TerminalSettingsView is a project's terminal settings.
type TerminalSettingsView struct {
	AllowProductionFileWrites bool `json:"allowProductionFileWrites" doc:"Let the file browser upload files in production environments of this project (default off: read-only)."`
}

// UploadResult answers a file upload.
type UploadResult struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}
