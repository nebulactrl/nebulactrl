// Package catalog decides, for every operation of the REST API, what the MCP
// server does with it.
//
// Every operationId in openapi/openapi.json has exactly one Entry in Manifest:
//
//   - Curated: a hand-named tool (noun_verb) with an agent-oriented
//     description, a result projection and accurate annotations.
//   - Generic: reachable only through the call_operation meta-tool (and found
//     through find_operation). It still carries toolset, hints, production
//     guard and confirmation metadata.
//   - Deny: never callable over MCP, with a reason from the closed set below.
//
// The catalog never imports internal/mcp (the registry imports the catalog),
// and it does not carry scopes: those are declared on the huma operation
// (api.WithScopes / api.ScopesOf) and are the single source of truth for
// "which token may see this tool".
package catalog

// Kind discriminates the Entry implementations.
type Kind string

const (
	KindCurated Kind = "curated"
	KindGeneric Kind = "generic"
	KindDeny    Kind = "deny"
)

// Toolset groups tools so a token (or /mcp?toolsets=...) can narrow what is
// listed. Composites and the two meta-tools live in ToolsetCore too, but they
// are not catalog entries.
type Toolset string

const (
	ToolsetCore    Toolset = "core"
	ToolsetConfig  Toolset = "config"
	ToolsetData    Toolset = "data"
	ToolsetServers Toolset = "servers"
	ToolsetObserve Toolset = "observe"
)

// Toolsets lists every toolset in display order.
func Toolsets() []Toolset {
	return []Toolset{ToolsetCore, ToolsetConfig, ToolsetData, ToolsetServers, ToolsetObserve}
}

// Valid reports whether t is one of the five toolsets.
func (t Toolset) Valid() bool {
	for _, k := range Toolsets() {
		if k == t {
			return true
		}
	}
	return false
}

// DenyReason is why an operation is never callable over MCP.
type DenyReason string

const (
	// ReasonSessionOnly: the API refuses tokens (browser account management).
	ReasonSessionOnly DenyReason = "session-only"
	// ReasonRevealsSecret: the response carries a credential in plain text.
	ReasonRevealsSecret DenyReason = "reveals-secret"
	// ReasonMintsCredential: the call creates or rotates a credential, link or
	// token whose value is returned (a privilege-escalation path).
	ReasonMintsCredential DenyReason = "mints-credential"
	// ReasonInstanceAdmin: instance, tenancy or admin-scope settings (SSO, team
	// security and roles, instance backup, third-party credential stores).
	ReasonInstanceAdmin DenyReason = "instance-admin"
	// ReasonHostRoot: grants or changes root access to a host or widens what
	// containers may do on it.
	ReasonHostRoot DenyReason = "host-root"
	// ReasonAuthPlumbing: sign-in, 2FA, passkey, password-reset, OIDC and
	// bootstrap endpoints.
	ReasonAuthPlumbing DenyReason = "auth-plumbing"
	// ReasonStreaming: server-sent events; the composites offer non-follow
	// tails instead.
	ReasonStreaming DenyReason = "streaming"
	// ReasonBinary: the response is not JSON.
	ReasonBinary DenyReason = "binary"
	// ReasonSuperseded: a composite covers the operation and adds the
	// production guard, plan or redaction the raw endpoint lacks.
	ReasonSuperseded DenyReason = "superseded-by-composite"
	// ReasonPersonOnly: a destructive saga that a person reviews and confirms
	// in the web UI or CLI (merging services deletes the originals).
	ReasonPersonOnly DenyReason = "person-only"
)

// DenyReasons lists every reason in display order.
func DenyReasons() []DenyReason {
	return []DenyReason{
		ReasonSessionOnly, ReasonRevealsSecret, ReasonMintsCredential, ReasonInstanceAdmin,
		ReasonHostRoot, ReasonAuthPlumbing, ReasonStreaming, ReasonBinary, ReasonSuperseded, ReasonPersonOnly,
	}
}

// Valid reports whether r is one of the closed set.
func (r DenyReason) Valid() bool {
	for _, k := range DenyReasons() {
		if k == r {
			return true
		}
	}
	return false
}

// Hints are the four MCP tool annotations. All four are always set; a table
// test checks them against the HTTP method and the production-guard list.
type Hints struct {
	// ReadOnly: the call changes nothing (no state, no audit-visible effect
	// beyond the MCP call itself).
	ReadOnly bool
	// Destructive: the call deletes or overwrites something that is not
	// trivially recreatable, or interrupts running work for good.
	Destructive bool
	// Idempotent: repeating the call with the same arguments has no further
	// effect.
	Idempotent bool
	// OpenWorld: the call talks to systems outside NebulaCtrl (git hosts, DNS,
	// Cloudflare, registries, notification endpoints, user containers).
	OpenWorld bool
}

// Detail is the shared detail argument of read tools.
type Detail string

const (
	DetailConcise Detail = "concise"
	DetailFull    Detail = "full"
)

// Confirm describes a REST confirmation argument the caller must echo for an
// irreversible operation. It is a speed bump and a statement of intent, never
// the production control (see ProdGuard).
type Confirm struct {
	// Field is the argument name (a query parameter or body property).
	Field string
	// Echo says what the value must equal, e.g. "the service slug".
	Echo string
	// When restricts the confirmation to a mode ("mode=in_place"); empty means
	// always.
	When string
}

// Entry is the closed sum Curated | Generic | Deny. Values (not pointers) are
// stored in Manifest; switch on the concrete type.
type Entry interface {
	// OperationID is the OpenAPI operationId the entry decides.
	OperationID() string
	Kind() Kind
}

// Curated is a named MCP tool generated from one huma operation.
type Curated struct {
	// Op is the operationId (filled from the manifest key).
	Op string
	// Name is the unique snake_case tool name (noun_verb, except the nine
	// native tools, which are named verb_noun).
	Name    string
	Toolset Toolset
	// Description is the agent-facing text: what the tool does, when to use
	// it, its staged semantics, its production guard and its key arguments.
	Description string
	// Projection shrinks large results; nil returns the response unchanged.
	Projection *Projection
	Hints      Hints
	// ProdGuard: the domain service requires approval on production
	// environments for MCP principals (internal/prodguard). The registry adds
	// the approval flow to the tool; the description says so.
	ProdGuard bool
	// Confirm is the REST confirmation argument, if the operation has one.
	Confirm *Confirm
	// Native marks tools whose handler is hand-written in internal/mcp because
	// the result needs shaping the generator cannot do (log capture, polling,
	// outcome_uncertain). The entry still owns name, toolset, description and
	// hints so the registry, tests and docs agree.
	Native bool
}

func (c Curated) OperationID() string { return c.Op }
func (Curated) Kind() Kind            { return KindCurated }

// Generic is an allowed operation without a dedicated tool; it is reached
// through find_operation/call_operation.
type Generic struct {
	Op        string
	Toolset   Toolset
	Hints     Hints
	ProdGuard bool
	Confirm   *Confirm
}

func (g Generic) OperationID() string { return g.Op }
func (Generic) Kind() Kind            { return KindGeneric }

// Deny is an operation that is never callable over MCP.
type Deny struct {
	Op     string
	Reason DenyReason
	// Why is the specific justification, shown in docs and in the error a
	// caller gets when it asks for the operation.
	Why string
}

func (d Deny) OperationID() string { return d.Op }
func (Deny) Kind() Kind            { return KindDeny }
