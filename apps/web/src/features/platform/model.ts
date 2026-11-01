import type {
	PlatformKindView,
	PlatformListing,
	PlatformServiceView,
	PlatformWiringView,
	ProvisionInputBody,
} from "#/api/gen/types.gen";
import type { StatusKind } from "#/components/ui/status-icon";

export type PlatformKind = PlatformKindView["kind"];
export type PlatformStep = PlatformServiceView["step"];
export type WiringType = PlatformWiringView["type"];

/**
 * The one platform service a team can run: Headscale, the control server of
 * its own tailnet. Object storage, the image registry and central logs are
 * instance services (Admin → Built-in services) and have no set-up here.
 */
export const HEADSCALE: PlatformKind = "headscale";

// --- Words for state ---------------------------------------------------------

const STEP_WORDS: Record<PlatformStep, string> = {
	configure: "Staging",
	deploy: "Deploying",
	await: "Waiting for it to answer",
	wire: "Connecting",
	done: "Finishing up",
};

/** The plain-word name of a provisioning step. */
export function stepWords(step: PlatformStep): string {
	return STEP_WORDS[step];
}

/** The four visible stages of a set-up, in order (`done` is the end, not a stage). */
export const PROVISION_STEPS = [
	"configure",
	"deploy",
	"await",
	"wire",
] as const satisfies readonly PlatformStep[];

export interface StatusSummary {
	kind: StatusKind;
	/** The words next to the icon; never empty. */
	label: string;
	/** Work is in flight: keep polling. */
	busy: boolean;
}

/** One status line for a service: an icon kind and what to say. */
export function statusSummary(
	view: Pick<PlatformServiceView, "status" | "step">,
): StatusSummary {
	switch (view.status) {
		case "ready":
			return { kind: "live", label: "Ready", busy: false };
		case "failed":
			return { kind: "failed", label: "Failed", busy: false };
		case "removing":
			return { kind: "stopped", label: "Removing", busy: true };
		case "provisioning":
			return { kind: "deploying", label: stepWords(view.step), busy: true };
	}
}

export type StepState = "done" | "current" | "todo" | "failed";

export interface StepRow {
	step: PlatformStep;
	label: string;
	state: StepState;
}

/**
 * The stages of a set-up with where it stands: the stages before the current
 * one are done, the current one is running (or failed), the rest wait. A ready
 * service has every stage done.
 */
export function stepRows(
	view: Pick<PlatformServiceView, "status" | "step">,
): StepRow[] {
	const at =
		view.step === "done" ? PROVISION_STEPS.length : indexOfStep(view.step);
	return PROVISION_STEPS.map((step, index) => {
		let state: StepState = "todo";
		if (view.status === "ready" || index < at) state = "done";
		else if (index === at)
			state = view.status === "failed" ? "failed" : "current";
		return { step, label: stepWords(step), state };
	});
}

function indexOfStep(step: PlatformStep): number {
	const index = (PROVISION_STEPS as readonly PlatformStep[]).indexOf(step);
	return Math.max(index, 0);
}

/** A service that is still moving: keep the page fresh while any is. */
export function isBusy(
	view: Pick<PlatformServiceView, "status"> | undefined,
): boolean {
	return view?.status === "provisioning" || view?.status === "removing";
}

/** True when any set-up in the listing is provisioning or being removed. */
export function anyBusy(listing: PlatformListing | undefined): boolean {
	return (listing?.kinds ?? []).some((entry) => isBusy(entry.instance));
}

/** The follow-up poll is quick while work is in flight, slow otherwise. */
export const PLATFORM_BUSY_MS = 3_000;
export const PLATFORM_REFRESH_MS = 15_000;

export function pollInterval(busy: boolean): number {
	return busy ? PLATFORM_BUSY_MS : PLATFORM_REFRESH_MS;
}

// --- Where a service is wired ------------------------------------------------

export interface WiringTarget {
	/** The Team settings page it lives in, by name. */
	section: string;
	/** That page's slug under `/settings/`. */
	slug: string;
	/** What was created: "the Tailscale integration". */
	noun: string;
}

/** Headscale is wired into the team's Tailscale integration, and nothing else. */
export function wiringTarget(_type: WiringType): WiringTarget {
	return {
		section: "Integrations",
		slug: "integrations",
		noun: "the Tailscale integration",
	};
}

/** What a removal takes out first, in words. */
export const REMOVAL_IMPACT = "the Tailscale integration it created";

/**
 * Whether the set-up can start: it needs a server that is online and takes
 * new services.
 */
export type Availability =
	| { state: "available" }
	| { state: "exists" }
	| { state: "no_server"; reason: string };

export interface AvailabilityContext {
	/** Servers that are online and take new services. */
	usableServers: number;
}

/**
 * Whether Headscale can be set up now. One that already exists (in any state:
 * a failed one is retried from its card, not set up twice) is `exists`.
 */
export function availability(
	catalog: Pick<PlatformKindView, "instance">,
	context: AvailabilityContext,
): Availability {
	if (catalog.instance) return { state: "exists" };
	if (context.usableServers === 0) {
		return {
			state: "no_server",
			reason: "No server is online. Connect or start a server first.",
		};
	}
	return { state: "available" };
}

/** The kind's entry in a listing. */
export function kindEntry(
	listing: PlatformListing | undefined,
	kind: PlatformKind,
): PlatformKindView | undefined {
	return (listing?.kinds ?? []).find((entry) => entry.kind === kind);
}

// --- Set-up options ----------------------------------------------------------

/** What the dialog collects; every field is text until it is sent. */
export interface SetupDraft {
	serverId: string;
	hostname: string;
	publicUrl: string;
}

export const EMPTY_DRAFT: SetupDraft = {
	serverId: "",
	hostname: "",
	publicUrl: "",
};

/** The control plane's rules for a hostname, in the same order; null when valid or empty. */
export function validateHostname(value: string): string | null {
	const host = value.trim().toLowerCase().replace(/\.$/, "");
	if (host === "") return null;
	if (/[/:@\s]/.test(host)) {
		return "Enter the hostname only, without https://, a port or a path.";
	}
	if (host.length > 253) return "A hostname is at most 253 characters.";
	const labels = host.split(".");
	if (labels.length < 2) {
		return "A hostname needs at least two parts, like headscale.example.com.";
	}
	for (const label of labels) {
		if (label === "" || label.length > 63) {
			return "Each part of a hostname is 1 to 63 characters.";
		}
		if (label.startsWith("-") || label.endsWith("-")) {
			return "A part of a hostname cannot start or end with a hyphen.";
		}
		if (!/^[a-z0-9-]+$/.test(label)) {
			return "A hostname uses letters, digits, hyphens and dots only.";
		}
	}
	if (/^\d+$/.test(labels[labels.length - 1] ?? "")) {
		return "Use a name, not an IP address.";
	}
	return null;
}

/** The public URL override: http or https, the hostname only, no path. */
export function validatePublicUrl(
	value: string,
	hostname: string,
): string | null {
	const text = value.trim();
	if (text === "") return null;
	let url: URL;
	try {
		url = new URL(text);
	} catch {
		return "Enter a full address like https://headscale.example.com.";
	}
	if (
		(url.protocol !== "http:" && url.protocol !== "https:") ||
		url.username !== "" ||
		url.password !== "" ||
		url.search !== "" ||
		url.hash !== "" ||
		(url.pathname !== "" && url.pathname !== "/")
	) {
		return "Use an http or https address without a login or path.";
	}
	const host = hostname.trim().toLowerCase();
	if (host !== "" && url.hostname.toLowerCase() !== host) {
		return `The address must use the hostname ${host}.`;
	}
	return null;
}

export interface SetupFieldProblems {
	hostname?: string;
	publicUrl?: string;
	server?: string;
}

/** The server a hostname cannot be generated for: no base domain and no public address. */
export function hostnameRequired(
	server: { baseDomain: string | null; publicIp: string | null } | undefined,
): boolean {
	return server !== undefined && !server.baseDomain && !server.publicIp;
}

/**
 * Every problem in the draft, by field. `hostnameNeeded` is true when the
 * control plane cannot generate a name (the server has no domain).
 */
export function setupProblems(
	draft: SetupDraft,
	options: { hostnameNeeded: boolean },
): SetupFieldProblems {
	const problems: SetupFieldProblems = {};
	const hostname =
		draft.hostname.trim() === "" && options.hostnameNeeded
			? "Enter a hostname: this server has no domain to generate one from."
			: validateHostname(draft.hostname);
	if (hostname) problems.hostname = hostname;
	const publicUrl = validatePublicUrl(draft.publicUrl, draft.hostname);
	if (publicUrl) problems.publicUrl = publicUrl;
	if (draft.serverId === "") problems.server = "Pick the server it runs on.";
	return problems;
}

/** The first problem, for a disabled button's title. */
export function firstProblem(problems: SetupFieldProblems): string | null {
	return problems.server ?? problems.hostname ?? problems.publicUrl ?? null;
}

/** The request body: only what the person typed, so the server's defaults apply to the rest. */
export function provisionBody(
	draft: SetupDraft,
	options: { replace?: boolean },
): ProvisionInputBody {
	const hostname = draft.hostname.trim().toLowerCase().replace(/\.$/, "");
	const publicUrl = draft.publicUrl.trim();
	return {
		kind: HEADSCALE,
		serverId: draft.serverId,
		...(hostname !== "" ? { hostname } : {}),
		...(publicUrl !== "" ? { publicUrl } : {}),
		...(options.replace ? { replace: true } : {}),
	};
}

// --- Failures, in words ------------------------------------------------------

export type CreateFailureAction =
	/** The person can fix it in the dialog. */
	| "none"
	/** Reveal the hostname field as required. */
	| "hostname"
	/** Offer to replace the existing Tailscale integration. */
	| "replace"
	/** Nothing to do here; it is already there. */
	| "exists";

export interface CreateFailure {
	action: CreateFailureAction;
	message: string;
}

/**
 * What a refused set-up says and what the dialog should offer. The server's
 * sentence is kept, since it names the server or the setting; only a few get
 * a next step added.
 */
export function createFailure(
	code: string | undefined,
	message: string,
): CreateFailure {
	switch (code) {
		case "integration_exists":
			return { action: "replace", message };
		case "no_base_domain":
			return {
				action: "hostname",
				message:
					"This server has neither a base domain nor a public address, so a name cannot be made up. Enter the hostname it should be served on.",
			};
		case "platform_service_exists":
			return { action: "exists", message };
		case "acme_email_required":
			return {
				action: "none",
				message: `${message} Then try again.`,
			};
		case "proxy_unavailable":
			return {
				action: "none",
				message: `${message} Pick another server or fix its proxy first.`,
			};
		default:
			return { action: "none", message };
	}
}

export interface RemovalBlocker {
	message: string;
	/** What to do about it; empty when there is nothing to add. */
	hint: string;
}

/** A refused removal: the server's reason verbatim and a hint for what to do. */
export function removalBlocker(
	code: string | undefined,
	message: string,
): RemovalBlocker {
	if (code === "integration_in_use") {
		return {
			message,
			hint: "Stop whatever uses the integration, then remove the service.",
		};
	}
	return { message, hint: "" };
}

/** The query of a delete: the kind confirms, the data choice is the person's. */
export function removeQuery(options: { deleteData: boolean }): {
	confirm: string;
	deleteData?: boolean;
} {
	return {
		confirm: HEADSCALE,
		...(options.deleteData ? { deleteData: true } : {}),
	};
}

// --- Details and containers --------------------------------------------------

const DETAIL_LABELS: Record<string, string> = {
	apiKeyExpires: "API key expires",
};

/** The label of a `details` entry; unknown keys read as sentence-case words. */
export function detailLabel(key: string): string {
	const known = DETAIL_LABELS[key];
	if (known) return known;
	const words = key
		.replace(/([A-Z])/g, " $1")
		.replace(/[_-]+/g, " ")
		.trim();
	return words.charAt(0).toUpperCase() + words.slice(1).toLowerCase();
}

/** The details in a stable order: known keys first, then the rest alphabetically. */
export function detailEntries(
	details: Readonly<Record<string, string>> | undefined,
): Array<[string, string]> {
	const order = Object.keys(DETAIL_LABELS);
	return Object.entries(details ?? {})
		.filter(([, value]) => value !== "")
		.sort(([a], [b]) => {
			const ia = order.indexOf(a);
			const ib = order.indexOf(b);
			if (ia !== ib) return (ia < 0 ? 99 : ia) - (ib < 0 ? 99 : ib);
			return a.localeCompare(b);
		});
}

type ContainerState = NonNullable<
	PlatformServiceView["services"]
>[number]["state"];

const CONTAINER_STATE: Record<
	ContainerState,
	{ kind: StatusKind; label: string }
> = {
	running: { kind: "live", label: "Running" },
	starting: { kind: "deploying", label: "Starting" },
	stopped: { kind: "stopped", label: "Stopped" },
	staged: { kind: "pending", label: "Staged, not deployed yet" },
	not_deployed: { kind: "pending", label: "Not deployed" },
	missing: { kind: "failed", label: "Missing" },
};

/** The icon and words for one of the service's containers. */
export function containerState(state: ContainerState): {
	kind: StatusKind;
	label: string;
} {
	return CONTAINER_STATE[state];
}

/** The canvas of the system project, where the services can be inspected. */
export function canvasTarget(
	view: Pick<PlatformServiceView, "projectId" | "environmentSlug">,
): { projectId: string; envSlug: string } | null {
	return view.projectId
		? { projectId: view.projectId, envSlug: view.environmentSlug || "platform" }
		: null;
}
