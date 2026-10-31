import type { Service } from "#/api/models";
import type { StatusKind } from "#/components/ui/status-icon";
import { routeSubtitle } from "#/features/proxy/lib/l4";
import { componentCountText, stackComponents } from "#/features/stacks/model";
import { nodeStatus } from "../lib/status";

/** The service's state as one status icon and one word (the `Live` pill). */
export interface ServicePill {
	kind: StatusKind;
	label: string;
}

/**
 * The header pill: Live, Building, Deploying, Failed, Stopped, Offline,
 * Degraded, Rolled back or Not deployed. Chosen from the service resource
 * alone; the control plane owns the truth, this only names it.
 */
export function servicePill(service: Service): ServicePill {
	const status = nodeStatus(service);
	switch (status.group) {
		case "in_flight":
			return status.kind === "building"
				? { kind: "building", label: "Building" }
				: { kind: "deploying", label: "Deploying" };
		case "waiting":
			return { kind: "offline", label: "Offline" };
		case "failed":
			return { kind: "failed", label: "Failed" };
		case "rolled_back":
			return { kind: "degraded", label: "Rolled back" };
		case "not_deployed":
			return { kind: "unknown", label: "Not deployed" };
		case "deployed":
			break;
	}
	if (service.desiredState === "stopped") {
		return { kind: "stopped", label: "Stopped" };
	}
	if (
		service.server?.status === "offline" ||
		service.server?.status === "revoked"
	) {
		return { kind: "offline", label: "Offline" };
	}
	if (service.maintenance?.enabled) {
		return { kind: "warning", label: "Maintenance" };
	}
	const health = service.observed?.health;
	if (health === "unhealthy" || health === "degraded") {
		return { kind: "degraded", label: "Degraded" };
	}
	return { kind: "live", label: "Live" };
}

/** `owner/repo` from a git URL (https or scp-style), or the URL minus its scheme. */
export function repoName(gitUrl: string): string {
	const trimmed = gitUrl.trim().replace(/\.git$/, "");
	const scp = /^[\w.-]+@[\w.-]+:(.+)$/.exec(trimmed);
	if (scp?.[1]) return scp[1];
	try {
		const url = new URL(trimmed);
		const path = url.pathname.replace(/^\/+|\/+$/g, "");
		return path === "" ? url.host : path;
	} catch {
		return trimmed.replace(/^[a-z]+:\/\//, "");
	}
}

export interface SourceLine {
	kind: "git" | "image" | "route" | "compose" | "empty";
	/** The parts joined by " · " in the header: repo and branch, image, or route. */
	parts: string[];
	/** The service's first domain, drawn last as a link, with how many more it has. */
	domain: { hostname: string; more: number } | null;
}

/** What the header's mono line says: `owner/repo · main · api.acme.dev +1`. */
export function sourceLine(service: Service): SourceLine {
	const parts: string[] = [];
	let kind: SourceLine["kind"] = "empty";
	if (service.sourceType === "external") {
		kind = "route";
		parts.push(
			routeSubtitle(service.config.route, () => undefined, service.privateHost),
		);
	} else if (service.kind === "stack") {
		kind = "compose";
		parts.push(
			"Docker Compose",
			componentCountText(stackComponents(service).length),
		);
	} else if (service.sourceType === "git" && service.config.gitUrl) {
		kind = "git";
		parts.push(repoName(service.config.gitUrl));
		if (service.config.gitBranch) parts.push(service.config.gitBranch);
	} else if (service.config.image) {
		kind = "image";
		parts.push(service.config.image);
	}
	const first = service.domains[0];
	const domain = first
		? { hostname: first.hostname, more: service.domains.length - 1 }
		: null;
	return { kind, parts, domain };
}
