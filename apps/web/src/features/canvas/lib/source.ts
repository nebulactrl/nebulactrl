import type { Service } from "#/api/models";
import { routeSubtitle } from "#/features/proxy/lib/l4";

/** "acme/shop-api" from a clone URL (https, ssh or scp-style); the URL itself when it has no path. */
export function repoName(gitUrl: string): string {
	const trimmed = gitUrl
		.trim()
		.replace(/\/+$/, "")
		.replace(/\.git$/, "");
	const path = trimmed
		.replace(/^[a-z+]+:\/\/[^/]+\//i, "")
		.replace(/^[^@/]+@[^:/]+:/, "");
	const parts = path.split("/").filter(Boolean);
	if (parts.length === 0) return trimmed;
	return parts.slice(-2).join("/");
}

/**
 * The card's source line: the image reference, or `repo@branch · abc1234`
 * for a git service (the commit comes from the release that serves).
 */
export function sourceSubtitle(
	service: Pick<
		Service,
		"sourceType" | "config" | "currentRelease" | "privateHost"
	>,
): string {
	const { config } = service;
	if (service.sourceType === "external") {
		return routeSubtitle(config.route, () => undefined, service.privateHost);
	}
	if (service.sourceType === "git" || (!config.image && config.gitUrl)) {
		const repo = config.gitUrl ? repoName(config.gitUrl) : "";
		const branch = config.gitBranch ? `@${config.gitBranch}` : "";
		const sha = service.currentRelease?.source.commitSha?.slice(0, 7);
		const head = `${repo}${branch}`;
		if (head === "") return sha ?? service.privateHost;
		return sha ? `${head} · ${sha}` : head;
	}
	return config.image || service.privateHost;
}
