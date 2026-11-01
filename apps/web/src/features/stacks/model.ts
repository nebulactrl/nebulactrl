import type { StatusKind } from "#/components/ui/status-icon";

// The pure side of Compose stacks: how a stack's components, the allow-list
// violations of a compose file and the states of its containers read in the
// UI. The components never come from the client: the control plane derives
// them into `config.stack.components` and the agent reports one replica per
// component (`index` = the component's index, `component` = its name).

/** Shown under a restricted violation while the server is not unrestricted. */
export const UNRESTRICTED_HINT =
	"An instance admin can enable Unrestricted for this server";

/** What a stack's compose file declares: one entry per compose service. */
export interface StackComponent {
	name: string;
	/** The component's position (sorted by name): the replica index everywhere. */
	index: number;
	image?: string | undefined;
	ports?: readonly number[] | null | undefined;
	profiles?: readonly string[] | null | undefined;
}

export function isStack(service: { kind: string }): boolean {
	return service.kind === "stack";
}

/** The components of a stack service, by index; none for any other service. */
export function stackComponents(service: {
	kind: string;
	config: {
		stack?: { components?: readonly StackComponent[] | null | undefined };
	};
}): readonly StackComponent[] {
	if (!isStack(service)) return NO_COMPONENTS;
	return service.config.stack?.components ?? NO_COMPONENTS;
}

const NO_COMPONENTS: readonly StackComponent[] = [];

/** A component's name from its replica/metrics index, or undefined when unknown. */
export function componentName(
	components: readonly StackComponent[],
	index: number,
): string | undefined {
	return components.find((component) => component.index === index)?.name;
}

/** "web", or "#2" when the index no longer names a component. */
export function componentLabel(
	components: readonly StackComponent[],
	index: number,
): string {
	return componentName(components, index) ?? `#${index}`;
}

/** The label of a replica of any service: a component's name, else "Replica N". */
export function replicaLabel(
	components: readonly StackComponent[],
	index: number,
): string {
	return components.length > 0
		? componentLabel(components, index)
		: `Replica ${index}`;
}

// --- the live check ---------------------------------------------------------

export interface StackViolation {
	/** The compose service the problem is in; empty for the file as a whole. */
	service?: string | undefined;
	key: string;
	message: string;
	/** `unsupported` always blocks; `restricted` blocks unless the server is unrestricted. */
	kind: string;
}

/** What `POST /environments/{id}/stacks/check` answers. */
export interface StackCheckResult {
	components: readonly StackComponent[];
	violations: readonly StackViolation[];
	notes: readonly string[];
	/** The chosen server is Unrestricted, so restricted problems do not block. */
	unrestricted: boolean;
	/** Whether the server has the Docker Compose plugin; null without a server or while unknown. */
	composeAvailable: boolean | null;
}

/**
 * Where the live check of a compose text stands. `result` is the last answer
 * that arrived and stays on screen while the next one is on its way, so the
 * list does not flash on every keystroke.
 */
export interface StackCheckState {
	/** The text is empty: nothing to check. */
	empty: boolean;
	/** A newer text is waiting for its answer (debounce or request). */
	pending: boolean;
	result: StackCheckResult | null;
	/** The check itself failed (a parse error comes back as violations or a 422). */
	error: string | null;
}

export interface ViolationGroup {
	/** The compose service name; "" for violations of the file itself. */
	service: string;
	unsupported: StackViolation[];
	restricted: StackViolation[];
}

/** Anything that is not clearly `restricted` is treated as `unsupported`: the stricter reading. */
function isRestricted(violation: Pick<StackViolation, "kind">): boolean {
	return violation.kind === "restricted";
}

/**
 * Violations grouped by compose service: the file-level group first, then
 * services by name; inside a group `unsupported` problems come apart from
 * `restricted` ones, each keeping the order the check returned them in.
 */
export function groupViolations(
	violations: readonly StackViolation[] | null | undefined,
): ViolationGroup[] {
	const byService = new Map<string, ViolationGroup>();
	for (const violation of violations ?? []) {
		const service = violation.service ?? "";
		let group = byService.get(service);
		if (!group) {
			group = { service, unsupported: [], restricted: [] };
			byService.set(service, group);
		}
		(isRestricted(violation) ? group.restricted : group.unsupported).push(
			violation,
		);
	}
	return [...byService.values()].sort((a, b) =>
		a.service === b.service
			? 0
			: a.service === ""
				? -1
				: b.service === ""
					? 1
					: a.service.localeCompare(b.service),
	);
}

export interface CheckVerdict {
	/** The compose file cannot be saved or deployed as it is. */
	blocked: boolean;
	unsupported: number;
	/** Restricted problems that still block (none once the server is unrestricted). */
	restricted: number;
	/** Restricted problems exist and only an instance admin can lift them. */
	needsAdmin: boolean;
}

export function checkVerdict(input: {
	violations: readonly StackViolation[] | null | undefined;
	unrestricted: boolean;
}): CheckVerdict {
	const list = input.violations ?? [];
	const restricted = input.unrestricted ? 0 : list.filter(isRestricted).length;
	const unsupported = list.filter((v) => !isRestricted(v)).length;
	return {
		blocked: unsupported + restricted > 0,
		unsupported,
		restricted,
		needsAdmin: restricted > 0,
	};
}

/** "2 problems" style summary line of a verdict. */
export function verdictText(verdict: CheckVerdict): string {
	const total = verdict.unsupported + verdict.restricted;
	if (total === 0) return "The Compose file passes the checks.";
	const parts: string[] = [];
	if (verdict.unsupported > 0) {
		parts.push(`${verdict.unsupported} not supported`);
	}
	if (verdict.restricted > 0) {
		parts.push(`${verdict.restricted} restricted on this server`);
	}
	return `${total} ${total === 1 ? "problem" : "problems"}: ${parts.join(", ")}.`;
}

/**
 * Why a compose text cannot be saved or deployed yet, or null when its live
 * check passed. A text still waiting for its answer blocks too, so a save
 * never races the check.
 */
export function checkBlocker(state: StackCheckState): string | null {
	if (state.empty) return "Add a Compose file.";
	if (state.error) return state.error;
	if (state.pending || !state.result) return "Checking the Compose file…";
	const verdict = checkVerdict({
		violations: state.result.violations,
		unrestricted: state.result.unrestricted,
	});
	if (verdict.blocked) return verdictText(verdict);
	if (state.result.components.length === 0) {
		return "The Compose file declares no services.";
	}
	return null;
}

// --- component states -------------------------------------------------------

/** What the agent reports of one component container. */
export interface ComponentReplica {
	index: number;
	component?: string | undefined;
	state: string;
	health: string;
	restartCount: number;
	startedAt?: string | null | undefined;
}

export interface ComponentState {
	kind: StatusKind;
	label: string;
}

/**
 * One component's state in words and a status icon. A component that exited
 * with status 0 is "Completed" (the agent reports exited + healthy for it);
 * a component the agent has not reported is "Not started".
 */
export function componentState(
	replica: Pick<ComponentReplica, "state" | "health"> | undefined,
): ComponentState {
	if (!replica) return { kind: "pending", label: "Not started" };
	switch (replica.state) {
		case "running":
			switch (replica.health) {
				case "healthy":
					return { kind: "healthy", label: "Healthy" };
				case "unhealthy":
					return { kind: "failed", label: "Unhealthy" };
				case "starting":
					return { kind: "building", label: "Starting" };
				default:
					return { kind: "live", label: "Running" };
			}
		case "restarting":
			return { kind: "degraded", label: "Restarting" };
		case "exited":
			return replica.health === "healthy"
				? { kind: "healthy", label: "Completed" }
				: { kind: "failed", label: "Exited" };
		case "created":
			return { kind: "building", label: "Starting" };
		case "missing":
			return { kind: "failed", label: "Missing" };
		default:
			return { kind: "unknown", label: replica.state || "Unknown" };
	}
}

/** The replica of a component: by name when the agent sends one, else by index. */
function replicaOf(
	component: StackComponent,
	replicas: readonly ComponentReplica[],
): ComponentReplica | undefined {
	return (
		replicas.find((replica) => replica.component === component.name) ??
		replicas.find(
			(replica) => !replica.component && replica.index === component.index,
		)
	);
}

/** "sha256:ab12cd34ef56" from "repo@sha256:ab12…", or null when there is no digest. */
export function shortDigest(ref: string | undefined): string | null {
	if (!ref) return null;
	const at = ref.indexOf("@");
	const digest = at === -1 ? ref : ref.slice(at + 1);
	const match = /^([a-z0-9]+):([a-f0-9]{8,})$/i.exec(digest);
	if (!match) return null;
	return `${match[1]}:${(match[2] ?? "").slice(0, 12)}`;
}

export interface ComponentUsage {
	index: number;
	cpuPercent: number;
	memBytes: number;
}

export interface ComponentRow {
	name: string;
	index: number;
	image: string;
	ports: readonly number[];
	state: ComponentState;
	/** The container runs, so a shell or the file browser can attach. */
	running: boolean;
	restarts: number;
	startedAt: string | null;
	/** The image digest the release runs, "sha256:…" shortened; null before a release. */
	digest: string | null;
	/** CPU in percent of one core from the newest metrics point; null without one. */
	cpuPercent: number | null;
	memBytes: number | null;
}

/**
 * The Components table of the Overview: the declared components in index
 * order, each joined to its replica, its newest usage sample (by replica
 * index) and the digest its release runs (by name). A replica of a component
 * the configuration no longer declares still gets a row, so nothing the agent
 * reports is hidden.
 */
export function componentRows(input: {
	components: readonly StackComponent[];
	replicas: readonly ComponentReplica[];
	usage: readonly ComponentUsage[];
	/** `ReleaseSource.components`: component -> `repo@sha256:…`. */
	digests: Readonly<Record<string, string>> | null | undefined;
}): ComponentRow[] {
	const known = new Set<number>();
	const rows: ComponentRow[] = [];
	const build = (
		component: StackComponent,
		replica: ComponentReplica | undefined,
	): ComponentRow => {
		const sample = input.usage.find((u) => u.index === component.index);
		return {
			name: component.name,
			index: component.index,
			image: component.image ?? "",
			ports: component.ports ?? [],
			state: componentState(replica),
			running: replica?.state === "running",
			restarts: replica?.restartCount ?? 0,
			startedAt: replica?.startedAt ?? null,
			digest: shortDigest(input.digests?.[component.name]),
			cpuPercent: replica && sample ? sample.cpuPercent : null,
			memBytes: replica && sample ? sample.memBytes : null,
		};
	};
	for (const component of [...input.components].sort(
		(a, b) => a.index - b.index,
	)) {
		const replica = replicaOf(component, input.replicas);
		if (replica) known.add(replica.index);
		rows.push(build(component, replica));
	}
	for (const replica of input.replicas) {
		if (known.has(replica.index)) continue;
		if (rows.some((row) => row.name === replica.component)) continue;
		rows.push(
			build(
				{
					name: replica.component || `#${replica.index}`,
					index: replica.index,
				},
				replica,
			),
		);
	}
	return rows;
}

// --- the canvas card --------------------------------------------------------

/** The most component rows a stack's card lists before "+N". */
export const NODE_COMPONENT_LIMIT = 8;

interface NodeComponent {
	name: string;
	state: ComponentState;
	/** The first public hostname that routes to the component, else its first port (":8080"); null with neither. */
	route: string | null;
	restarts: number;
}

export interface NodeSummary {
	shown: NodeComponent[];
	/** Components beyond the limit: the card says "+N". */
	more: number;
	total: number;
}

/** What the card reads of a stack service. */
export interface StackNodeSource {
	kind: string;
	config: {
		stack?: { components?: readonly StackComponent[] | null | undefined };
	};
	observed: { replicas?: readonly ComponentReplica[] | null } | null;
	domains?: readonly { hostname: string; component?: string | undefined }[];
}

/**
 * The component rows of a stack's card: every component in index order with
 * its status icon, route and restarts. Past `limit` rows the card shows the
 * first `limit` and "+N"; then failed and degraded components lead, so a
 * problem is never behind "+N".
 */
export function nodeSummary(
	service: StackNodeSource,
	limit = NODE_COMPONENT_LIMIT,
): NodeSummary {
	const components = stackComponents(service);
	const replicas = service.observed?.replicas ?? [];
	const all: NodeComponent[] = [...components]
		.sort((a, b) => a.index - b.index)
		.map((component) => {
			const replica = replicaOf(component, replicas);
			const domain = service.domains?.find(
				(candidate) => candidate.component === component.name,
			);
			const port = component.ports?.[0];
			return {
				name: component.name,
				state: componentState(replica),
				route: domain?.hostname ?? (port === undefined ? null : `:${port}`),
				restarts: replica?.restartCount ?? 0,
			};
		});
	const trouble = (item: NodeComponent) =>
		item.state.kind === "failed" || item.state.kind === "degraded" ? 0 : 1;
	const ordered =
		all.length > limit ? [...all].sort((a, b) => trouble(a) - trouble(b)) : all;
	return {
		shown: ordered.slice(0, limit),
		more: Math.max(0, ordered.length - limit),
		total: ordered.length,
	};
}

/** A stack card is wider than a service card so names and hostnames fit. */
export const STACK_NODE_WIDTH = 336;
const STACK_HEADER_HEIGHT = 52;
const STACK_ROW_HEIGHT = 28;
const STACK_FOOTER_HEIGHT = 44;

/**
 * The height the card takes for a summary: header, one row per shown
 * component (at least one, for the empty line), the "+N" row when there is one
 * and the footer. The layout uses it so arranging never overlaps a stack.
 */
export function stackNodeSize(summary: Pick<NodeSummary, "shown" | "more">): {
	w: number;
	h: number;
} {
	const rows = Math.max(1, summary.shown.length) + (summary.more > 0 ? 1 : 0);
	return {
		w: STACK_NODE_WIDTH,
		h: STACK_HEADER_HEIGHT + rows * STACK_ROW_HEIGHT + STACK_FOOTER_HEIGHT,
	};
}

/** "3 components", "1 component". */
export function componentCountText(count: number): string {
	return `${count} ${count === 1 ? "component" : "components"}`;
}

// --- per-component series ---------------------------------------------------

/** A component's resource limits as the form holds them: text, empty = none. */
interface ResourcesDraft {
	memoryMb: string;
	cpus: string;
}

export type StackResourcesDraft = Record<string, ResourcesDraft>;

/** What the config stores per component: a limit left out is no limit. */
export type StackResourcesConfig = Record<
	string,
	{ memoryMb?: number; cpus?: number }
>;

export function toResourcesDraft(
	resources: StackResourcesConfig | null | undefined,
): StackResourcesDraft {
	const draft: StackResourcesDraft = {};
	for (const [name, limits] of Object.entries(resources ?? {})) {
		draft[name] = {
			memoryMb: limits.memoryMb ? String(limits.memoryMb) : "",
			cpus: limits.cpus ? String(limits.cpus) : "",
		};
	}
	return draft;
}

/** The config's `resources`: components with no limit are left out; empty when none has one. */
export function buildResources(
	draft: StackResourcesDraft,
): StackResourcesConfig {
	const out: StackResourcesConfig = {};
	for (const name of Object.keys(draft).sort()) {
		const limits = draft[name];
		if (!limits) continue;
		const memoryMb = limits.memoryMb.trim();
		const cpus = limits.cpus.trim();
		if (memoryMb === "" && cpus === "") continue;
		out[name] = {
			...(memoryMb === "" ? {} : { memoryMb: Number.parseInt(memoryMb, 10) }),
			...(cpus === "" ? {} : { cpus: Number(cpus) }),
		};
	}
	return out;
}

/** What is wrong with a component's limits, by field: memory 6 MiB–1 TiB, CPUs 0.01–256. */
export function resourceErrors(
	draft: StackResourcesDraft,
): Record<string, { memoryMb?: string; cpus?: string }> {
	const errors: Record<string, { memoryMb?: string; cpus?: string }> = {};
	for (const [name, limits] of Object.entries(draft)) {
		const problem: { memoryMb?: string; cpus?: string } = {};
		const memory = limits.memoryMb.trim();
		if (memory !== "") {
			const value = Number(memory);
			if (!Number.isInteger(value) || value < 6 || value > 1_048_576) {
				problem.memoryMb =
					"Memory must be a whole number from 6 to 1048576 MB.";
			}
		}
		const cpus = limits.cpus.trim();
		if (cpus !== "") {
			const value = Number(cpus);
			if (!Number.isFinite(value) || value <= 0 || value > 256) {
				problem.cpus = "CPUs must be a number between 0.01 and 256.";
			}
		}
		if (problem.memoryMb || problem.cpus) errors[name] = problem;
	}
	return errors;
}

/** The wait timeout of `up --wait` in seconds: 1 to 3600, empty for the default (300). */
export function waitSecondsError(value: string): string | undefined {
	if (value.trim() === "") return undefined;
	const seconds = Number(value);
	if (!Number.isInteger(seconds) || seconds < 1 || seconds > 3600) {
		return "Wait timeout must be a whole number of seconds from 1 to 3600.";
	}
	return undefined;
}

/**
 * Resource drafts for the components a compose file declares: what was
 * edited stays, components without an entry start empty, and entries of
 * components that are gone are dropped (the control plane refuses limits for
 * a component the file does not have).
 */
export function alignResources(
	draft: StackResourcesDraft,
	components: readonly Pick<StackComponent, "name">[],
): StackResourcesDraft {
	const out: StackResourcesDraft = {};
	for (const { name } of components) {
		out[name] = draft[name] ?? { memoryMb: "", cpus: "" };
	}
	return out;
}

// --- domains ----------------------------------------------------------------

/**
 * The port a domain on this component points at when the form leaves it
 * empty: the only declared port, or undefined when there is none or several
 * (the form then asks).
 */
export function defaultDomainPort(
	component: Pick<StackComponent, "ports"> | undefined,
): number | undefined {
	const ports = component?.ports ?? [];
	return ports.length === 1 ? ports[0] : undefined;
}

/** "web:8080", the component and port a domain targets. */
export function domainTarget(domain: {
	component?: string | undefined;
	targetPort: number;
}): string | null {
	if (!domain.component) return null;
	return domain.targetPort > 0
		? `${domain.component}:${domain.targetPort}`
		: domain.component;
}

/** Why the Add domain button is off for a stack, or null when it can be used. */
export function domainBlocker(input: {
	hostname: string;
	component: string;
	port: string;
	components: readonly StackComponent[];
}): string | null {
	if (input.components.length === 0) return "The stack has no components yet.";
	if (input.component === "") return "Pick the component the domain serves.";
	const chosen = input.components.find((c) => c.name === input.component);
	if (!chosen) return "That component is not in the compose file.";
	if (input.port.trim() === "") {
		return defaultDomainPort(chosen) === undefined
			? `${chosen.name} declares ${(chosen.ports ?? []).length === 0 ? "no port" : "several ports"}: enter the port.`
			: null;
	}
	const port = Number(input.port);
	if (!Number.isInteger(port) || port < 1 || port > 65535) {
		return "The port must be a number from 1 to 65535.";
	}
	return null;
}
