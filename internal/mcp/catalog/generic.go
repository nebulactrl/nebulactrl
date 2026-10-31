package catalog

// guarded marks a generic entry as enforced by the production guard: in its
// domain service (internal/prodguard) or, for the operations listed in
// central (prod.go), by the MCP dispatch.
func guarded(g Generic) Generic { g.ProdGuard = true; return g }

// Constructors keep the table below one line per operation. The name says
// the hint preset: rd read, rdO read that reaches outside, st idempotent edit,
// cr creation, dl deletion, ac action (runtime effect), pr probe of an
// external system that records its outcome.
func rd(ts Toolset) Generic  { return Generic{Toolset: ts, Hints: hRead} }
func rdO(ts Toolset) Generic { return Generic{Toolset: ts, Hints: hReadOpen} }
func st(ts Toolset) Generic  { return Generic{Toolset: ts, Hints: hStage} }
func cr(ts Toolset) Generic  { return Generic{Toolset: ts, Hints: hCreate} }
func crO(ts Toolset) Generic {
	return Generic{Toolset: ts, Hints: Hints{OpenWorld: true}}
}
func dl(ts Toolset) Generic { return Generic{Toolset: ts, Hints: hDelete} }
func pr(ts Toolset) Generic { return Generic{Toolset: ts, Hints: hProbe} }
func ac(ts Toolset) Generic { return Generic{Toolset: ts, Hints: hAct} }
func ai(ts Toolset) Generic { return Generic{Toolset: ts, Hints: hActIdem} }
func ad(ts Toolset) Generic { return Generic{Toolset: ts, Hints: hActDestroy} }

const (
	tCore    = ToolsetCore
	tConfig  = ToolsetConfig
	tData    = ToolsetData
	tServers = ToolsetServers
	tObserve = ToolsetObserve
)

// genericEntries are allowed operations without a curated tool. They are
// found with find_operation and run with call_operation, behind the scopes the
// operation declares and the production guard in the domain service.
var genericEntries = map[string]Generic{
	// ── analytics, deployments (read) ──
	"getServerAnalytics":             rd(tObserve),
	"getTrafficAnalytics":            rd(tObserve),
	"getDeploymentRelease":           rd(tObserve),
	"getEnvironmentDeploymentsDaily": rd(tObserve),
	"listDeploymentCommands":         rd(tObserve),
	"listDeploymentEvents":           rd(tObserve),
	"listEnvironmentDeployments":     rd(tObserve),
	"planDeployment":                 rd(tObserve),
	"previewBuildPlan":               rdO(tConfig),

	// ── backups ──
	"deleteBackup":            guarded(dl(tData)),
	"deleteBackupDestination": dl(tData),
	"deleteBackupSchedule":    guarded(dl(tData)),
	"getBackup":               rd(tData),
	"getBackupRestore":        rd(tData),
	"listBackupRestores":      rd(tData),
	"cancelDumpRestore":       ac(tData),
	"getDumpRestore":          rd(tData),
	"listDumpRestores":        rd(tData),
	"lockBackup":              guarded(st(tData)),
	"testBackupDestination":   pr(tData),
	"getTeamSlice":            rd(tData),
	"updateBackupSchedule":    guarded(st(tData)),

	// ── volume backups ──
	"deleteRetiredVolume":      guarded(dl(tData)),
	"deleteVolumeBackup":       guarded(dl(tData)),
	"deleteVolumeBackupConfig": guarded(dl(tData)),
	"getVolumeBackup":          rd(tData),
	"getVolumeBackupConfig":    rd(tData),
	"listRetiredVolumes":       rd(tData),
	"listVolumeBackupFiles":    rd(tData),
	"listVolumeRestores":       rd(tData),
	"lockVolumeBackup":         guarded(st(tData)),
	"putVolumeBackupConfig":    guarded(st(tData)),
	"restoreVolumeBackup": {
		Toolset: tData, Hints: hActDestroy, ProdGuard: true,
		Confirm: &Confirm{Field: "confirmSlug", Echo: "the service slug", When: "mode=in_place"},
	},

	// ── databases ──
	"disableDatabasePublicPort": {Toolset: tData, Hints: Hints{Idempotent: true}, ProdGuard: true},
	"getDatabasePublicPort":     rd(tData),
	"listDatabaseEngines":       rd(tData),
	"planDatabaseUpgrade":       rd(tData),
	"refreshDatabase":           ai(tData),
	"rotateDatabaseCredentials": guarded(ad(tData)),
	"setDatabasePublicPort":     guarded(st(tData)),
	"upgradeDatabase": {
		Toolset: tData, Hints: hActDestroy, ProdGuard: true,
		Confirm: &Confirm{Field: "confirm", Echo: "the service slug"},
	},

	// ── canvas, previews, links, routes, builds ──
	"getProjectDeletionPlan": rd(tConfig),
	"getCanvas":              rd(tConfig),
	"putCanvas":              st(tConfig),
	"deletePreview":          dl(tConfig),
	"getPreviewSettings":     rd(tConfig),
	"updatePreviewSettings":  st(tConfig),
	"listLinks":              rd(tConfig),
	"previewLink":            rd(tConfig),
	"createRoute":            cr(tConfig),
	"deleteRoute":            dl(tConfig),
	"getRoute":               rd(tConfig),
	"updateRoute":            st(tConfig),
	"getTeamBuildSettings":   rd(tConfig),
	"putTeamBuildSettings":   st(tConfig),
	"checkStack":             rd(tConfig),
	"listVolumes":            rd(tConfig),

	// ── domains ──
	"deleteDomainAuthUser": dl(tConfig),
	"fixDomainDns":         {Toolset: tConfig, Hints: Hints{Destructive: true, Idempotent: true, OpenWorld: true}, ProdGuard: true}, // adopt:true replaces records NebulaCtrl did not create
	"listDomains":          rd(tConfig),
	"setDomainAuthUsers":   st(tConfig),
	"updateDomain":         st(tConfig),
	"updateDomainDns":      guarded(st(tConfig)),

	// ── services ──
	"getDeployHook":             rd(tConfig),
	"getDockerOptionsPolicy":    rd(tConfig),
	"getServiceGit":             rd(tConfig),
	"linkServiceGit":            guarded(st(tConfig)),
	"stageServiceSourceChange":  st(tConfig),
	"unlinkServiceGit":          guarded(dl(tConfig)),
	"listPendingChanges":        rd(tConfig),
	"reprovisionServiceTailnet": {Toolset: tConfig, Hints: Hints{Destructive: true, OpenWorld: true}, ProdGuard: true},
	"setServiceMaintenance":     guarded(ai(tConfig)),
	"setServiceScaling":         guarded(ai(tConfig)),
	"setProcessScaling":         guarded(ai(tConfig)),
	"planCombineServices":       rd(tConfig),
	"getCombine":                rd(tConfig),
	"createServiceTask":         guarded(cr(tConfig)),
	"deleteServiceTask":         guarded(dl(tConfig)),
	"listServiceTaskRuns":       rd(tConfig),
	"previewTaskSchedule":       rd(tConfig),
	"updateServiceTask":         guarded(st(tConfig)),

	// ── variables and shared variables ──
	"getVariableHistory":                  rd(tConfig),
	"getVariableUsage":                    rd(tConfig),
	"listSuggestedVariables":              rdO(tConfig),
	"previewEnvTemplates":                 rdO(tConfig),
	"restoreVariableVersion":              st(tConfig),
	"rotateVariable":                      cr(tConfig),
	"scanServiceEnvTemplates":             {Toolset: tConfig, Hints: hProbe},
	"validateVariable":                    rd(tConfig),
	"deleteEnvironmentSharedVariable":     dl(tConfig),
	"deleteProjectSharedVariable":         dl(tConfig),
	"deleteTeamSharedVariable":            dl(tConfig),
	"getEnvironmentSharedVariableHistory": rd(tConfig),
	"getProjectSharedVariableHistory":     rd(tConfig),
	"getTeamSharedVariableHistory":        rd(tConfig),
	"listEnvironmentSharedVariables":      rd(tConfig),
	"listProjectSharedVariables":          rd(tConfig),
	"listTeamSharedVariables":             rd(tConfig),
	"putEnvironmentSharedVariables":       st(tConfig),
	"putProjectSharedVariables":           st(tConfig),
	"putTeamSharedVariables":              st(tConfig),
	"rotateEnvironmentSharedVariable":     cr(tConfig),
	"rotateProjectSharedVariable":         cr(tConfig),
	"rotateTeamSharedVariable":            cr(tConfig),
	"shareEnvironmentSharedVariable":      st(tConfig),
	"shareProjectSharedVariable":          st(tConfig),
	"shareTeamSharedVariable":             st(tConfig),

	// ── templates ──
	"applyTemplateUpdate": st(tConfig),
	"deleteTemplateInstance": {
		Toolset: tConfig, Hints: hDelete, ProdGuard: true,
		Confirm: &Confirm{Field: "confirm", Echo: "the template id"},
	},
	"getCatalogTemplate":    rd(tConfig),
	"getTemplateUpdate":     rd(tConfig),
	"listTemplateInstances": rd(tConfig),

	// ── git, registries, secret managers, terminal reads ──
	"acceptServiceSshHostKey":   st(tConfig),
	"deleteGitConnection":       dl(tConfig),
	"deleteServiceSshKey":       dl(tConfig),
	"getGitConnection":          rd(tConfig),
	"getServiceSshKey":          rd(tConfig),
	"listGitBranches":           rdO(tConfig),
	"listGitConnections":        rd(tConfig),
	"listGitDeliveries":         rd(tConfig),
	"listGitRepos":              rdO(tConfig),
	"refreshGitHubInstallation": pr(tConfig),
	"setServiceSshKey":          cr(tConfig),
	"testGitConnection":         pr(tConfig),
	"testServiceSshConnection":  pr(tConfig),
	"checkRegistry":             pr(tConfig),
	"deleteRegistry":            dl(tConfig),
	"getRegistry":               rd(tConfig),
	"listRegistries":            rd(tConfig),
	"listRegistryImages":        rdO(tConfig),
	"deleteSecretManager":       dl(tConfig),
	"listSecretManagers":        rd(tConfig),
	"testSecretManager":         pr(tConfig),
	"getServiceTerminal":        rd(tConfig),
	"getTerminalSettings":       rd(tConfig),
	"listServiceFiles":          rd(tConfig),

	// ── teams (reads and light edits) ──
	"deleteTeamInvite":    dl(tConfig),
	"getInstance":         rd(tConfig),
	"getTeam":             rd(tConfig),
	"getTeamDeletionPlan": rd(tConfig),
	"getTeamSecurity":     rd(tConfig),
	"listTeamInvites":     rd(tConfig),
	"listTeamMembers":     rd(tConfig),
	"updateTeam":          st(tConfig),

	// ── core ──
	"getSystem":         rd(tCore),
	"getSystemUpdate":   rd(tCore),
	"checkSystemUpdate": pr(tCore), // reads the public release feed and records the outcome
	"listTokens":        rd(tCore),

	// ── servers: proxy, certificates, DNS-01, tunnel, networks, drain ──
	"deleteCertificate":         dl(tServers),
	"listCertificates":          rd(tServers),
	"createServerWildcardCert":  cr(tServers),
	"deleteDnsCredential":       dl(tServers),
	"deleteServerDns01Resolver": dl(tServers),
	"deleteWildcardCert":        dl(tServers),
	"getServerDns01":            rd(tServers),
	"listDnsCredentials":        rd(tServers),
	"listDnsProviders":          rd(tServers),
	"listLegoDnsProviders":      rd(tServers),
	"setServerDns01Resolver":    st(tServers),
	"updateWildcardCert":        st(tServers),
	"cancelServerDrain":         ai(tServers),
	"getServerDrain":            rd(tServers),
	"undrainServer":             ai(tServers),
	"deployServerProxy":         ac(tServers),
	"getServerProxy":            rd(tServers),
	"listServerProxyRoutes":     rd(tServers),
	"planServerProxy":           rd(tServers),
	"reprovisionServerTailnet":  {Toolset: tServers, Hints: Hints{Destructive: true, OpenWorld: true}},
	"updateServerProxySettings": st(tServers),
	"attachServiceNetwork":      st(tServers),
	"createNetwork":             cr(tServers),
	"deleteNetwork":             dl(tServers),
	"detachServiceNetwork":      {Toolset: tServers, Hints: Hints{Idempotent: true}},
	"getNetwork":                rd(tServers),
	"listNetworks":              rd(tServers),
	"getEnvironmentMesh":        rd(tServers),
	"getServiceMesh":            rd(tServers),
	"reconcileEnvironmentMesh":  guarded(ai(tServers)),
	"listServerPorts":           rd(tServers),
	"getServerLogSettings":      rd(tServers),
	"updateServerLogSettings":   st(tServers),
	"getCloudflareIPs":          rd(tServers),
	"getCloudflareIntegration":  rd(tServers),
	"getTailscaleIntegration":   rd(tServers),
	"listCloudflareZones":       rdO(tServers),
	"listTailscaleDevices":      rdO(tServers),
	"refreshCloudflareIPs":      pr(tServers),
	"testCloudflareIntegration": pr(tServers),
	"getPlatformService":        rd(tServers),
	"listPlatformServices":      rd(tServers),

	// ── servers: lifecycle and settings ──
	"deleteMaxMindCredentials": dl(tServers),
	"disableServerTunnel":      guarded(dl(tServers)),
	"enableServerTunnel":       crO(tServers),
	"getMaxMindCredentials":    rd(tServers),
	"getServerCleanup":         rd(tServers),
	"getServerHardening":       rd(tServers),
	"getServerTunnel":          rd(tServers),
	"getServerUnrestricted":    rd(tServers),
	"getInstanceUpgrade":       rd(tServers),
	"getServerHostShell":       rd(tServers),
	"pruneServerVolumes": {
		Toolset: tServers, Hints: hActDestroy,
		Confirm: &Confirm{Field: "confirm", Echo: "the server name"},
	},
	"resyncServerTunnel":      {Toolset: tServers, Hints: hProbe},
	"revokeServer":            {Toolset: tServers, Hints: hDelete, ProdGuard: true},
	"runServerCleanup":        ad(tServers),
	"runServerHardeningAudit": crO(tServers),
	"checkServerPatches":      crO(tServers),
	"getServerPatches":        rd(tServers),
	"getServerPatchRun":       rd(tServers),
	"planServerPatches":       rd(tServers),
	"updateServer":            st(tServers),
	"updateServerCleanup":     st(tServers),
	"updateServerHardening":   st(tServers),

	// ── observe: notifications and log drains (reads, tests, deletes) ──
	"deleteNotificationChannel":  dl(tObserve),
	"getNotificationChannel":     rd(tObserve),
	"listNotificationDeliveries": rd(tObserve),
	"listNotificationEventTypes": rd(tObserve),
	"getServiceAlertSettings":    rd(tObserve),
	"listServiceIncidents":       rd(tObserve),
	"getServiceUptime":           rd(tObserve),
	"getEnvironmentUptime":       rd(tObserve),
	"getUptimeMonitor":           rd(tObserve),
	"testNotificationChannel":    pr(tObserve),
	"deleteLogDrain":             dl(tObserve),
	"getLogDrain":                rd(tObserve),
	"listLogDrains":              rd(tObserve),
	"testLogDrain":               pr(tObserve),

	// ── clone and move ──
	// Clones copy secrets (production sources are guarded, see central); a
	// move out of or into production needs approval.
	"cloneService":     guarded(cr(tConfig)),
	"cloneEnvironment": guarded(cr(tConfig)),
	"moveService":      guarded(ai(tConfig)),
}
