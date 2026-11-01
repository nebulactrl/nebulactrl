// Pure model for build servers: what a server's role means, where a build
// runs once the service, project and team settings are read in order, and how
// a plan's placement reads in words. Nothing here touches the DOM, the
// network or the generated client, so `model.test.ts` covers all of it.

/** Where a service, project or team builds its images. */
export type BuildLocation = "inherit" | "target" | "build_server";

/** A team can only choose a real location; `inherit` has nothing above it. */
export type TeamBuildLocation = Exclude<BuildLocation, "inherit">;

/** The three jobs a server can have. */
export type ServerRole = "deployments" | "builds" | "both";

/** The server fields that decide its role. */
export interface RoleFlags {
	acceptWorkloads: boolean;
	buildEnabled: boolean;
}

/** The build capability an agent advertises in `facts.capabilities`. */
export const CAP_BUILD_PUSH = "build.push";

/** The PATCH /servers bounds of `buildMaxConcurrent` (the `servers.build_max_concurrent` CHECK). */
export const BUILD_CONCURRENCY = { min: 1, max: 16, default: 2 } as const;

export interface RoleOption {
	value: ServerRole;
	label: string;
	/** What choosing it means, in one plain sentence shown under the control. */
	consequence: string;
}

export const ROLE_OPTIONS: readonly RoleOption[] = [
	{
		value: "deployments",
		label: "Deployments",
		consequence:
			"Deployments: apps run here. Builds for projects that use build servers never run here.",
	},
	{
		value: "builds",
		label: "Builds",
		consequence:
			"Builds: no apps are placed here; builds for projects that use build servers run here.",
	},
	{
		value: "both",
		label: "Both",
		consequence:
			"Both: apps run here, and builds for projects that use build servers also run here, sharing this server's CPU, memory and disk with them.",
	},
];

/** The role a server has today, from its two flags. */
export function serverRole(flags: RoleFlags): ServerRole {
	if (!flags.buildEnabled) return "deployments";
	return flags.acceptWorkloads ? "both" : "builds";
}

/** The one-line consequence of a role. */
export function roleConsequence(role: ServerRole): string {
	return (
		ROLE_OPTIONS.find((option) => option.value === role)?.consequence ?? ""
	);
}

/**
 * The flags after the role changes. Leaving "Builds" for "Deployments" turns
 * workloads back on, since that is what a deployments server is; a
 * deployments server that was already paused (workloads off, build off)
 * keeps its switch as it was, because that is a different state the role
 * does not describe.
 */
export function applyRole(current: RoleFlags, role: ServerRole): RoleFlags {
	switch (role) {
		case "builds":
			return { buildEnabled: true, acceptWorkloads: false };
		case "both":
			return { buildEnabled: true, acceptWorkloads: true };
		case "deployments":
			return {
				buildEnabled: false,
				acceptWorkloads: current.buildEnabled ? true : current.acceptWorkloads,
			};
	}
}

/** The server card's badge: null for a plain deployments server. */
export function roleBadge(flags: RoleFlags): string | null {
	switch (serverRole(flags)) {
		case "builds":
			return "Build server";
		case "both":
			return "Apps and builds";
		case "deployments":
			return null;
	}
}

/** "Max builds at once": a whole number 1 to 16, or null. */
export function parseConcurrency(text: string): number | null {
	const value = Number(text);
	const { min, max } = BUILD_CONCURRENCY;
	return text.trim() !== "" &&
		Number.isInteger(value) &&
		value >= min &&
		value <= max
		? value
		: null;
}

/** The problem with a "max builds" field, or undefined when it is fine. */
export function concurrencyError(text: string): string | undefined {
	return parseConcurrency(text) === null
		? `Use a whole number from ${BUILD_CONCURRENCY.min} to ${BUILD_CONCURRENCY.max}.`
		: undefined;
}

// ------------------------------------------------------------- build servers

/** The slice of a server the placement controls read. */
export interface BuildServerLike {
	id: string;
	name: string;
	status: string;
	buildEnabled: boolean;
	facts?: { capabilities?: readonly string[] | null | undefined } | null;
}

/** Servers that can be pinned: build-enabled and not revoked or still waiting for an agent. */
export function pinnableServers<T extends BuildServerLike>(
	servers: readonly T[],
): T[] {
	return servers.filter(
		(server) =>
			server.buildEnabled &&
			server.status !== "revoked" &&
			server.status !== "pending",
	);
}

/**
 * Whether the agent can build and push. An agent that has not reported its
 * facts yet counts as unknown (null), not as unable.
 */
export function canBuildAndPush(server: BuildServerLike): boolean | null {
	const capabilities = server.facts?.capabilities;
	if (!server.facts || !capabilities) return null;
	return capabilities.includes(CAP_BUILD_PUSH);
}

/** What is wrong with a build server as a pin target, in words; null when nothing. */
export function pinProblem(server: BuildServerLike): string | null {
	if (server.status !== "online") return `${server.status}`;
	if (canBuildAndPush(server) === false) return "agent needs an update";
	return null;
}

// ---------------------------------------------------------------- resolution

/** One level of the chain: its location and the optional pin and registry. */
export interface BuildScope {
	buildLocation: BuildLocation;
	buildServerId?: string | null | undefined;
	registryId?: string | null | undefined;
}

export interface EffectiveBuild {
	location: TeamBuildLocation;
	/** The level the location came from. */
	locationFrom: "service" | "project" | "team";
	buildServerId: string | null;
	registryId: string | null;
	registryFrom: "service" | "project" | "team" | null;
}

const blank = (value: string | null | undefined): string | null =>
	value === undefined || value === null || value === "" ? null : value;

/**
 * Reads service, then project, then team, as the control plane does: the
 * first level that is not `inherit` decides the location; the pin comes from
 * the service, then the project; the registry from the first level that names
 * one. The server stays the authority; this only lets a form say what an
 * "Inherit" choice currently means.
 */
export function resolveBuild(chain: {
	service?: BuildScope | null;
	project?: BuildScope | null;
	team: { buildLocation: TeamBuildLocation; registryId?: string | null };
}): EffectiveBuild {
	const { service, project, team } = chain;
	let location: TeamBuildLocation = team.buildLocation;
	let locationFrom: EffectiveBuild["locationFrom"] = "team";
	if (service && service.buildLocation !== "inherit") {
		location = service.buildLocation;
		locationFrom = "service";
	} else if (project && project.buildLocation !== "inherit") {
		location = project.buildLocation;
		locationFrom = "project";
	}
	const registry: [EffectiveBuild["registryFrom"], string | null][] = [
		["service", blank(service?.registryId)],
		["project", blank(project?.registryId)],
		["team", blank(team.registryId)],
	];
	const found = registry.find(([, id]) => id !== null);
	return {
		location,
		locationFrom,
		buildServerId:
			blank(service?.buildServerId) ?? blank(project?.buildServerId),
		registryId: found?.[1] ?? null,
		registryFrom: found?.[0] ?? null,
	};
}

export const LOCATION_LABEL: Record<BuildLocation, string> = {
	inherit: "Inherit",
	target: "On the app's server",
	build_server: "On a build server",
};

/** The segmented options; a team has nothing to inherit from. */
export function locationOptions(
	scope: "team" | "project" | "service",
): { value: BuildLocation; label: string }[] {
	const all: BuildLocation[] = ["inherit", "target", "build_server"];
	return all
		.filter((value) => scope !== "team" || value !== "inherit")
		.map((value) => ({ value, label: LOCATION_LABEL[value] }));
}

/** The sentence under a location control: what the choice means. */
export function locationConsequence(
	location: BuildLocation,
	inherited?: EffectiveBuild,
): string {
	switch (location) {
		case "target":
			return "The image is built on the server that runs the app and used from there. No registry is needed.";
		case "build_server":
			return "The image is built on a build server, pushed to a registry, and pulled by digest on the app's server. If no build server or registry is usable, the build falls back to the app's server and the deployment says so.";
		case "inherit": {
			if (!inherited) return "Uses the setting one level up.";
			const where =
				inherited.location === "build_server"
					? "on a build server"
					: "on the app's server";
			return `Uses the ${inherited.locationFrom} setting: builds run ${where}.`;
		}
	}
}

// ------------------------------------------------------------- plan placement

/** A warning on a placement. `code` is one of the known kinds or the server's own. */
export interface PlacementWarning {
	code: string;
	message: string;
}

/** What a plan says about where an image is built and where it goes. */
export interface PlacementView {
	location: TeamBuildLocation;
	buildServerId: string | null;
	buildServerName: string | null;
	registryId: string | null;
	/** `host/namespace`, or the bare host. */
	registryHost: string | null;
	/** The platform the image is built for, such as `linux/arm64`. */
	platform: string;
	/** The control plane's reason: "pinned", "sticky", "fallback: …". */
	reason: string;
	sticky: boolean;
	warnings: PlacementWarning[];
}

export type WarningKind = "fallback" | "emulation" | "cold_cache" | "other";

/** The kind of a warning: its code when known, else read from the words. */
export function warningKind(warning: PlacementWarning): WarningKind {
	const code = warning.code.toLowerCase();
	if (code === "fallback" || code === "emulation" || code === "cold_cache") {
		return code;
	}
	const text = warning.message.toLowerCase();
	if (/\bfall(s|ing)? ?back\b|no build server|no registry/.test(text)) {
		return "fallback";
	}
	if (/emulat|qemu/.test(text)) return "emulation";
	if (/cold|empty cache|no cache|first build/.test(text)) return "cold_cache";
	return "other";
}

/** The label that heads each kind of warning in the plan. */
export const WARNING_TITLE: Record<WarningKind, string> = {
	fallback: "Falls back to the app's server",
	emulation: "Cross-architecture build",
	cold_cache: "Cold build cache",
	other: "Heads up",
};

/** A fallback placement: the control plane chose the target although a build server was asked for. */
export function isFallback(placement: PlacementView): boolean {
	return (
		placement.reason.toLowerCase().startsWith("fallback") ||
		placement.warnings.some((warning) => warningKind(warning) === "fallback")
	);
}

/** "(sticky)", "(pinned)", "(fallback)" or "": how the server was chosen. */
function how(placement: PlacementView): string {
	const reason = placement.reason.toLowerCase();
	if (isFallback(placement)) return " (fallback)";
	if (reason.startsWith("pinned")) return " (pinned)";
	if (placement.sticky || reason.startsWith("sticky")) return " (sticky)";
	return "";
}

/**
 * The one line a plan shows: "Builds on build-01 (sticky), pushes to
 * ghcr.io/acme, deploys to web-02". A build on the target reads "Builds and
 * deploys on web-02", with "(fallback)" when a build server was wanted.
 */
export function placementSummary(
	placement: PlacementView,
	target: { name: string },
): string {
	if (placement.location === "target" || placement.buildServerName === null) {
		return isFallback(placement)
			? `Builds and deploys on ${target.name} (fallback)`
			: `Builds and deploys on ${target.name}`;
	}
	const parts = [`Builds on ${placement.buildServerName}${how(placement)}`];
	if (placement.registryHost) parts.push(`pushes to ${placement.registryHost}`);
	parts.push(`deploys to ${target.name}`);
	return parts.join(", ");
}

/** The wire shape `toPlacementView` reads: the plan's `placement` object. */
export interface PlacementWire {
	location: string;
	buildServerId?: string | null | undefined;
	buildServerName?: string | null | undefined;
	registryId?: string | null | undefined;
	registryHost?: string | null | undefined;
	platform?: string | undefined;
	reason?: string | undefined;
	sticky?: boolean | undefined;
	warnings?: readonly { code: string; message: string }[] | null | undefined;
}

/** The plan's placement as the UI reads it; null for a plan without one (an image, a route). */
export function toPlacementView(
	wire: PlacementWire | null | undefined,
	/** The plan's plain warnings, used only when the placement carries none. */
	fallbackWarnings: readonly string[] = [],
): PlacementView | null {
	if (
		!wire ||
		(wire.location !== "target" && wire.location !== "build_server")
	) {
		return null;
	}
	const warnings =
		wire.warnings && wire.warnings.length > 0
			? wire.warnings.map(({ code, message }) => ({ code, message }))
			: fallbackWarnings
					.map((message) => ({ code: "", message }))
					.filter((warning) => warningKind(warning) !== "other");
	return {
		location: wire.location,
		buildServerId: blank(wire.buildServerId),
		buildServerName: blank(wire.buildServerName),
		registryId: blank(wire.registryId),
		registryHost: blank(wire.registryHost),
		platform: wire.platform ?? "",
		reason: wire.reason ?? "",
		sticky: wire.sticky === true,
		warnings,
	};
}

// ---------------------------------------------------------------- deployment

/**
 * "Built on build-01, deployed on web-02": the two servers of a deployment
 * that built elsewhere; null when it built where it deployed or the build
 * server is not known.
 */
export function builtOnText(
	buildServerName: string | null,
	targetName: string | null,
): string | null {
	if (!buildServerName || !targetName) return null;
	if (buildServerName === targetName) return null;
	return `Built on ${buildServerName}, deployed on ${targetName}`;
}

/** The timeline step id the control plane gives placement notices. */
export const PLACEMENT_STEP = "placement";

/** An event of the placement step: a fallback or a wait for a build slot, shown as an info alert, not a step. */
export function isPlacementEvent(event: { step: string }): boolean {
	return event.step.toLowerCase() === PLACEMENT_STEP;
}

// ------------------------------------------------------------ service override

/** A service's build override as the settings form holds it: every field is text, "" for none. */
export interface BuildFields {
	buildLocation: BuildLocation;
	buildServerId: string;
	registryId: string;
}

/** The override fields of a service config (`buildLocation`, `buildServerId`, `registryId`). */
export interface BuildConfigLike {
	buildLocation?: string | null | undefined;
	buildServerId?: string | null | undefined;
	registryId?: string | null | undefined;
}

/** The form fields of a config. A missing or unknown location is Inherit, the default. */
export function toBuildFields(config: BuildConfigLike): BuildFields {
	const location = config.buildLocation;
	return {
		buildLocation:
			location === "target" || location === "build_server"
				? location
				: "inherit",
		buildServerId: config.buildServerId ?? "",
		registryId: config.registryId ?? "",
	};
}

/**
 * What the form adds to the PATCH config. Inherit is the default and is left
 * out, and a pin only means something when the location can be a build
 * server, so it is dropped for "On the app's server".
 */
export function buildOverrideConfig(fields: BuildFields): BuildConfigLike {
	const config: { -readonly [K in keyof BuildConfigLike]: string } = {};
	if (fields.buildLocation !== "inherit") {
		config.buildLocation = fields.buildLocation;
	}
	if (fields.buildServerId !== "" && fields.buildLocation !== "target") {
		config.buildServerId = fields.buildServerId;
	}
	if (fields.registryId !== "") config.registryId = fields.registryId;
	return config;
}

/**
 * The plan's plain warnings that the placement readout does not already show,
 * so a fallback is said once, in the placement, and not again beneath it.
 */
export function withoutPlacementWarnings(
	warnings: readonly string[],
	placement: PlacementView | null,
): string[] {
	if (placement === null) return [...warnings];
	const shown = new Set(placement.warnings.map((warning) => warning.message));
	return warnings.filter((message) => !shown.has(message));
}

/** The heading of a placement notice in the timeline, from the event's own words. */
export function placementNoticeTitle(message: string): string {
	if (/waiting for a build slot/i.test(message))
		return "Waiting for a build slot";
	if (warningKind({ code: "", message }) === "fallback") {
		return "Built on the app's server instead";
	}
	return "Build placement";
}
