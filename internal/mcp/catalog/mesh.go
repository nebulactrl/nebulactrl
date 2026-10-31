package catalog

// The managed server mesh. Reads and the connectivity test are
// allowed; anything that adds tagged devices to the team's tailnet (billing),
// changes ACL mode or reads the tailnet policy is reserved to people in the web
// UI. Every operation declares its scopes with api.WithScopes.
const (
	whyMeshDevices = "Turning the mesh on or off adds or removes tagged devices on the team's tailnet (they may be billed) and changes who can reach what: a decision for a person in the web UI."
	whyMeshACL     = "Reads or writes the tailnet access policy (admin scope): reserved to people in the web UI."
)

// meshGenericEntries are the allowed mesh operations without a curated tool.
var meshGenericEntries = map[string]Generic{
	"getMesh":       rd(tServers),
	"getServerMesh": rd(tServers),
	// The agent dials a mesh endpoint and reports the outcome (records nothing).
	"checkServerMesh": pr(tServers),
	// New device and tailnet address, like reprovisionServerTailnet.
	"reprovisionServerMesh": {Toolset: tServers, Hints: Hints{Destructive: true, OpenWorld: true}},
	"reprovisionMeshLink":   {Toolset: tServers, Hints: Hints{Destructive: true, OpenWorld: true}, ProdGuard: true},
}

// meshDenyEntries are the mesh operations never callable over MCP.
var meshDenyEntries = map[string]Deny{
	"setServerMesh":      {Reason: ReasonInstanceAdmin, Why: whyMeshDevices},
	"updateMeshSettings": {Reason: ReasonInstanceAdmin, Why: whyMeshACL},
	"previewMeshACL":     {Reason: ReasonInstanceAdmin, Why: whyMeshACL},
	"applyMeshACL":       {Reason: ReasonInstanceAdmin, Why: whyMeshACL},
	"checkMeshACL":       {Reason: ReasonInstanceAdmin, Why: whyMeshACL},
}
