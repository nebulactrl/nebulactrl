package catalog

// Status pages, status incidents and maintenance windows. Reading them is
// observation like the service incidents; writing them publishes statements
// on the public internet (a page served on its own hostname, incident text,
// maintenance announcements) and decides what the workspace's customers see:
// reserved to workspace owners and admins in the web UI.
const whyStatusWrite = "Creates, publishes or edits what the workspace says in public: a status page served on its own hostname, its incident text and maintenance announcements. A statement to customers is a decision for a person in the web UI."

// Changing a page's DNS moves a public hostname (and its Cloudflare proxy and origin lock): like the page itself, a person's decision.
const whyStatusDNSWrite = "Changes where a public status page's hostname points in Cloudflare DNS and whether it is proxied and locked to Cloudflare. The page is a public statement served on the workspace's own hostname: a decision for a person in the web UI."

// statusPageGenericEntries are the allowed status page operations without a curated tool.
var statusPageGenericEntries = map[string]Generic{
	"listStatusPages":         rd(tObserve),
	"getStatusPage":           rd(tObserve),
	"checkStatusPageDNS":      rd(tObserve),
	"getStatusPageCloudflare": rd(tObserve),
	"listStatusIncidents":     rd(tObserve),
	"getStatusIncident":       rd(tObserve),
	"listMaintenanceWindows":  rd(tObserve),
	"getMaintenanceWindow":    rd(tObserve),
}

// statusPageDenyEntries are the status page operations never callable over MCP.
var statusPageDenyEntries = map[string]Deny{
	"createStatusPage":           {Reason: ReasonInstanceAdmin, Why: whyStatusWrite},
	"updateStatusPage":           {Reason: ReasonInstanceAdmin, Why: whyStatusWrite},
	"deleteStatusPage":           {Reason: ReasonInstanceAdmin, Why: whyStatusWrite},
	"setStatusPageComponents":    {Reason: ReasonInstanceAdmin, Why: whyStatusWrite},
	"updateStatusPageCloudflare": {Reason: ReasonInstanceAdmin, Why: whyStatusDNSWrite},
	"fixStatusPageCloudflare":    {Reason: ReasonInstanceAdmin, Why: whyStatusDNSWrite},
	"createStatusIncident":       {Reason: ReasonInstanceAdmin, Why: whyStatusWrite},
	"updateStatusIncident":       {Reason: ReasonInstanceAdmin, Why: whyStatusWrite},
	"postStatusIncidentUpdate":   {Reason: ReasonInstanceAdmin, Why: whyStatusWrite},
	"deleteStatusIncident":       {Reason: ReasonInstanceAdmin, Why: whyStatusWrite},
	"createMaintenanceWindow":    {Reason: ReasonInstanceAdmin, Why: whyStatusWrite},
	"updateMaintenanceWindow":    {Reason: ReasonInstanceAdmin, Why: whyStatusWrite},
	"cancelMaintenanceWindow":    {Reason: ReasonInstanceAdmin, Why: whyStatusWrite},
	"deleteMaintenanceWindow":    {Reason: ReasonInstanceAdmin, Why: whyStatusWrite},
}
