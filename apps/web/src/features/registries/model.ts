// Pure model for registries: the add/edit form's validation, the shapes the
// UI reads, and the words for check results, sizes and garbage collection.
// No DOM, no network, no generated client: `model.test.ts` covers it all.

type RegistryKind = "external" | "builtin";

/** One "Test registry" result, from one server's point of view. */
export interface RegistryCheck {
	ok: boolean;
	push: boolean;
	pull: boolean;
	error: string;
	/** ISO time of the check. */
	at: string;
}

export type BuiltinStatus = "ready" | "unavailable";
export type GcState = "idle" | "readonly" | "collecting";

/** The built-in registry's state: the instance's registry, this team's share of it. */
export interface BuiltinInfo {
	status: BuiltinStatus;
	statusMessage: string;
	gcState: GcState;
	lastGcAt: string | null;
}

/** A registry as the list shows it. Credentials never come back, only whether they are set. */
export interface Registry {
	id: string;
	name: string;
	kind: RegistryKind;
	host: string;
	namespace: string;
	hasPushAuth: boolean;
	pushUsername: string;
	hasPullAuth: boolean;
	pullUsername: string;
	manageRetention: boolean;
	/** Latest check per server id. */
	lastCheck: Record<string, RegistryCheck>;
	imageCount: number;
	sizeBytes: number;
	/** Set for the team's built-in registry, which the instance admin runs. */
	builtin: BuiltinInfo | null;
	/** A team or project setting points here, so it cannot be deleted. */
	inUse: boolean;
	createdAt: string;
}

export type ImageState = "live" | "expired" | "deleted";

export interface RegistryImage {
	id: string;
	repository: string;
	digest: string;
	platform: string;
	sizeBytes: number | null;
	state: ImageState;
	pushedAt: string;
	serviceId: string | null;
	releaseId: string | null;
}

// --------------------------------------------------------------------- forms

/** The add and edit form's working copy; every field is the text typed. */
export interface RegistryDraft {
	name: string;
	host: string;
	namespace: string;
	pushUsername: string;
	pushPassword: string;
	/** A separate read-only credential for the servers that pull. */
	pullEnabled: boolean;
	pullUsername: string;
	pullPassword: string;
}

export const EMPTY_DRAFT: RegistryDraft = {
	name: "",
	host: "",
	namespace: "",
	pushUsername: "",
	pushPassword: "",
	pullEnabled: false,
	pullUsername: "",
	pullPassword: "",
};

/** The form of an existing registry: credentials stay empty, which keeps them. */
export function draftOf(registry: Registry): RegistryDraft {
	return {
		name: registry.name,
		host: registry.host,
		namespace: registry.namespace,
		pushUsername: registry.pushUsername,
		pushPassword: "",
		pullEnabled: registry.hasPullAuth,
		pullUsername: registry.pullUsername,
		pullPassword: "",
	};
}

export type DraftErrors = Partial<Record<keyof RegistryDraft, string>>;

const SCHEME = /^[a-z][a-z0-9+.-]*:\/\//i;
// A DNS name or IPv4 address: dot-separated labels of letters, digits and
// hyphens that neither start nor end with a hyphen. A bracketed IPv6 address
// is not accepted; a registry should have a name.
const HOST_NAME =
	/^(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)*$/;
// One path component of an OCI repository name: lowercase alphanumerics,
// joined by single `.`, `_`, `__` or runs of `-`.
// https://github.com/opencontainers/distribution-spec/blob/main/spec.md#pulling-manifests
const NAMESPACE_PART = /^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$/;

/** What is wrong with a registry host, or undefined. It is `host` or `host:port`: no scheme, no path. */
export function hostError(value: string): string | undefined {
	const host = value.trim();
	if (host === "") return "Enter the registry host, such as ghcr.io.";
	if (SCHEME.test(host)) {
		return "Enter the host only, without https:// or http://.";
	}
	if (host.includes("/")) {
		return "Enter the host only. Put the path in Namespace.";
	}
	if (/\s/.test(host)) return "A host has no spaces.";
	if (host !== host.toLowerCase()) return "Use lowercase letters.";
	const colon = host.lastIndexOf(":");
	const name = colon === -1 ? host : host.slice(0, colon);
	if (colon !== -1) {
		const port = host.slice(colon + 1);
		const number = Number(port);
		if (!/^\d+$/.test(port) || number < 1 || number > 65535) {
			return "The port must be a number from 1 to 65535.";
		}
	}
	if (!HOST_NAME.test(name) || name.length > 253) {
		return "That is not a valid host name. Use letters, digits, dots and hyphens.";
	}
	return undefined;
}

/**
 * What is wrong with a namespace, or undefined. Empty is allowed (a registry
 * that takes repositories at its root). Otherwise it is `acme` or
 * `acme/apps`: lowercase letters, digits and `. _ -` inside each part.
 */
export function namespaceError(value: string): string | undefined {
	const namespace = value.trim();
	if (namespace === "") return undefined;
	if (namespace !== namespace.toLowerCase()) {
		return "Use lowercase letters; registries refuse capitals.";
	}
	if (namespace.startsWith("/") || namespace.endsWith("/")) {
		return "No slash at the start or the end.";
	}
	for (const part of namespace.split("/")) {
		if (part === "") return "No empty part between slashes.";
		if (!NAMESPACE_PART.test(part)) {
			return "Use lowercase letters, digits and . _ - only, with a letter or digit at each end of every part.";
		}
	}
	return undefined;
}

/** Trims the host and the namespace so what is sent is what is checked. */
function normalize(draft: RegistryDraft): RegistryDraft {
	return {
		...draft,
		name: draft.name.trim(),
		host: draft.host.trim(),
		namespace: draft.namespace.trim().replace(/^\/+|\/+$/g, ""),
		pushUsername: draft.pushUsername.trim(),
		pullUsername: draft.pullUsername.trim(),
	};
}

/** Which passwords must be typed again before an edit can be saved. */
export interface RequiredPasswords {
	push: boolean;
	pull: boolean;
}

export const HOST_CHANGE_HINT =
	"Changing the host needs the credentials entered again.";

/**
 * The control plane refuses a host change unless the push password is sent
 * again, and the pull password too when a pull credential is stored and kept
 * (turning it off with `clearPull` needs none). The stored secrets were issued
 * for the old host, so they are never carried over to a new one. With the host
 * unchanged nothing is required and an empty password keeps what is stored.
 */
export function requiredPasswords(
	draft: RegistryDraft,
	registry: Registry | null,
): RequiredPasswords {
	if (registry === null) return { push: false, pull: false };
	const hostChanged = draft.host.trim() !== registry.host;
	return {
		push: hostChanged,
		pull: hostChanged && registry.hasPullAuth && draft.pullEnabled,
	};
}

/** What the form shows when the server answers `secret_required`: the passwords the host change needs, by field. */
export function secretRequiredErrors(
	draft: RegistryDraft,
	registry: Registry | null,
): DraftErrors {
	const errors: DraftErrors = {
		pushPassword: "Enter the password again; the host changed.",
	};
	if (requiredPasswords(draft, registry).pull) {
		errors.pullPassword = "Enter the pull password again; the host changed.";
	}
	return errors;
}

/**
 * What is wrong with the form, by field. `editing` means credentials are
 * already stored, so an empty password keeps them, unless `reenter` says the
 * host changed and the password must be typed again.
 */
export function validateDraft(
	draft: RegistryDraft,
	options: {
		editing: boolean;
		hadPull?: boolean;
		reenter?: RequiredPasswords;
	},
): DraftErrors {
	const errors: DraftErrors = {};
	if (draft.name.trim() === "") errors.name = "Give the registry a name.";
	const host = hostError(draft.host);
	if (host) errors.host = host;
	const namespace = namespaceError(draft.namespace);
	if (namespace) errors.namespace = namespace;
	if (draft.pushUsername.trim() === "") {
		errors.pushUsername = "Enter the user name the builds push with.";
	}
	if (!options.editing && draft.pushPassword === "") {
		errors.pushPassword = "Paste the token or password the builds push with.";
	} else if (options.reenter?.push && draft.pushPassword === "") {
		errors.pushPassword =
			"Enter the token or password again; the host changed.";
	}
	if (draft.pullEnabled) {
		if (draft.pullUsername.trim() === "") {
			errors.pullUsername = "Enter the user name servers pull with.";
		}
		if (draft.pullPassword === "" && !(options.editing && options.hadPull)) {
			errors.pullPassword = "Paste the read-only token servers pull with.";
		} else if (options.reenter?.pull && draft.pullPassword === "") {
			errors.pullPassword =
				"Enter the pull token or password again; the host changed.";
		}
	}
	return errors;
}

/** `ghcr.io/acme/apps`, or the bare host when there is no namespace. */
export function repositoryRoot(host: string, namespace: string): string {
	const ns = namespace.trim().replace(/^\/+|\/+$/g, "");
	return ns === "" ? host.trim() : `${host.trim()}/${ns}`;
}

/** The body of POST /registries from a valid draft. */
export interface CreateRegistryBody {
	name: string;
	host: string;
	namespace: string;
	pushUsername: string;
	pushPassword: string;
	pullUsername?: string;
	pullPassword?: string;
}

export function createBody(draft: RegistryDraft): CreateRegistryBody {
	const d = normalize(draft);
	const body: CreateRegistryBody = {
		name: d.name,
		host: d.host,
		namespace: d.namespace,
		pushUsername: d.pushUsername,
		pushPassword: d.pushPassword,
	};
	if (d.pullEnabled) {
		body.pullUsername = d.pullUsername;
		body.pullPassword = d.pullPassword;
	}
	return body;
}

/** The body of PATCH /registries/{id}: only what changed; an empty password keeps the stored one. */
export interface UpdateRegistryBody {
	name?: string;
	host?: string;
	namespace?: string;
	pushUsername?: string;
	pushPassword?: string;
	pullUsername?: string;
	pullPassword?: string;
	clearPull?: boolean;
}

export function updateBody(
	draft: RegistryDraft,
	registry: Registry,
): UpdateRegistryBody {
	const d = normalize(draft);
	const body: UpdateRegistryBody = {};
	if (d.name !== registry.name) body.name = d.name;
	if (d.host !== registry.host) body.host = d.host;
	if (d.namespace !== registry.namespace) body.namespace = d.namespace;
	if (d.pushUsername !== registry.pushUsername) {
		body.pushUsername = d.pushUsername;
	}
	if (d.pushPassword !== "") body.pushPassword = d.pushPassword;
	if (!d.pullEnabled) {
		if (registry.hasPullAuth) body.clearPull = true;
	} else {
		if (d.pullUsername !== registry.pullUsername) {
			body.pullUsername = d.pullUsername;
		}
		if (d.pullPassword !== "") body.pullPassword = d.pullPassword;
	}
	return body;
}

// ------------------------------------------------------------- check results

/** "Pushes and pulls work", "Push works, pull failed", or the registry's own error. */
export function checkSummary(check: RegistryCheck): string {
	if (check.ok) return "Push and pull work";
	if (check.push && !check.pull) return "Push works, pull failed";
	if (!check.push && check.pull) return "Pull works, push failed";
	return check.error !== "" ? check.error : "Could not reach the registry";
}

/** The status kind that matches a check: a pass, or a failure. */
export function checkKind(check: RegistryCheck): "healthy" | "failed" {
	return check.ok ? "healthy" : "failed";
}

/** Servers that can run a check: online and able to build and push. */
export interface CheckServer {
	id: string;
	name: string;
	status: string;
	buildEnabled: boolean;
}

/**
 * The servers offered under "Test registry": a registry is used from build
 * servers (to push) and from every server that runs an app (to pull), so
 * every online server can be tested; build servers come first.
 */
export function checkableServers<T extends CheckServer>(
	servers: readonly T[],
): T[] {
	return servers
		.filter((server) => server.status === "online")
		.sort(
			(a, b) =>
				Number(b.buildEnabled) - Number(a.buildEnabled) ||
				a.name.localeCompare(b.name),
		);
}

// ------------------------------------------------------------------- built-in

export const BUILTIN_STATUS_LABEL: Record<BuiltinStatus, string> = {
	ready: "Ready",
	unavailable: "Unavailable",
};

export const GC_LABEL: Record<GcState, string> = {
	idle: "Idle",
	readonly: "Read-only for cleanup",
	collecting: "Cleaning up",
};

/** The built-in registry's cleanup line: when it last ran, or that it has not. */
export function gcLine(info: BuiltinInfo, now: number = Date.now()): string {
	if (info.gcState === "collecting") return "Cleaning up now";
	if (info.gcState === "readonly") return "Read-only while cleanup starts";
	if (info.lastGcAt === null) return "No cleanup yet";
	const at = new Date(info.lastGcAt).getTime();
	if (Number.isNaN(at)) return "No cleanup yet";
	const minutes = Math.max(0, Math.round((now - at) / 60_000));
	if (minutes < 1) return "Last cleanup just now";
	if (minutes < 60) return `Last cleanup ${minutes} min ago`;
	const hours = Math.round(minutes / 60);
	if (hours < 48) return `Last cleanup ${hours} h ago`;
	return `Last cleanup ${Math.round(hours / 24)} days ago`;
}

/** A registry that deployments cannot use now: the built-in one while the instance's registry is down. */
export function isUsable(registry: Registry): boolean {
	return registry.builtin === null || registry.builtin.status === "ready";
}

/** The registries a select offers: usable ones first, then the rest by name. */
export function selectableRegistries(
	registries: readonly Registry[],
): Registry[] {
	return [...registries].sort(
		(a, b) =>
			Number(isUsable(b)) - Number(isUsable(a)) || a.name.localeCompare(b.name),
	);
}

/** The select's text for a registry: its name and where it is. */
export function registryOptionText(registry: Registry): string {
	const root = repositoryRoot(registry.host, registry.namespace);
	return registry.kind === "builtin"
		? `${registry.name} (built-in)`
		: `${registry.name} (${root})`;
}

/** `sha256:0a1b2c3d4e5f`: the digest of an image cut to twelve hex characters; anything else comes back as it is. */
export function shortImageDigest(digest: string): string {
	const match = /^sha256:([0-9a-f]{12})[0-9a-f]*$/.exec(digest);
	return match ? `sha256:${match[1]}` : digest;
}
