import type { TailnetConfig } from "#/api/gen/types.gen";

// The tailnet part of the settings form: a working copy that keeps ports as
// typed text, the static rules the control plane enforces (so a bad value is
// caught before the PATCH), and the canonical config sent to it. Nothing here
// knows about the integration; provider-specific limits (no Funnel on
// Headscale) are the section's business.

export const MAX_TCP_FORWARDS = 16;
export const MAX_EXTRA_TAGS = 10;
const MAX_LABEL = 63;

const LABEL_RE = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/;
const TAG_RE = /^tag:[a-zA-Z][a-zA-Z0-9-]{0,62}$/;

export interface TailnetPortDraft {
	/** The tailnet port, as typed. */
	listen: string;
	/** The service port, as typed; empty means the same as `listen`. */
	target: string;
	tls: boolean;
}

export interface TailnetDraft {
	enabled: boolean;
	/** Empty derives `<service>-<environment>`. */
	hostname: string;
	extraTags: string[];
	/** Serve the HTTP port: HTTPS on 443 (Tailscale) or HTTP on 80 (Headscale). */
	web: boolean;
	tcp: TailnetPortDraft[];
	funnel: boolean;
	/** "Tailnet only": public domains are not routed. */
	hidePublic: boolean;
}

export type TailnetErrorKey =
	| "tailnetHostname"
	| "tailnetTags"
	| "tailnetWeb"
	| "tailnetTcp"
	| "tailnetFunnel";

/** `tailnet` itself carries a problem with the section as a whole. */
export type TailnetErrors = Partial<
	Record<TailnetErrorKey | "tailnet", string>
>;

/** What the section is allowed to depend on from the service's own form. */
export interface TailnetContext {
	/** The service's HTTP port as typed in the Networking section. */
	port: string;
	oneshot: boolean;
}

/** A fresh, enabled draft: the web exposure is on when there is an HTTP port. */
export function emptyTailnetDraft(hasPort: boolean): TailnetDraft {
	return {
		enabled: true,
		hostname: "",
		extraTags: [],
		web: hasPort,
		tcp: [],
		funnel: false,
		hidePublic: false,
	};
}

/** The stored config as a draft; null when the service is not on a tailnet. */
export function toTailnetDraft(
	config: TailnetConfig | undefined,
): TailnetDraft | null {
	if (!config?.enabled) return null;
	return {
		enabled: true,
		hostname: config.hostname ?? "",
		extraTags: [...(config.extraTags ?? [])],
		web: config.web === true,
		tcp: (config.tcp ?? []).map((forward) => ({
			listen: String(forward.listen),
			target: String(forward.target || forward.listen),
			tls: forward.tls === true,
		})),
		funnel: config.funnel === true,
		hidePublic: config.hidePublic === true,
	};
}

export const normalizeHostname = (hostname: string): string =>
	hostname.trim().toLowerCase();

/** Trimmed, without blanks, sorted and de-duplicated. */
export function normalizeTags(tags: readonly string[]): string[] {
	const trimmed = tags.map((tag) => tag.trim()).filter((tag) => tag !== "");
	return [...new Set(trimmed)].sort();
}

const int = (value: string): number => Number.parseInt(value, 10);

/**
 * The canonical config the PATCH carries, or undefined when the service is not
 * on a tailnet (the server stores a disabled config as absent). Hostname is
 * lowercased, tags are sorted and unique, ports are sorted by listen port and
 * a blank target means the listen port.
 */
export function buildTailnetConfig(
	draft: TailnetDraft | null,
): TailnetConfig | undefined {
	if (!draft?.enabled) return undefined;
	const config: TailnetConfig = { enabled: true };
	const hostname = normalizeHostname(draft.hostname);
	if (hostname !== "") config.hostname = hostname;
	const tags = normalizeTags(draft.extraTags);
	if (tags.length > 0) config.extraTags = tags;
	if (draft.web) config.web = true;
	const tcp = draft.tcp
		.map((row) => {
			const listen = int(row.listen);
			const target = row.target.trim() === "" ? listen : int(row.target);
			return { listen, target, ...(row.tls ? { tls: true } : {}) };
		})
		.sort((a, b) => a.listen - b.listen);
	if (tcp.length > 0) config.tcp = tcp;
	if (draft.funnel) config.funnel = true;
	if (draft.hidePublic) config.hidePublic = true;
	return config;
}

function sanitizeLabel(value: string): string {
	let label = "";
	for (const char of value.toLowerCase()) {
		if (/[a-z0-9]/.test(char)) label += char;
		else if (label !== "") label += "-";
	}
	return label.replace(/^-+|-+$/g, "");
}

/** `<service>-<environment>` as one DNS label, the way the control plane derives it. */
export function defaultTailnetHostname(
	serviceSlug: string,
	envSlug: string,
): string {
	let hostname = sanitizeLabel(serviceSlug);
	const env = sanitizeLabel(envSlug);
	if (env !== "" && env !== hostname) {
		hostname = hostname === "" ? env : `${hostname}-${env}`;
	}
	if (hostname === "") hostname = "service";
	if (hostname.length > MAX_LABEL) {
		hostname = hostname.slice(0, MAX_LABEL).replace(/-+$/, "");
	}
	return hostname;
}

/** The problem with a hostname as the control plane will see it, or undefined. */
export function hostnameProblem(hostname: string): string | undefined {
	const normalized = normalizeHostname(hostname);
	if (normalized === "") return undefined;
	if (normalized.length > MAX_LABEL || !LABEL_RE.test(normalized)) {
		return `Use lower-case letters, digits and dashes, starting and ending with a letter or digit (max ${MAX_LABEL}).`;
	}
	return undefined;
}

function portProblem(value: string, label: string): string | undefined {
	const text = value.trim();
	if (!/^\d+$/.test(text)) return `${label} must be a whole number.`;
	const port = Number(text);
	if (port < 1 || port > 65535) return `${label} must be between 1 and 65535.`;
	return undefined;
}

/** One message per TCP row (undefined = fine), in row order. */
export function validateTcpRows(
	rows: readonly TailnetPortDraft[],
	web: boolean,
): (string | undefined)[] {
	const seen = new Set<number>();
	return rows.map((row) => {
		if (row.listen.trim() === "") return "Listen port is required.";
		const listen = portProblem(row.listen, "Listen port");
		if (listen) return listen;
		if (row.target.trim() !== "") {
			const target = portProblem(row.target, "Service port");
			if (target) return target;
		}
		const port = int(row.listen);
		if (seen.has(port)) return `Listen port ${port} is used twice.`;
		seen.add(port);
		if (web && (port === 80 || port === 443)) {
			return "Ports 80 and 443 are used by the web exposure.";
		}
		return undefined;
	});
}

/** Tags the integration does not allow; empty when the allow-list is unknown. */
export function tagsNotAllowed(
	tags: readonly string[],
	allowed: readonly string[] | null | undefined,
): string[] {
	if (!allowed) return [];
	return normalizeTags(tags).filter((tag) => !allowed.includes(tag));
}

/**
 * What is wrong with the tailnet draft, by field; empty means it can be sent.
 * A draft that is off (null or disabled) is never wrong.
 */
export function validateTailnet(
	draft: TailnetDraft | null,
	context: TailnetContext,
): TailnetErrors {
	const errors: TailnetErrors = {};
	if (!draft?.enabled) return errors;
	if (context.oneshot) {
		errors.tailnet = "A one-shot job cannot be on a tailnet.";
	}
	const hostname = hostnameProblem(draft.hostname);
	if (hostname) errors.tailnetHostname = hostname;

	const tags = normalizeTags(draft.extraTags);
	if (tags.length > MAX_EXTRA_TAGS) {
		errors.tailnetTags = `At most ${MAX_EXTRA_TAGS} extra tags.`;
	} else {
		const bad = tags.find((tag) => !TAG_RE.test(tag));
		if (bad) {
			errors.tailnetTags = `“${bad}” is not a tag. Use tag:name with letters, digits and dashes, starting with a letter.`;
		}
	}

	const port = Number.parseInt(context.port, 10);
	const hasPort = Number.isInteger(port) && port > 0;
	if (draft.web && !hasPort) {
		errors.tailnetWeb =
			"The web exposure needs the service to have an HTTP port.";
	} else if (!draft.web && draft.tcp.length === 0) {
		errors.tailnetWeb = "Expose the web port or add at least one TCP port.";
	}
	if (draft.funnel && !draft.web) {
		errors.tailnetFunnel =
			"Funnel publishes the web exposure; turn the web exposure on first.";
	}

	if (draft.tcp.length > MAX_TCP_FORWARDS) {
		errors.tailnetTcp = `At most ${MAX_TCP_FORWARDS} TCP ports.`;
	} else {
		const rows = validateTcpRows(draft.tcp, draft.web);
		const index = rows.findIndex((problem) => problem !== undefined);
		if (index >= 0) errors.tailnetTcp = `Port ${index + 1}: ${rows[index]}`;
	}
	return errors;
}

/** One-click rows for the services people put on a tailnet most. */
export const TCP_PRESETS: readonly { label: string; port: number }[] = [
	{ label: "Postgres", port: 5432 },
	{ label: "Redis", port: 6379 },
	{ label: "MySQL", port: 3306 },
	{ label: "Minecraft", port: 25565 },
];

/**
 * The rows with `port` filled in: into a blank row if there is one, else as a
 * new row. A port that is already listed, or a full list, changes nothing.
 */
export function withTcpPreset(
	rows: readonly TailnetPortDraft[],
	port: number,
): TailnetPortDraft[] {
	const text = String(port);
	if (rows.some((row) => row.listen.trim() === text)) return [...rows];
	const blank = rows.findIndex(
		(row) => row.listen.trim() === "" && row.target.trim() === "",
	);
	if (blank >= 0) {
		return rows.map((row, index) =>
			index === blank ? { ...row, listen: text, target: text } : row,
		);
	}
	if (rows.length >= MAX_TCP_FORWARDS) return [...rows];
	return [...rows, { listen: text, target: text, tls: false }];
}

/** The tags every node carries, and the ones a service may add on top. */
export interface TagPolicy {
	/** Added to every node; not a per-service choice. */
	defaults: string[];
	/** The integration's allow-list, default tags included. */
	allowed: string[];
}

/**
 * The integration's tag rules, or null when it is unknown (not connected, or
 * the reader may not see it). An empty allow-list means the default tags.
 */
export function tagPolicy(
	integration:
		| {
				configured: boolean;
				defaultTags: string[] | null;
				allowedTags: string[] | null;
		  }
		| undefined,
): TagPolicy | null {
	if (!integration?.configured) return null;
	const defaults = integration.defaultTags ?? [];
	const listed = integration.allowedTags ?? [];
	return {
		defaults,
		allowed: normalizeTags([
			...(listed.length > 0 ? listed : defaults),
			...defaults,
		]),
	};
}
