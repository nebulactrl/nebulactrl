import type { ServiceMetricPoint } from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import type { StatusKind } from "#/components/ui/status-icon";
import { isInFlight } from "#/features/deployments/labels";
import { processResources } from "#/features/metrics/model";
import { serviceProcesses } from "#/features/processes/model";

export interface HealthVerdict {
	kind: StatusKind;
	/** One word: Healthy, Degraded, Unhealthy, Starting, Stopped, … */
	label: string;
	/** What the verdict rests on, in a few words. */
	detail: string;
}

/** How the platform decides this service is healthy, from its health check. */
export function probeDescription(service: Service): string {
	const check = service.config.healthcheck;
	const port = check.port || service.config.port;
	switch (check.type) {
		case "http":
			return `HTTP GET ${check.path || "/"}${port ? ` on :${port}` : ""} · every ${check.intervalSeconds}s`;
		case "tcp":
			return `TCP connect${port ? ` :${port}` : ""} · every ${check.intervalSeconds}s`;
		case "docker":
			return "The image's own HEALTHCHECK";
		default:
			return "No probe · healthy after 10s without a restart";
	}
}

/**
 * Replicas up and wanted, from what the agent observed. Before the first
 * report: the setting, which for a service with processes is the sum over its
 * running (not pending) processes.
 */
export function replicaCounts(service: Service): {
	ready: number;
	desired: number;
} {
	const observed = service.observed;
	if (observed && observed.replicasDesired > 0) {
		return { ready: observed.replicasReady, desired: observed.replicasDesired };
	}
	const processes = serviceProcesses(service);
	if (processes.length > 0) {
		const running = processes.filter((process) => !process.pending);
		return {
			ready: running.reduce((sum, process) => sum + process.scaling.ready, 0),
			desired: running.reduce(
				(sum, process) => sum + process.scaling.replicas,
				0,
			),
		};
	}
	return { ready: service.scaling.ready, desired: service.scaling.replicas };
}

/**
 * The configured limits behind the Resources figures. The total of several
 * processes has no single limit (they may differ), so none is assumed.
 */
export function configuredLimits(service: Service): {
	memoryMb: number | undefined;
	cpus: number | undefined;
} {
	return serviceProcesses(service).length > 0
		? processResources(service.config, undefined)
		: { memoryMb: service.config.memoryMb, cpus: service.config.cpus };
}

/** Container restarts since the replicas started, summed over replicas. */
export function restartTotal(service: Service): number {
	return (service.observed?.replicas ?? []).reduce(
		(sum, replica) => sum + replica.restartCount,
		0,
	);
}

/** One replica's icon: down is failed, up and healthy is live, anything else is starting. */
export function replicaKind(replica: {
	state: string;
	health: string;
}): StatusKind {
	if (replica.state !== "running" || replica.health === "unhealthy")
		return "failed";
	return replica.health === "healthy" ? "live" : "building";
}

/**
 * The verdict of the Health card: whether the service is serving, and why we
 * say so. Never claims a probe result the agent has not reported.
 */
export function healthVerdict(service: Service): HealthVerdict {
	const probe = probeDescription(service);
	if (service.desiredState === "stopped") {
		return {
			kind: "stopped",
			label: "Stopped",
			detail: "Restart or deploy to start it again",
		};
	}
	const latest = service.latestDeployment;
	if (service.currentDeploymentId === null) {
		if (latest && isInFlight(latest.status)) {
			return {
				kind: "deploying",
				label: "Deploying",
				detail: "The first release is on its way",
			};
		}
		return {
			kind: "unknown",
			label: "Not deployed",
			detail: "Deploy the service to start it",
		};
	}
	const server = service.server;
	if (server && (server.status === "offline" || server.status === "revoked")) {
		return {
			kind: "offline",
			label: "Server offline",
			detail: `${server.name} is not connected; the last report may be stale`,
		};
	}
	const observed = service.observed;
	if (!observed) {
		return {
			kind: "offline",
			label: "Not reported",
			detail: "The agent has not reported this service yet",
		};
	}
	if (observed.state !== "running") {
		return {
			kind: "failed",
			label: observed.state === "missing" ? "Container missing" : "Not running",
			detail: observed.error || `Container state: ${observed.state}`,
		};
	}
	const { ready, desired } = replicaCounts(service);
	switch (observed.health) {
		case "healthy":
			return { kind: "healthy", label: "Healthy", detail: probe };
		case "unhealthy":
			return { kind: "failed", label: "Unhealthy", detail: probe };
		case "degraded":
			return {
				kind: "degraded",
				label: "Degraded",
				detail: `${ready} of ${desired} replicas healthy`,
			};
		default:
			return service.config.healthcheck.type === "none"
				? { kind: "live", label: "Running", detail: probe }
				: { kind: "building", label: "Starting", detail: probe };
	}
}

/** How long the service has been up, in ms (null when it is not running). */
export function uptimeMs(service: Service, now: number): number | null {
	const observed = service.observed;
	if (observed?.state !== "running" || !observed.startedAt) return null;
	const started = Date.parse(observed.startedAt);
	return Number.isNaN(started) ? null : Math.max(0, now - started);
}

/** "14d 6h", "3h 12m", "4m", "42s". */
export function formatUptime(ms: number): string {
	const seconds = Math.floor(Math.max(0, ms) / 1000);
	const days = Math.floor(seconds / 86_400);
	const hours = Math.floor((seconds % 86_400) / 3600);
	const minutes = Math.floor((seconds % 3600) / 60);
	if (days > 0) return `${days}d ${hours}h`;
	if (hours > 0) return `${hours}h ${minutes}m`;
	if (minutes > 0) return `${minutes}m`;
	return `${seconds}s`;
}

export interface ResourceFigure {
	/** 0–100 share of the limit, or null with no limit to measure against. */
	percent: number | null;
	/** "42%" or "0.42 vCPU". */
	headline: string;
	/** "0.42 / 1 vCPU" or "312 / 512 MB". */
	detail: string;
}

function trim(value: number, digits = 2): string {
	return String(Math.round(value * 10 ** digits) / 10 ** digits);
}

/**
 * CPU as the service uses it now. The API reports percent of one core summed
 * over replicas; the limit is `cpus` per replica when set (never for the
 * total of several processes).
 */
export function cpuFigure(
	service: Service,
	point: Pick<ServiceMetricPoint, "cpuPercent" | "replicaCount"> | undefined,
): ResourceFigure {
	if (!point) return { percent: null, headline: "—", detail: "No samples yet" };
	const cores = point.cpuPercent / 100;
	const replicas = Math.max(1, point.replicaCount);
	const { cpus } = configuredLimits(service);
	const limit = cpus ? cpus * replicas : 0;
	if (limit > 0) {
		const percent = Math.min(100, (cores / limit) * 100);
		return {
			percent,
			headline: `${Math.round(percent)}%`,
			detail: `${trim(cores)} / ${trim(limit)} vCPU`,
		};
	}
	return {
		percent: null,
		headline: `${trim(cores)} vCPU`,
		detail: "No CPU limit set",
	};
}

/** Memory in use against the container limit (summed over replicas). */
export function memoryFigure(
	service: Service,
	point:
		| Pick<ServiceMetricPoint, "memBytes" | "memLimitBytes" | "replicaCount">
		| undefined,
	format: (bytes: number) => string,
): ResourceFigure {
	if (!point) return { percent: null, headline: "—", detail: "No samples yet" };
	const replicas = Math.max(1, point.replicaCount);
	const limit =
		point.memLimitBytes > 0
			? point.memLimitBytes
			: (configuredLimits(service).memoryMb ?? 0) * 1024 * 1024 * replicas;
	if (limit > 0) {
		const percent = Math.min(100, (point.memBytes / limit) * 100);
		return {
			percent,
			headline: `${Math.round(percent)}%`,
			detail: `${format(point.memBytes)} / ${format(limit)}`,
		};
	}
	return {
		percent: null,
		headline: format(point.memBytes),
		detail: "No memory limit set",
	};
}

/** The level of a log line, when it names one near its start. */
export function logLevel(line: string): "info" | "warn" | "error" | null {
	const match = /\b(INFO|WARN(?:ING)?|ERROR|ERR|FATAL|PANIC)\b/i.exec(
		line.slice(0, 64),
	);
	if (!match?.[1]) return null;
	const word = match[1].toUpperCase();
	if (word === "INFO") return "info";
	if (word.startsWith("WARN")) return "warn";
	return "error";
}

/** "agent1 has 0.4 GiB free" style one-liner for the last scale event. */
export function scaleEventLine(event: {
	from: number;
	to: number;
	reason: string;
	actor: string;
}): string {
	const reason = event.reason ? ` · ${event.reason}` : "";
	return `Scaled ${event.from} → ${event.to}${reason} · ${event.actor}`;
}

/** The autoscaling setting in a few words. */
export function autoscalingLine(scaling: Service["scaling"]): string {
	const { autoscaling } = scaling;
	return autoscaling.enabled
		? `On · ${autoscaling.min}–${autoscaling.max} replicas at ${autoscaling.targetCpuPercent}% CPU`
		: "Off";
}

/**
 * The uptime card's "Last error" line, only while it is still true. The
 * report keeps the last failed check's text after the service recovers, so a
 * card that says "Up" beside "Last error: HTTP 500" contradicts itself: the
 * error shows while the state is not up or checks are failing now.
 */
export function liveUptimeError(report: {
	state: string;
	lastError: string;
	consecutiveFailures?: number | undefined;
}): string | null {
	if (report.lastError === "") return null;
	if (report.state === "up" && (report.consecutiveFailures ?? 0) === 0) {
		return null;
	}
	return report.lastError;
}
