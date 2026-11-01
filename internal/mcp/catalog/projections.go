package catalog

// Projections of the large REST views the curated tools return. Concise
// results keep what a model needs to decide the next step; detail=full returns
// the whole response (still subject to the 64 KB cap and the registry's
// redaction).

var serviceConcise = []string{
	"id", "name", "slug", "kind", "sourceType", "projectId", "environmentId", "serverId", "server",
	"generation", "appliedGeneration", "hasStagedChanges", "pendingChangeCounts", "stagedKind",
	"desiredState", "stale", "privateHost",
	"domains.hostname", "domains.status", "domains.dnsOk", "domains.targetPort", "domains.kind",
	"volumes", "scaling.replicas", "scaling.autoscaling",
	"observed.state", "observed.health", "observed.replicasReady", "observed.replicasDesired",
	"observed.error", "observed.startedAt", "observed.processes",
	"processes.name", "processes.default", "processes.port", "processes.command", "processes.singleton",
	"processes.scaling.replicas", "processes.scaling.ready", "processes.observed.state", "processes.observed.health",
	"processes.pending", "processes.removed",
	"latestDeployment", "currentDeploymentId", "currentRelease.id", "currentRelease.imageRef",
	"maintenance.enabled", "git.provider", "git.repo", "git.autoDeploy",
	"config.image", "config.gitUrl", "config.gitBranch", "config.port", "config.buildType",
	"config.startCommand", "config.memoryMb", "config.cpus", "config.healthcheck",
	"config.stopGraceSeconds", "config.imageRetention", "config.processName", "config.singleton", "config.processes",
}

var deploymentConcise = []string{
	"id", "serviceId", "serverId", "releaseId", "status", "statusMessage", "message", "strategy",
	"trigger", "actorType", "actorLabel", "generation", "rolledBackFrom", "waitingFor",
	"createdAt", "startedAt", "finishedAt", "cancelRequestedAt", "canceledAtStep",
	"commit.sha", "commit.branch", "commit.message", "release.imageRef", "build.method", "build.reason",
}

var serverConcise = []string{
	"id", "name", "status", "publicIp", "baseDomain", "agentVersion", "lastSeenAt", "serviceCount",
	"acceptWorkloads", "drainState", "drainMessage", "proxy", "latest", "unrestricted", "buildEnabled",
	"diskAlert", "facts.os", "facts.arch", "facts.cpus", "facts.memTotal", "facts.diskTotal",
	"facts.dockerVersion", "facts.port80Free", "facts.port443Free",
}

var (
	// projService serves service_list and service_get.
	projService = &Projection{ItemsPath: "items", Concise: serviceConcise}
	// projServiceOne is projService for a single object (no page wrapper).
	projServiceOne = &Projection{Concise: serviceConcise}

	projDeploymentList = &Projection{ItemsPath: "items", Concise: deploymentConcise}
	projDeploymentOne  = &Projection{Concise: deploymentConcise}

	projServerList = &Projection{ItemsPath: "items", Concise: serverConcise}
	projServerOne  = &Projection{Concise: serverConcise}

	projAudit = &Projection{
		ItemsPath: "items",
		Concise: []string{
			"id", "action", "actorType", "actorLabel", "resourceType", "resourceId", "projectId",
			"createdAt", "metadata",
		},
	}

	projDatabase = &Projection{
		Concise: []string{
			"serviceId", "deployed", "image", "spec", "credentialKeys", "exports", "info", "rotation",
			"upgrade", "replication", "engine.id", "engine.label", "engine.port", "engine.license",
		},
	}

	projNotificationChannels = &Projection{
		ItemsPath: "items",
		Concise:   []string{"id", "name", "kind", "enabled", "rule", "stats", "test", "updatedAt"},
	}

	projServiceMetrics = &Projection{Hook: func(d Detail, v any) any { return downsamplePoints(v, d) }}
	projServerMetrics  = &Projection{Hook: func(d Detail, v any) any { return downsamplePoints(v, d) }}

	// projVariables lists keys, isSecret and set. Non-secret values only with
	// detail=full, and a secret's value is scrubbed whatever the REST layer
	// returned.
	projVariables = &Projection{ItemsPath: "items", Hook: variableView}
)

func variableView(d Detail, v any) any {
	v = scrubSecretValues(v)
	if d == DetailFull {
		return v
	}
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := map[string]any{}
	if items, ok := m["items"].([]any); ok {
		kept := make([]any, 0, len(items))
		for _, it := range items {
			vm, ok := it.(map[string]any)
			if !ok {
				continue
			}
			row := map[string]any{}
			for _, k := range []string{
				"key", "isSecret", "set", "scope", "source", "computedSecret", "problem",
				"rotationDue", "references", "version", "updatedAt",
			} {
				if val, ok := vm[k]; ok {
					row[k] = val
				}
			}
			kept = append(kept, row)
		}
		out["items"] = kept
	}
	if pl, ok := m["platform"].([]any); ok {
		keys := make([]any, 0, len(pl))
		for _, p := range pl {
			if pm, ok := p.(map[string]any); ok {
				if k, ok := pm["key"]; ok {
					keys = append(keys, k)
				}
			}
		}
		out["platformKeys"] = keys
	}
	for _, k := range []string{"warnings", "restaged"} {
		if val, ok := m[k]; ok {
			out[k] = val
		}
	}
	return out
}

// downsamplePoints keeps at most 60 (concise) or 240 (full) points of a
// metrics response, always including the newest, by even striding.
func downsamplePoints(v any, d Detail) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	pts, ok := m["points"].([]any)
	if !ok {
		return v
	}
	limit := 60
	if d == DetailFull {
		limit = 240
	}
	if len(pts) <= limit {
		return v
	}
	out := make([]any, 0, limit)
	for i := range limit {
		out = append(out, pts[i*(len(pts)-1)/(limit-1)])
	}
	m["points"] = out
	m["downsampledFrom"] = len(pts)
	return m
}
