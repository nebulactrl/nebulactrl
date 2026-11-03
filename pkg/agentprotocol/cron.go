package agentprotocol

// CapCronService is advertised in Facts.Capabilities by agents that deploy a
// cron service without starting it (DeployPayload.NoStart) and that run a
// service's own image command for a scheduled run (RunTaskPayload.ServiceRun).
// The control plane refuses to deploy or run a cron service on a server whose
// facts lack it (409 agent_too_old); a server that has never connected has no
// facts and is assumed to enroll with a current agent (same rule as
// CapProcesses).
const CapCronService = "service.cron"

// CronDeployMessage is the result message of a no-start (cron) deploy.
const CronDeployMessage = "Image ready; runs on schedule"
