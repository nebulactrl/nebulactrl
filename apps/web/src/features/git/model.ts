import type { BrandSlug } from "#/assets/brand-icons";

export type GitProvider =
	| "github"
	| "gitlab"
	| "gitea"
	| "forgejo"
	| "bitbucket";

export interface ProviderInfo {
	id: GitProvider;
	label: string;
	/** One line under the name in the connect picker. */
	blurb: string;
	/** Self-hosted by default: the instance URL is required. */
	instanceUrlRequired: boolean;
	/** The URL used when the field is left empty. */
	defaultBaseUrl: string;
	/** Needs the repository webhook created by hand. */
	manualWebhook: boolean;
	slug: BrandSlug;
}

export const PROVIDERS: readonly ProviderInfo[] = [
	{
		id: "github",
		label: "GitHub",
		blurb:
			"One click creates a GitHub App: pushes, pull requests and statuses.",
		instanceUrlRequired: false,
		defaultBaseUrl: "https://github.com",
		manualWebhook: false,
		slug: "github",
	},
	{
		id: "gitlab",
		label: "GitLab",
		blurb: "gitlab.com or your own instance. Token or OAuth application.",
		instanceUrlRequired: false,
		defaultBaseUrl: "https://gitlab.com",
		manualWebhook: false,
		slug: "gitlab",
	},
	{
		id: "gitea",
		label: "Gitea",
		blurb: "Your Gitea instance. Token or OAuth application.",
		instanceUrlRequired: true,
		defaultBaseUrl: "",
		manualWebhook: false,
		slug: "gitea",
	},
	{
		id: "forgejo",
		label: "Forgejo",
		blurb: "Your Forgejo or Codeberg instance. Token or OAuth application.",
		instanceUrlRequired: true,
		defaultBaseUrl: "",
		manualWebhook: false,
		slug: "forgejo",
	},
	{
		id: "bitbucket",
		label: "Bitbucket",
		blurb: "A signed push webhook. No repository picker and no previews.",
		instanceUrlRequired: false,
		defaultBaseUrl: "https://bitbucket.org",
		manualWebhook: true,
		slug: "bitbucket",
	},
];

export function providerInfo(id: string): ProviderInfo {
	return (
		PROVIDERS.find((p) => p.id === id) ?? {
			id: "gitea",
			label: id,
			blurb: "",
			instanceUrlRequired: true,
			defaultBaseUrl: "",
			manualWebhook: false,
			slug: "gitea",
		}
	);
}

export type ConnectionTone = "live" | "warn" | "bad";

export const CONNECTION_STATUS: Record<
	string,
	{ label: string; tone: ConnectionTone }
> = {
	active: { label: "Connected", tone: "live" },
	pending: { label: "Waiting to finish", tone: "warn" },
	error: { label: "Needs attention", tone: "bad" },
};

export const DELIVERY_OUTCOME: Record<
	string,
	{ label: string; tone: ConnectionTone | "off" }
> = {
	processed: { label: "Processed", tone: "live" },
	ignored: { label: "Ignored", tone: "off" },
	replayed: { label: "Replay rejected", tone: "warn" },
	rejected: { label: "Rejected", tone: "bad" },
	error: { label: "Error", tone: "bad" },
};

/** One pattern per line (or comma): the watch paths as the API takes them. */
export function parseWatchPaths(text: string): string[] {
	const seen = new Set<string>();
	const out: string[] = [];
	for (const raw of text.split(/[\n,]/)) {
		const p = raw.trim();
		if (p !== "" && !seen.has(p)) {
			seen.add(p);
			out.push(p);
		}
	}
	return out;
}

export function formatWatchPaths(
	paths: readonly string[] | null | undefined,
): string {
	return (paths ?? []).join("\n");
}

/** Cheap client-side check; the server validates for real. */
export function watchPathProblem(paths: readonly string[]): string | undefined {
	if (paths.length > 50) return "At most 50 watch paths.";
	for (const p of paths) {
		const body = p.startsWith("!") ? p.slice(1) : p;
		if (body.split("/").includes("..")) return `"${p}" must not contain ..`;
		if (p.length > 255) return `"${p.slice(0, 20)}…" is too long.`;
	}
	return undefined;
}

export function shortSha(sha: string): string {
	return sha.slice(0, 7);
}

/** The first line of a commit message, cut to `max` characters. */
export function commitSubject(message: string, max = 72): string {
	const first = message.split("\n", 1)[0]?.trim() ?? "";
	return first.length > max ? `${first.slice(0, max - 1)}…` : first;
}

export interface FlowResult {
	connected: string | null;
	notice: string | null;
	error: string | null;
	/** The query string without the flow parameters ("" when nothing else is left). */
	rest: string;
}

/** Reads (and strips) the outcome the connection flows redirect back with. */
export function readFlowResult(search: string): FlowResult {
	const params = new URLSearchParams(search);
	const take = (key: string): string | null => {
		const v = params.get(key);
		params.delete(key);
		return v && v !== "" ? v : null;
	};
	const connected = take("git_connected");
	const notice = take("git_notice");
	const error = take("git_error");
	const rest = params.toString();
	return { connected, notice, error, rest: rest === "" ? "" : `?${rest}` };
}

/**
 * Submits the GitHub App manifest the way GitHub requires: a form POST in the
 * browser (the page navigates to github.com).
 */
export function submitManifestForm(postUrl: string, manifest: string): void {
	const form = document.createElement("form");
	form.method = "post";
	form.action = postUrl;
	const field = document.createElement("input");
	field.type = "hidden";
	field.name = "manifest";
	field.value = manifest;
	form.appendChild(field);
	document.body.appendChild(form);
	form.submit();
}
