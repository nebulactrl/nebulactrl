package catalog

// The production guard has two enforcement points, and an MCP principal meets
// both (a REST token meets only the domain services):
//
//   - the domain service (internal/prodguard Check inside deployments,
//     services, tasks, backups, …), which holds every REST and MCP call to the
//     rule; the catalog marks these operations ProdGuard;
//   - the MCP dispatch itself (internal/mcp, prodgate.go), which describes the
//     operations listed in central below to the approver and finds their
//     environment; an approval it consumed covers the domain service's check
//     of the same call.
//
// Every other allowed, mutating operation that is reached through a
// production-scoped id (service, environment, domain, task, backup, …) is
// listed in prodExempt with the reason it needs no approval. A test fails for
// an operation that is in neither table, so a new operation cannot ship
// without someone deciding.

// Target says which id of the call identifies the production resource and how
// the dispatch maps it to environments.
type Target string

const (
	// TargetService: path/argument serviceId.
	TargetService Target = "service"
	// TargetTask: taskId, resolved to its service.
	TargetTask Target = "task"
	// TargetBackup: backupId of a database backup, resolved to its service.
	TargetBackup Target = "backup"
	// TargetVolumeBackup: backupId of a volume backup, resolved to its service.
	TargetVolumeBackup Target = "volume_backup"
	// TargetBackupSchedule: scheduleId, resolved to its service.
	TargetBackupSchedule Target = "backup_schedule"
	// TargetRetiredVolume: retiredId, resolved to its service.
	TargetRetiredVolume Target = "retired_volume"
	// TargetDomain: domainId, resolved to its service.
	TargetDomain Target = "domain"
	// TargetEnvironment: environmentId.
	TargetEnvironment Target = "environment"
	// TargetLink: linkId, resolved to its environment.
	TargetLink Target = "link"
	// TargetMeshLink: linkId of a mesh link node, resolved to its environment.
	TargetMeshLink Target = "mesh_link"
	// TargetServer: serverId; production when the server hosts a production
	// service.
	TargetServer Target = "server"
)

// Param is the name of the argument that carries the target's id.
func (t Target) Param() string {
	switch t {
	case TargetService:
		return "serviceId"
	case TargetTask:
		return "taskId"
	case TargetBackup, TargetVolumeBackup:
		return "backupId"
	case TargetBackupSchedule:
		return "scheduleId"
	case TargetRetiredVolume:
		return "retiredId"
	case TargetDomain:
		return "domainId"
	case TargetEnvironment:
		return "environmentId"
	case TargetLink:
		return "linkId"
	case TargetMeshLink:
		return "linkId"
	case TargetServer:
		return "serverId"
	}
	return ""
}

// Cond selects the calls of an operation that need approval. An empty When
// means every call does.
type Cond struct {
	// Arg is the argument name.
	Arg string
	// Equals is the value that triggers the guard; nil means "the argument is
	// present".
	Equals any
}

// Central describes an operation whose production guard the MCP dispatch
// enforces.
type Central struct {
	Target Target
	// Summary is one sentence for the approver; {name} is replaced by the name
	// of the service, environment or server.
	Summary string
	// Effects are the concrete consequences, one short sentence each.
	Effects []string
	// Irreversible marks operations that cannot be undone.
	Irreversible bool
	// Show lists the arguments the approver reads, besides the plan. The
	// whole argument set goes into the approval digest either way.
	Show []string
	// When restricts the guard to some calls: any matching Cond triggers it.
	When []Cond
}

var central = map[string]Central{
	// ── scheduled tasks: the scheduler runs the command in the container ──
	"createServiceTask": {
		Target: TargetService, Summary: "Schedule a command to run inside {name} in production",
		Effects: []string{"The command runs in the running container on every tick, with the service's secrets in its environment."},
		Show:    []string{"name", "schedule", "command", "enabled"},
	},
	"updateServiceTask": {
		Target: TargetTask, Summary: "Change a scheduled command of {name} in production",
		Effects: []string{"The new command, schedule or enabled flag applies from the next tick."},
		Show:    []string{"name", "schedule", "command", "enabled"},
	},
	"deleteServiceTask": {
		Target: TargetTask, Summary: "Delete a scheduled command of {name} in production",
		Effects: []string{"The task and its run history are removed."}, Irreversible: true,
	},
	// ── git: a push deploys ──
	"linkServiceGit": {
		Target: TargetService, Summary: "Link {name} in production to a git repository",
		Effects: []string{"The repository, branch and auto-deploy setting apply at once; with auto-deploy a push to the branch deploys the service."},
		Show:    []string{"repo", "branch", "autoDeploy", "watchPaths", "connectionId"},
	},
	"unlinkServiceGit": {
		Target: TargetService, Summary: "Unlink {name} in production from its git repository",
		Effects: []string{"Pushes stop deploying the service."},
	},
	// ── clone copies secrets ──
	"cloneService": {
		Target: TargetService, Summary: "Clone {name} from production, with its secrets",
		Effects: []string{"The copy gets the source's secret values, git token, registry credentials and basic-auth hashes, in an environment people may run commands in."},
		Show:    []string{"environmentId", "name", "domains", "domainSuffix"},
	},
	"cloneEnvironment": {
		Target: TargetEnvironment, Summary: "Clone the production environment {name}, with its secrets",
		Effects: []string{"Every copy gets the source's secret values, git tokens, registry credentials and basic-auth hashes, in an environment people may run commands in."},
		Show:    []string{"name", "slug", "dryRun"},
	},
	// ── DNS ──
	"fixDomainDns": {
		Target: TargetDomain, Summary: "Replace DNS records for a hostname of {name} in production",
		Effects: []string{"Records NebulaCtrl did not create are replaced at once; the public name points where NebulaCtrl says."},
		Show:    []string{"adopt"}, When: []Cond{{Arg: "adopt", Equals: true}},
	},
	"updateDomainDns": {
		Target: TargetDomain, Summary: "Change the DNS proxy or origin lock of a hostname of {name} in production",
		Effects: []string{"The change reaches Cloudflare at once; turning the origin lock off exposes the origin past Cloudflare."},
		Show:    []string{"originLock", "proxied"}, When: []Cond{{Arg: "originLock", Equals: false}, {Arg: "proxied"}},
	},
	// ── backups and volumes: data that cannot come back ──
	"deleteRetiredVolume": {
		Target: TargetRetiredVolume, Summary: "Delete a retired volume of {name} in production, with its data",
		Effects: []string{"The Docker volume and everything in it are removed from the host."}, Irreversible: true,
	},
	"deleteBackup": {
		Target: TargetBackup, Summary: "Delete a database backup of {name} in production",
		Effects: []string{"The dump is removed from its destination."}, Irreversible: true,
	},
	"deleteVolumeBackup": {
		Target: TargetVolumeBackup, Summary: "Delete a volume backup of {name} in production",
		Effects: []string{"The snapshot is removed from its destination."}, Irreversible: true,
	},
	"lockBackup": {
		Target: TargetBackup, Summary: "Unlock a database backup of {name} in production",
		Effects: []string{"The backup can then be deleted by retention or by hand."},
		Show:    []string{"locked"}, When: []Cond{{Arg: "locked", Equals: false}},
	},
	"lockVolumeBackup": {
		Target: TargetVolumeBackup, Summary: "Unlock a volume backup of {name} in production",
		Effects: []string{"The snapshot can then be deleted by retention or by hand."},
		Show:    []string{"locked"}, When: []Cond{{Arg: "locked", Equals: false}},
	},
	"createBackupSchedule": {
		Target: TargetService, Summary: "Add a backup schedule to {name} in production",
		Effects: []string{"Backups start on the new schedule."},
		Show:    []string{"cron", "destinationId", "retentionCount", "retentionDays", "enabled"},
	},
	"updateBackupSchedule": {
		Target: TargetBackupSchedule, Summary: "Change the backup schedule of {name} in production",
		Effects: []string{"Backups and their retention follow the new settings at once; shorter retention deletes older backups."},
		Show:    []string{"cron", "destinationId", "retentionCount", "retentionDays", "enabled"},
	},
	"deleteBackupSchedule": {
		Target: TargetBackupSchedule, Summary: "Delete the backup schedule of {name} in production",
		Effects: []string{"Scheduled backups stop."}, Irreversible: true,
	},
	"putVolumeBackupConfig": {
		Target: TargetService, Summary: "Change the volume backup settings of {name} in production",
		Effects: []string{"Volume backups and their retention follow the new settings at once."},
		Show:    []string{"enabled", "cron", "destinationId", "retentionCount", "retentionDays", "mode"},
	},
	"deleteVolumeBackupConfig": {
		Target: TargetService, Summary: "Delete the volume backup settings of {name} in production",
		Effects: []string{"Scheduled volume backups stop."}, Irreversible: true,
	},
	// ── exposure and connectivity ──
	"setDatabasePublicPort": {
		Target: TargetService, Summary: "Open the public database port of {name} in production",
		Effects: []string{"The database is reachable from the internet on the listen port, from the allowed addresses."},
		Show:    []string{"listenPort", "allowCidrs", "denyCidrs"},
	},
	"disableDatabasePublicPort": {
		Target: TargetService, Summary: "Close the public database port of {name} in production",
		Effects: []string{"Clients that connect through the public port lose access."},
	},
	"reprovisionServiceTailnet": {
		Target: TargetService, Summary: "Re-create the tailnet device of {name} in production",
		Effects: []string{"The service leaves the tailnet under its old identity and joins again; its tailnet address changes."},
	},
	"createLink": {
		Target: TargetEnvironment, Summary: "Link two services of the production environment {name}",
		Effects: []string{"When they run on different servers the link opens a mesh grant between them at once."},
		Show:    []string{"fromServiceId", "toServiceId"},
	},
	"deleteLink": {
		Target: TargetLink, Summary: "Remove a link between services of the production environment {name}",
		Effects: []string{"The link and its mesh grant are removed at once."},
	},
	"reconcileEnvironmentMesh": {
		Target: TargetEnvironment, Summary: "Reconcile the mesh of the production environment {name}",
		Effects: []string{"Missing links, exposures and grants are created and tailnet devices are added at once."},
		Show:    []string{"mode", "acceptMeshChanges"},
	},
	"reprovisionMeshLink": {
		Target: TargetMeshLink, Summary: "Re-create a mesh link node of the production environment {name}",
		Effects: []string{"The link node leaves the tailnet and joins again with a new device and tailnet IP; traffic over it is interrupted until it is back and the access rules regenerate."},
	},
	// ── servers that host production ──
	"revokeServer": {
		Target: TargetServer, Summary: "Permanently disable the agent of {name}, which hosts production services",
		Effects: []string{
			"The agent is cut off for good: no deploy, rollback or certificate work reaches the server.",
			"Production keeps running but cannot be managed until someone with SSH access enrolls the server again.",
		},
		Irreversible: true,
	},
	"disableServerTunnel": {
		Target: TargetServer, Summary: "Turn off the Cloudflare tunnel of {name}, which hosts production services",
		Effects: []string{"DNS for the hostnames behind the tunnel is pointed back at the server's address at once."},
		Show:    []string{"force"},
	},
}

// CentralGuard returns how the MCP dispatch guards an operation, if it does.
func CentralGuard(op string) (Central, bool) {
	c, ok := central[op]
	return c, ok
}

// ProdExemption is why an allowed operation on a production-scoped id needs no
// approval.
type ProdExemption string

const (
	// ExemptStaged: the change stays staged until a guarded deploy applies it.
	ExemptStaged ProdExemption = "staged"
	// ExemptCreates: creates a new resource, staged until a guarded deploy;
	// nothing that runs changes.
	ExemptCreates ProdExemption = "creates"
	// ExemptProbe: a plan, test, scan or check; changes nothing that runs or
	// is stored.
	ExemptProbe ProdExemption = "probe"
	// ExemptServiceGuard: the domain service guards the effect (a route
	// delete is a service delete).
	ExemptServiceGuard ProdExemption = "service_guard"
	// ExemptNoRuntime: a layout, label, credential for builds or a record
	// with no effect on a running service.
	ExemptNoRuntime ProdExemption = "no_runtime"
	// ExemptNonProduction: acts only on non-production environments
	// (previews).
	ExemptNonProduction ProdExemption = "non_production"
)

// prodExempt lists the allowed, mutating operations reached through a
// production-scoped id that need no approval, with the reason.
var prodExempt = map[string]ProdExemption{
	"acceptServiceSshHostKey":         ExemptNoRuntime,
	"applyTemplateUpdate":             ExemptStaged,
	"attachServiceNetwork":            ExemptStaged,
	"cancelDumpRestore":               ExemptNoRuntime,
	"checkStack":                      ExemptProbe,
	"createBackup":                    ExemptCreates,
	"createDatabase":                  ExemptCreates,
	"createDomain":                    ExemptStaged,
	"createEnvironment":               ExemptCreates,
	"createRoute":                     ExemptCreates,
	"createService":                   ExemptCreates,
	"createVolume":                    ExemptStaged,
	"createVolumeBackup":              ExemptCreates,
	"deleteDomainAuthUser":            ExemptStaged,
	"deleteEnvironmentSharedVariable": ExemptStaged,
	"deletePreview":                   ExemptNonProduction,
	"deleteProjectSharedVariable":     ExemptStaged,
	"deleteRoute":                     ExemptServiceGuard,
	"deleteServiceSshKey":             ExemptNoRuntime,
	"deleteVariable":                  ExemptStaged,
	"detachServiceNetwork":            ExemptStaged,
	"discardPendingChange":            ExemptStaged,
	"generateDomain":                  ExemptStaged,
	"installTemplate":                 ExemptCreates,
	"planComposeImport":               ExemptProbe,
	"planDatabaseUpgrade":             ExemptProbe,
	"planDeployment":                  ExemptProbe,
	"planTemplate":                    ExemptProbe,
	"previewBuildPlan":                ExemptProbe,
	"previewEnvTemplates":             ExemptProbe,
	"putCanvas":                       ExemptNoRuntime,
	"putEnvironmentSharedVariables":   ExemptStaged,
	"putProjectSharedVariables":       ExemptStaged,
	"putVariables":                    ExemptStaged,
	"refreshDatabase":                 ExemptProbe,
	"releaseServiceConfigFields":      ExemptNoRuntime,
	"restoreVariableVersion":          ExemptStaged,
	"rotateEnvironmentSharedVariable": ExemptStaged,
	"rotateProjectSharedVariable":     ExemptStaged,
	"rotateVariable":                  ExemptStaged,
	"scanServiceEnvTemplates":         ExemptProbe,
	"setDomainAuthUsers":              ExemptStaged,
	"setServiceSshKey":                ExemptNoRuntime,
	"shareEnvironmentSharedVariable":  ExemptStaged,
	"shareProjectSharedVariable":      ExemptStaged,
	"stageServiceSourceChange":        ExemptStaged,
	"testServiceSshConnection":        ExemptProbe,
	"updateDomain":                    ExemptStaged,
	"updatePreviewSettings":           ExemptNoRuntime,
	"updateProject":                   ExemptNoRuntime,
	"updateRoute":                     ExemptStaged,
	"updateService":                   ExemptStaged,
	"validateVariable":                ExemptProbe,
}

// ProdExempt returns why an operation needs no production approval, if it is
// listed as exempt.
func ProdExempt(op string) (ProdExemption, bool) {
	r, ok := prodExempt[op]
	return r, ok
}
