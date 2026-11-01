package catalog

// Why texts shared by groups of denied operations.
const (
	whySignIn           = "Sign-in, 2FA, passkey, password-reset, OIDC or bootstrap plumbing: a token never authenticates a person."
	whySession          = "Browser account management. The API refuses token principals, and an agent must not change the owner's own sign-in methods."
	whySSO              = "Single sign-on and instance sign-in settings: changing them can lock people out or hand access to a third party."
	whyCredWrite        = "Stores a third-party credential (admin scope). Credentials are entered by a person in the web UI and never pass through a model."
	whyMint             = "Creates or rotates a credential, link or token and returns its value once: an agent holding it could escalate beyond its own scopes."
	whyReveal           = "Returns a credential in plain text; secrets are write-only over MCP."
	whyInstance         = "Instance or team administration (members, roles, security policy, instance backup): reserved to people in the web UI."
	whyHostRoot         = "Gives or changes root-level access to the host, or widens what containers may do on it."
	whyStreaming        = "Server-sent events; MCP is request/response here. tail_logs returns a bounded, redacted, untrusted-wrapped tail instead."
	whyComposite        = "Covered by a composite that adds the review, production approval and result handling this raw endpoint lacks."
	whyCoolify          = "Takes a Coolify API token and reads every variable value of another instance: that credential and those secrets must be entered by a person (web UI or CLI) and never pass through a model."
	whyInstanceServices = "Instance infrastructure (built-in S3, registry and Loki, quotas and purges of every team's slice): reserved to the instance administrator in the web UI."
	whyApprovalAPI      = "A principal must never approve its own production operation: approvals are given by a person in the web UI."
	whyCombine          = "Merges and deletes services; reviewed and confirmed by a person in the web UI or CLI."
)

// denyEntries are the operations never callable over MCP. Reasons come from
// the closed set in types.go; admin-scope credential stores are filed under
// instance-admin.
var denyEntries = map[string]Deny{
	// ── auth and sign-in plumbing ──
	"login":                      {Reason: ReasonAuthPlumbing, Why: whySignIn},
	"logout":                     {Reason: ReasonSessionOnly, Why: whySession},
	"verifySecondFactor":         {Reason: ReasonAuthPlumbing, Why: whySignIn},
	"getSecondFactorMethods":     {Reason: ReasonAuthPlumbing, Why: whySignIn},
	"passkeySecondFactor":        {Reason: ReasonAuthPlumbing, Why: whySignIn},
	"passkeySecondFactorOptions": {Reason: ReasonAuthPlumbing, Why: whySignIn},
	"passkeyLogin":               {Reason: ReasonAuthPlumbing, Why: whySignIn},
	"passkeyLoginOptions":        {Reason: ReasonAuthPlumbing, Why: whySignIn},
	"startProviderLogin":         {Reason: ReasonAuthPlumbing, Why: whySignIn},
	"providerLoginCallback":      {Reason: ReasonAuthPlumbing, Why: whySignIn},
	"listLoginProviders":         {Reason: ReasonAuthPlumbing, Why: whySignIn},
	"getPasswordResetStatus":     {Reason: ReasonAuthPlumbing, Why: whySignIn},
	"requestPasswordReset":       {Reason: ReasonAuthPlumbing, Why: whySignIn},
	"checkPasswordResetToken":    {Reason: ReasonAuthPlumbing, Why: whySignIn},
	"confirmPasswordReset":       {Reason: ReasonAuthPlumbing, Why: whySignIn},
	"getSetupStatus":             {Reason: ReasonAuthPlumbing, Why: "First-run bootstrap of the instance; public, pre-authentication."},
	"setup":                      {Reason: ReasonAuthPlumbing, Why: "First-run bootstrap of the instance; public, pre-authentication."},
	"previewInvite":              {Reason: ReasonAuthPlumbing, Why: "Public invite-link preview: pre-authentication plumbing."},

	// ── SSO provider configuration and instance settings ──
	"listAuthProviders":            {Reason: ReasonInstanceAdmin, Why: whySSO},
	"createAuthProvider":           {Reason: ReasonInstanceAdmin, Why: whySSO},
	"updateAuthProvider":           {Reason: ReasonInstanceAdmin, Why: whySSO},
	"deleteAuthProvider":           {Reason: ReasonInstanceAdmin, Why: whySSO},
	"getAuthSettings":              {Reason: ReasonInstanceAdmin, Why: whySSO},
	"updateAuthSettings":           {Reason: ReasonInstanceAdmin, Why: whySSO},
	"testAuthProvider":             {Reason: ReasonInstanceAdmin, Why: whySSO},
	"updateInstance":               {Reason: ReasonInstanceAdmin, Why: whyInstance},
	"getInstanceBackup":            {Reason: ReasonInstanceAdmin, Why: whyInstance},
	"updateInstanceBackup":         {Reason: ReasonInstanceAdmin, Why: "Control-plane backup settings and its destination credentials."},
	"acknowledgeInstanceBackupKey": {Reason: ReasonInstanceAdmin, Why: whyInstance},
	"listInstanceBackupRuns":       {Reason: ReasonInstanceAdmin, Why: whyInstance},
	"runInstanceBackup":            {Reason: ReasonInstanceAdmin, Why: whyInstance},
	"testInstanceBackup":           {Reason: ReasonInstanceAdmin, Why: whyInstance},
	"getDashboardAccess":           {Reason: ReasonInstanceAdmin, Why: "Dashboard access settings: who may reach the control plane and how its certificate is issued."},
	"updateDashboardAccess":        {Reason: ReasonInstanceAdmin, Why: "Dashboard IP allow list and certificate method: changing them can lock every person, agent and webhook out of the control plane."},
	"startSystemUpdate":            {Reason: ReasonHostRoot, Why: "Updates the control plane itself: runs the installer as root on the control plane's host and restarts the platform. An agent must never update the platform; the instance admin does it in the web UI."},

	// ── own account (session only) ──
	"updateMe":                  {Reason: ReasonSessionOnly, Why: whySession},
	"changePassword":            {Reason: ReasonSessionOnly, Why: whySession},
	"getMySecurity":             {Reason: ReasonSessionOnly, Why: whySession},
	"startTotpEnrolment":        {Reason: ReasonSessionOnly, Why: whySession},
	"enableTotp":                {Reason: ReasonSessionOnly, Why: whySession},
	"disableTotp":               {Reason: ReasonSessionOnly, Why: whySession},
	"regenerateRecoveryCodes":   {Reason: ReasonSessionOnly, Why: whySession},
	"listPasskeys":              {Reason: ReasonSessionOnly, Why: whySession},
	"startPasskeyRegistration":  {Reason: ReasonSessionOnly, Why: whySession},
	"finishPasskeyRegistration": {Reason: ReasonSessionOnly, Why: whySession},
	"renamePasskey":             {Reason: ReasonSessionOnly, Why: whySession},
	"deletePasskey":             {Reason: ReasonSessionOnly, Why: whySession},
	"unlinkIdentity":            {Reason: ReasonSessionOnly, Why: whySession},
	"startIdentityLink":         {Reason: ReasonSessionOnly, Why: whySession},
	"listTeams":                 {Reason: ReasonSessionOnly, Why: "Lists the teams of a signed-in user; a token belongs to exactly one team (see whoami)."},
	"switchTeam":                {Reason: ReasonSessionOnly, Why: "Changes the browser session's current team; a token belongs to one team."},
	"acceptInvite":              {Reason: ReasonSessionOnly, Why: "Joins a team as the signed-in person; never as an agent."},

	// ── tokens, enrollment, invites, reset links ──
	"createToken":                     {Reason: ReasonMintsCredential, Why: "A token that can create tokens is privilege escalation."},
	"revokeToken":                     {Reason: ReasonAuthPlumbing, Why: "API token lifecycle is managed by people in the web UI."},
	"setMcpEnabled":                   {Reason: ReasonAuthPlumbing, Why: "Turning the MCP endpoint on or off is the instance admin's decision in the web UI, never an agent's."},
	"createEnrollmentToken":           {Reason: ReasonMintsCredential, Why: "Returns a one-time token that enrolls a new host agent."},
	"reissueEnrollmentToken":          {Reason: ReasonMintsCredential, Why: "Returns a one-time token that enrolls a new host agent."},
	"createTeamInvite":                {Reason: ReasonMintsCredential, Why: whyMint},
	"createMemberPasswordResetLink":   {Reason: ReasonMintsCredential, Why: "Mints a one-time sign-in link for a member."},
	"rotateDeployHook":                {Reason: ReasonMintsCredential, Why: "Returns a new deploy-hook URL that carries a secret."},
	"setDeployHook":                   {Reason: ReasonMintsCredential, Why: "Turning the hook on returns its secret URL once."},
	"rotateNotificationChannelSecret": {Reason: ReasonMintsCredential, Why: "Returns a new webhook signing secret once."},
	"rotateGitWebhookSecret":          {Reason: ReasonMintsCredential, Why: "Returns a new git webhook secret once."},
	"startGitHubAppManifest":          {Reason: ReasonMintsCredential, Why: "Creates a GitHub App whose private key and secrets are issued to this instance."},
	"testTailscaleIntegration":        {Reason: ReasonMintsCredential, Why: "Mints and revokes an auth key on the tailnet to test the connection."},
	"startGitConnectionOAuth":         {Reason: ReasonAuthPlumbing, Why: "Starts an OAuth consent flow that only a person can complete."},

	// ── reveals ──
	"revealDatabaseExport":           {Reason: ReasonRevealsSecret, Why: whyReveal},
	"revealDatabasePublicConnection": {Reason: ReasonRevealsSecret, Why: whyReveal},
	"revealVolumeBackupRecoveryKey":  {Reason: ReasonRevealsSecret, Why: "Returns the backup repository password."},

	// ── third-party credential writes (admin scope) ──
	"putCloudflareIntegration":    {Reason: ReasonInstanceAdmin, Why: whyCredWrite},
	"deleteCloudflareIntegration": {Reason: ReasonInstanceAdmin, Why: "Disconnects Cloudflare and forgets its token; every domain's DNS management stops."},
	"putTailscaleIntegration":     {Reason: ReasonInstanceAdmin, Why: whyCredWrite},
	"deleteTailscaleIntegration":  {Reason: ReasonInstanceAdmin, Why: "Disconnects Tailscale and forgets its credentials."},
	"createDnsCredential":         {Reason: ReasonInstanceAdmin, Why: whyCredWrite},
	"updateDnsCredential":         {Reason: ReasonInstanceAdmin, Why: whyCredWrite},
	"createRegistry":              {Reason: ReasonInstanceAdmin, Why: whyCredWrite},
	"updateRegistry":              {Reason: ReasonInstanceAdmin, Why: whyCredWrite},
	"createGitConnection":         {Reason: ReasonInstanceAdmin, Why: whyCredWrite},
	"updateGitConnection":         {Reason: ReasonInstanceAdmin, Why: whyCredWrite},
	"createSecretManager":         {Reason: ReasonInstanceAdmin, Why: whyCredWrite},
	"updateSecretManager":         {Reason: ReasonInstanceAdmin, Why: whyCredWrite},
	"createBackupDestination":     {Reason: ReasonInstanceAdmin, Why: whyCredWrite},
	"updateBackupDestination":     {Reason: ReasonInstanceAdmin, Why: whyCredWrite},
	"createLogDrain":              {Reason: ReasonInstanceAdmin, Why: whyCredWrite},
	"updateLogDrain":              {Reason: ReasonInstanceAdmin, Why: whyCredWrite},
	"createNotificationChannel":   {Reason: ReasonInstanceAdmin, Why: whyCredWrite},
	"updateNotificationChannel":   {Reason: ReasonInstanceAdmin, Why: whyCredWrite},
	"updateServiceAlertSettings":  {Reason: ReasonInstanceAdmin, Why: "Changes the instance-wide thresholds of the service health and missed-backup alerts."},
	"updateUptimeMonitor":         {Reason: ReasonInstanceAdmin, Why: "Switches uptime probing of a service on or off and sets when its outages alert (admin scope): workspace administration, reserved to people."},
	"updateInstanceUpgrade":       {Reason: ReasonInstanceAdmin, Why: "Switches the automatic upgrade of every agent of the instance."},
	"setMaxMindCredentials":       {Reason: ReasonInstanceAdmin, Why: whyCredWrite},
	"uploadCertificate":           {Reason: ReasonInstanceAdmin, Why: "Uploads a TLS private key."},
	"planCoolifyImport":           {Reason: ReasonInstanceAdmin, Why: whyCoolify},
	"importCoolify":               {Reason: ReasonInstanceAdmin, Why: whyCoolify},
	"createPlatformService":       {Reason: ReasonInstanceAdmin, Why: "Sets up the tailnet control server and wires it into the Tailscale integration."},
	"deletePlatformService":       {Reason: ReasonInstanceAdmin, Why: "Removes a platform service that instance settings depend on."},
	"reconnectPlatformService":    {Reason: ReasonInstanceAdmin, Why: "Re-wires a platform service into instance settings."},
	"retryPlatformService":        {Reason: ReasonInstanceAdmin, Why: "Re-runs a platform service's instance-level setup."},
	"getInstanceServices":         {Reason: ReasonInstanceAdmin, Why: whyInstanceServices},
	"updateInstanceServices":      {Reason: ReasonInstanceAdmin, Why: whyInstanceServices},
	"checkInstanceServices":       {Reason: ReasonInstanceAdmin, Why: whyInstanceServices},
	"updateTeamSliceQuota":        {Reason: ReasonInstanceAdmin, Why: whyInstanceServices},
	"purgeTeamSlice":              {Reason: ReasonInstanceAdmin, Why: "Irreversibly deletes a deleted team's backups, images and logs from the built-in services."},

	// ── team administration ──
	"createTeam":          {Reason: ReasonInstanceAdmin, Why: whyInstance},
	"deleteTeam":          {Reason: ReasonHostRoot, Why: "Irreversibly deletes a team with every project, volume and server: the agents empty their hosts as root and the DNS records and tailnet devices are removed."},
	"setTeamSecurity":     {Reason: ReasonInstanceAdmin, Why: "Changes the team's two-factor requirement."},
	"updateTeamMember":    {Reason: ReasonInstanceAdmin, Why: "Role changes decide who may do what; reserved to people."},
	"removeTeamMember":    {Reason: ReasonInstanceAdmin, Why: "Member removal is reserved to people."},
	"setTerminalSettings": {Reason: ReasonInstanceAdmin, Why: "Controls whether production file writes through the file browser are allowed."},

	// ── host root ──
	"setServerUnrestricted":   {Reason: ReasonHostRoot, Why: "The unrestricted switch lets compose stacks and Docker options use privileged, host-level capabilities on the server."},
	"setServerHostShell":      {Reason: ReasonHostRoot, Why: "Turns a root shell on the host on or off."},
	"applyServerHardening":    {Reason: ReasonHostRoot, Why: "Changes the host's configuration as root."},
	"rollbackServerHardening": {Reason: ReasonHostRoot, Why: "Reverts host hardening as root."},
	"applyServerPatches":      {Reason: ReasonHostRoot, Why: "Upgrades the host's packages as root; Docker and system services restart."},
	"rebootServer":            {Reason: ReasonHostRoot, Why: "Reboots the host; every service on it goes down."},
	"removeServer":            {Reason: ReasonHostRoot, Why: "Has the agent (root) remove every NebulaCtrl container, network and volume on the host and then itself, and deletes the server: irreversible."},
	"upgradeServerAgent":      {Reason: ReasonHostRoot, Why: "Replaces the agent binary on the host (the agent runs as root) and restarts the agent."},
	"upgradeServerAgents":     {Reason: ReasonHostRoot, Why: "Replaces the agent binary on every selected host (the agents run as root) and restarts them."},

	// ── streaming and non-JSON ──
	"getServiceLogs":            {Reason: ReasonStreaming, Why: whyStreaming},
	"getEnvironmentLogs":        {Reason: ReasonStreaming, Why: whyStreaming},
	"getDeploymentLogs":         {Reason: ReasonStreaming, Why: whyStreaming},
	"getTaskRunLogs":            {Reason: ReasonStreaming, Why: "Server-sent events when following; get_task_run returns the bounded, redacted output."},
	"streamEvents":              {Reason: ReasonStreaming, Why: "Live invalidation feed for the web UI."},
	"previewServiceMaintenance": {Reason: ReasonBinary, Why: "Returns an HTML page, not JSON."},
	"downloadServiceLogs":       {Reason: ReasonBinary, Why: "Returns a log file, not JSON; tail_logs reads the same history redacted and wrapped as untrusted text."},
	"downloadDeploymentLogs":    {Reason: ReasonBinary, Why: "Returns a log file, not JSON; deployment_get reports the status and the failing step."},
	"createDumpRestore":         {Reason: ReasonBinary, Why: "Restoring a dump file needs the file itself as a binary upload (PUT /dump-restores/{id}/upload), which MCP tools cannot send; use the web app or the CLI (nebulactrl databases restore --file)."},

	// ── superseded by composites ──
	"getServiceLogHistory":     {Reason: ReasonSuperseded, Why: "tail_logs reads history with since, grep and cursor, redacted and wrapped as untrusted text."},
	"getServiceCentralLogs":    {Reason: ReasonSuperseded, Why: "tail_logs reads logs redacted and wrapped as untrusted text; central log lines are not returned raw."},
	"getEnvironmentLogHistory": {Reason: ReasonSuperseded, Why: "tail_logs reads the merged environment log, redacted and wrapped as untrusted text."},
	"deployService":            {Reason: ReasonSuperseded, Why: "apply_changes deploys with a reviewed plan, expectedGeneration and the production approval."},
	"deployStaged":             {Reason: ReasonSuperseded, Why: "apply_changes deploys staged changes per service or per environment with the production approval."},
	"rollbackDeployment":       {Reason: ReasonSuperseded, Why: "rollback_service previews what rolls back, handles image_pruned and always asks for approval."},

	// ── approvals ──
	"getApproval":     {Reason: ReasonSessionOnly, Why: whyApprovalAPI},
	"approveApproval": {Reason: ReasonSessionOnly, Why: whyApprovalAPI},
	"declineApproval": {Reason: ReasonSessionOnly, Why: whyApprovalAPI},

	// ── merging services (deletes the originals) ──
	"combineServices": {Reason: ReasonPersonOnly, Why: whyCombine},
	"abortCombine":    {Reason: ReasonPersonOnly, Why: whyCombine},
	"retryCombine":    {Reason: ReasonPersonOnly, Why: whyCombine},
}
