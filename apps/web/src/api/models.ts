// The app's view of the control plane's resources.
//
// Every type here is derived from the generated OpenAPI types (src/api/gen),
// tightened where the schema is looser than the wire format: Go pointers and
// slices are rendered as non-null objects, and string enums arrive as plain
// `string`. The `to*` functions are the single boundary where a generated
// response becomes one of these; they take the generated type as input, so a
// contract change breaks the build here instead of deep in a component.

import {
	type PlacementView,
	toPlacementView,
} from "#/features/build-placement/model";
import type { DomainCert } from "#/features/dns01/queries";
import { type MeshPlan, toMeshPlan } from "#/features/mesh/model";
import type * as Gen from "./gen/types.gen";

export type ServiceKind = Gen.ServiceView["kind"];
type ServiceSourceType = Gen.ServiceView["sourceType"];
export type ServerStatus = Gen.Server["status"];
type DeploymentStrategy = Gen.DeploymentView["strategy"];
type DeploymentTrigger = Gen.DeploymentView["trigger"];
export type HealthcheckType = NonNullable<Gen.HealthcheckInput["type"]>;

const DEPLOYMENT_STATUSES = [
	"created",
	"queued",
	"preparing",
	"building",
	"releasing",
	"verifying",
	"succeeded",
	"failed",
	"canceled",
	"rolled_back",
	"rollback_failed",
] as const;
export type DeploymentStatus = (typeof DEPLOYMENT_STATUSES)[number];

const STATUS_SET: ReadonlySet<string> = new Set(DEPLOYMENT_STATUSES);
export function asDeploymentStatus(value: string): DeploymentStatus {
	return STATUS_SET.has(value) ? (value as DeploymentStatus) : "failed";
}

type Healthcheck = Omit<Gen.Healthcheck, "type"> & {
	type: HealthcheckType;
};

/** One additional process of a service, as stored in its config (the default process is the service's own fields). */
export type ProcessConfig = Omit<
	Gen.ProcessConfig,
	"healthcheck" | "startCommand"
> & {
	healthcheck: Healthcheck;
	startCommand: string[];
};

type ServiceConfig = Omit<
	Gen.Config,
	"healthcheck" | "startCommand" | "processes"
> & {
	healthcheck: Healthcheck;
	startCommand?: string[];
	/** The additional processes, by name; empty for a single-process service. */
	processes: ProcessConfig[];
};

interface ServiceServerRef {
	id: string;
	name: string;
	status: ServerStatus;
	lastSeenAt: string | null;
}

export interface LatestDeployment {
	id: string;
	status: DeploymentStatus;
	trigger: string;
	strategy: DeploymentStrategy;
	statusMessage: string | null;
	createdAt: string;
	finishedAt: string | null;
	releaseId: string | null;
	/** The note given when the deployment was requested. */
	message: string | null;
	/** The step a failed deployment stopped at (build, release, verify). */
	failedStep: string | null;
	/** The agent step in progress while in flight (`predeploy`, `build`, …). */
	currentStep: string | null;
}

export type ReleaseSource = Gen.ReleaseSource;

interface CurrentRelease {
	id: string;
	imageRef: string;
	source: ReleaseSource;
}

type ObservedState = Gen.ObservedView;

/** What the agent reports of one process: its containers aggregated. */
export type ObservedProcess = Gen.ObservedProcessView;

/** One process of a service (the default one first), with its own scaling and state. */
type ProcessView = Omit<Gen.ProcessView, "command" | "scaling" | "observed"> & {
	command: string[];
	scaling: Scaling;
	/** Null until the agent has reported the process. */
	observed: ObservedProcess | null;
};

/** The proxy rules of a domain beyond its hostname (kind, path prefix, TLS, options). */
export interface DomainRule {
	pathPrefix: string;
	stripPrefix: boolean;
	addPrefix: string;
	kind: "serve" | "redirect";
	redirectTo: string;
	redirectCode: 301 | 302 | 307 | 308;
	preservePath: boolean;
	tlsMode: "auto" | "custom" | "none";
	/** The pinned custom certificate, when there is one. */
	certificateId: string | null;
	options: Gen.RouteOptions;
	/** Basic-auth user names; the hashes never leave the control plane. */
	authUsers: string[];
}

/** A plain "serve everything over automatic TLS" domain. */
export const PLAIN_DOMAIN_RULE: DomainRule = {
	pathPrefix: "",
	stripPrefix: false,
	addPrefix: "",
	kind: "serve",
	redirectTo: "",
	redirectCode: 308,
	preservePath: true,
	tlsMode: "auto",
	certificateId: null,
	options: {},
	authUsers: [],
};

export interface ServiceDomain extends DomainRule {
	id: string;
	hostname: string;
	targetPort: number;
	/** The compose service a stack's domain routes to; "" for every other service. */
	component: string;
	/** The process an app's domain routes to; "" for the default process and for every other service. */
	process: string;
	generated: boolean;
	status: "pending" | "active" | "error";
	statusMessage: string | null;
	dnsOk: boolean | null;
	dnsCheckedAt: string | null;
	/** When the domain was added (a new name takes a while to reach every DNS resolver). */
	createdAt?: string | null;
	/** The DNS-01 provider that obtains the certificate ("" = HTTP-01 or a covering wildcard). */
	dnsResolver?: string;
	/** Where the certificate comes from, with its observed status and expiry. */
	cert?: DomainCert | null;
	/** The cached Cloudflare DNS state; null while the team has no Cloudflare connection. */
	dns?: Gen.DomainDns | null;
	/** The hostname a www redirect of this domain answers for ("" when it cannot have one). */
	wwwPartner?: string;
	/** The www/apex counterpart exists as a whole-host redirect to this domain. */
	wwwRedirect?: boolean;
	/** For the redirect domain of a www redirect: the domain it redirects to; "" otherwise. */
	wwwRedirectFor?: string;
}

export interface Service {
	id: string;
	environmentId: string;
	projectId: string;
	name: string;
	slug: string;
	kind: ServiceKind;
	sourceType: ServiceSourceType;
	config: ServiceConfig;
	serverId: string | null;
	server: ServiceServerRef | null;
	generation: number;
	appliedGeneration: number;
	hasStagedChanges: boolean;
	currentDeploymentId: string | null;
	latestDeployment: LatestDeployment | null;
	currentRelease: CurrentRelease | null;
	observed: ObservedState | null;
	observedAt: string | null;
	stale: boolean;
	domains: ServiceDomain[];
	privateHost: string;
	gitTokenSet: boolean;
	registryAuthSet: boolean;
	createdAt: string;
	updatedAt: string;
	/** `stopped` after a Stop, until the next deploy or restart. */
	desiredState: "running" | "stopped";
	volumes: Gen.VolumeRef[];
	scaling: Scaling;
	/** The processes of a service that runs several, the default one first; empty otherwise. */
	processes: ProcessView[];
	/** Staged changes, split by what they touch. */
	pendingChangeCounts: Gen.PendingChangeCounts;
	/** `new` (never deployed) or `edited` (deployed, with staged changes). */
	stagedKind: "new" | "edited" | null;
	/** Maintenance mode: while enabled the proxy answers 503 with a maintenance page. */
	maintenance: Gen.MaintenanceView;
	/** The repository link of a git service (auto-deploy, watch paths); null when unlinked. */
	git: Gen.GitRef | null;
	/** The service's tailnet node as the last deployment left it; null until one was provisioned. */
	tailnet: Gen.TailnetView | null;
	/** The team shared networks the service is attached to (staged until deployed), with its hostname on each. */
	networks: Gen.NetworkRef[];
}

export type ServiceLink = Gen.LinkView;

export interface Server {
	id: string;
	name: string;
	status: ServerStatus;
	publicIp: string | null;
	baseDomain: string | null;
	acmeEmail: string | null;
	agentVersion: string | null;
	protocolVersion: number | null;
	facts: Gen.Facts | null;
	lastSeenAt: string | null;
	createdAt: string;
	proxy: {
		status: string;
		error?: string | undefined;
		/** The agent speaks proxy v2 (domain options, external routes, TCP/UDP, certificates, tailnet). */
		v2: boolean;
		/** Staged server-level proxy changes: generation ahead of appliedGeneration. */
		generation: number;
		appliedGeneration: number;
		tailnet: Gen.ServerTailnetSummary | null;
	};
	latest: Gen.Latest | null;
	serviceCount: number;
	/** Evacuation state; draining and drained servers accept no new services. */
	drainState: Gen.Server["drainState"];
	drainMessage: string;
	/** Whether services may be scheduled here. Off keeps what runs and refuses new placements and moves. */
	acceptWorkloads: boolean;
	/** Runs builds for projects that build on a build server (the server's role is Builds or Both). */
	buildEnabled: boolean;
	/** Builds that run at once; more wait in a queue. */
	buildMaxConcurrent: number;
	/** May build for another CPU architecture under emulation. */
	buildAllowEmulation: boolean;
	/** May build pull-request previews, which run untrusted Dockerfiles. */
	buildAllowPreviews: boolean;
	/** The host the installer put the control plane on; its proxy serves the dashboard. */
	controlPlane: boolean;
	/** The Cloudflare Tunnel of the server; null for a payload without one. */
	tunnel: Gen.ServerTunnelSummary | null;
	/** An instance admin made this server unrestricted: stacks and services here may use privileged mode, host devices, GPUs and the other host-access options. */
	unrestricted: boolean;
	/** The Docker Compose plugin version the agent reported; empty when it has none. */
	composeVersion: string;
}

export interface Point {
	x: number;
	y: number;
}

export interface CanvasGroup {
	id: string;
	name: string;
	x: number;
	y: number;
	w: number;
	h: number;
	collapsed: boolean;
	serviceIds: string[];
}

type CanvasViewport = Gen.Viewport;

/** What the canvas persists per environment; never read by deployment code. */
export interface CanvasLayout {
	nodes: Record<string, Point>;
	groups: CanvasGroup[];
	viewport?: CanvasViewport | null;
}

export interface CanvasDocument {
	layout: CanvasLayout;
	version: number;
}

export interface Deployment {
	id: string;
	serviceId: string;
	releaseId: string | null;
	serverId: string | null;
	/** The build server that built the image; null when it was built on the target. */
	buildServerId: string | null;
	trigger: DeploymentTrigger;
	actorType: string;
	/** Who started it, ready to show: a person, a token, "MCP · token" or "System". */
	actorLabel: string;
	status: DeploymentStatus;
	strategy: DeploymentStrategy;
	statusMessage: string | null;
	/** The note given when the deployment was requested. */
	message: string | null;
	rolledBackFrom: string | null;
	generation: number;
	createdAt: string;
	startedAt: string | null;
	finishedAt: string | null;
	release: Gen.ReleaseView | null;
	waitingFor: {
		serverId: string;
		serverName: string;
		offlineSince: string | null;
	} | null;
	/** The git commit the deployment builds (null for images). */
	commit: Gen.CommitView | null;
	/** When someone asked to cancel it while the agent ran it; the agent is stopping it. */
	cancelRequestedAt: string | null;
	/** The deploy step a canceled deployment stopped at; null when it was not canceled. */
	canceledAtStep: string | null;
}

export type VariableScope = Gen.Variable["scope"];
type VariableSource = Gen.Variable["source"];

export interface Variable {
	key: string;
	value: string | undefined;
	isSecret: boolean;
	/** Secret by declaration or because it embeds a secret through a reference. */
	computedSecret: boolean;
	source: VariableSource;
	scope: VariableScope;
	version: number;
	comment: string;
	/** Generator descriptors (`secret(32)`) of a generated variable; never the template text. */
	generated: Gen.GeneratorCall[];
	references: Gen.VariableReference[];
	rotateEveryDays: number | null;
	ageDays: number;
	rotationDue: boolean;
	/** Why it does not resolve right now, when it does not. */
	problem: string | undefined;
	updatedAt: string;
}

export interface PlatformVariable {
	key: string;
	value: string;
	description: string;
	category: Gen.PlatformVar["category"];
	referenceable: boolean;
}

/** A virtual variable a service offers to references, e.g. a database's DATABASE_URL. */
export type ExportVariable = Gen.ExportVar;

export interface PlanResult {
	strategy: DeploymentStrategy;
	reasons: string[];
	warnings: string[];
	changes: Gen.FieldChange[];
	blocking: Gen.PlanBlocking[];
	source: Gen.PlanSource;
	server: Gen.ServerRef | null;
	/** Where an image is built and where it goes; null for an image, a route or a plan without one. */
	placement: PlacementView | null;
	/** What the deploy does to the mesh (links and devices added, blockers); null when no service crosses a server. */
	mesh: MeshPlan | null;
}

const blank = (value: string | null | undefined): string | null =>
	value ? value : null;

export function toService(view: Gen.ServiceView): Service {
	const server = view.server?.id ? view.server : null;
	return {
		id: view.id,
		environmentId: view.environmentId,
		projectId: view.projectId,
		name: view.name,
		slug: view.slug,
		kind: view.kind,
		sourceType: view.sourceType,
		config: {
			...view.config,
			startCommand: view.config.startCommand ?? [],
			healthcheck: {
				...view.config.healthcheck,
				type: view.config.healthcheck.type as HealthcheckType,
			},
			processes: (view.config.processes ?? []).map((process) => ({
				...process,
				startCommand: process.startCommand ?? [],
				healthcheck: {
					...process.healthcheck,
					type: process.healthcheck.type as HealthcheckType,
				},
			})),
		},
		serverId: blank(view.serverId),
		server: server
			? {
					id: server.id,
					name: server.name,
					status: server.status as ServerStatus,
					lastSeenAt: server.lastSeenAt ?? null,
				}
			: null,
		generation: view.generation,
		appliedGeneration: view.appliedGeneration,
		hasStagedChanges: view.hasStagedChanges,
		currentDeploymentId: blank(view.currentDeploymentId),
		latestDeployment: view.latestDeployment?.id
			? {
					id: view.latestDeployment.id,
					status: asDeploymentStatus(view.latestDeployment.status),
					trigger: view.latestDeployment.trigger,
					strategy: view.latestDeployment.strategy as DeploymentStrategy,
					statusMessage: blank(view.latestDeployment.statusMessage),
					createdAt: view.latestDeployment.createdAt,
					finishedAt: view.latestDeployment.finishedAt ?? null,
					releaseId: blank(view.latestDeployment.releaseId),
					message: blank(view.latestDeployment.message),
					failedStep: blank(view.latestDeployment.failedStep),
					currentStep: blank(view.latestDeployment.currentStep),
				}
			: null,
		currentRelease: view.currentRelease?.id ? view.currentRelease : null,
		observed:
			view.observed?.containerId !== undefined && view.observed.state
				? view.observed
				: null,
		observedAt: view.observedAt ?? null,
		stale: view.stale,
		domains: (view.domains ?? []).map(toDomain),
		privateHost: view.privateHost,
		gitTokenSet: view.gitTokenSet,
		registryAuthSet: view.registryAuthSet,
		createdAt: view.createdAt,
		updatedAt: view.updatedAt,
		desiredState: view.desiredState,
		volumes: view.volumes ?? [],
		scaling: toScaling(view.scaling),
		pendingChangeCounts: view.pendingChangeCounts,
		stagedKind:
			view.stagedKind === "new" || view.stagedKind === "edited"
				? view.stagedKind
				: null,
		maintenance: view.maintenance,
		git: view.git?.connectionId ? view.git : null,
		tailnet: view.tailnet?.state ? view.tailnet : null,
		networks: view.networks ?? [],
		processes: (view.processes ?? []).map(toProcess),
	};
}

/** Replica settings and state of a service (`PUT /services/{id}/scaling`). */
export type Scaling = Omit<
	Gen.ScalingView,
	"lastScaleEvent" | "blockedReason" | "ineligibleReason"
> & {
	lastScaleEvent: Gen.ScaleEvent | null;
	blockedReason: string | null;
	ineligibleReason: string | null;
};

function toScaling(view: Gen.ScalingView): Scaling {
	return {
		...view,
		lastScaleEvent: view.lastScaleEvent?.at ? view.lastScaleEvent : null,
		blockedReason: blank(view.blockedReason),
		ineligibleReason: blank(view.ineligibleReason),
	};
}

function toProcess(view: Gen.ProcessView): ProcessView {
	return {
		...view,
		command: view.command ?? [],
		scaling: toScaling(view.scaling),
		observed: view.observed?.name ? view.observed : null,
	};
}

function toDomain(view: Gen.DomainView): ServiceDomain {
	return {
		id: view.id,
		hostname: view.hostname,
		targetPort: view.targetPort,
		component: view.component ?? "",
		process: view.process ?? "",
		generated: view.generated,
		status: view.status as ServiceDomain["status"],
		statusMessage: blank(view.statusMessage),
		dnsOk: view.dnsOk ?? null,
		dnsCheckedAt: view.dnsCheckedAt ?? null,
		createdAt: view.createdAt ?? null,
		dnsResolver: view.dnsResolver ?? "",
		cert: (view.cert as DomainCert | null | undefined) ?? null,
		dns: (view.dns as Gen.DomainDns | null | undefined) ?? null,
		wwwPartner: view.wwwPartner ?? "",
		wwwRedirect: view.wwwRedirect ?? false,
		wwwRedirectFor: view.wwwRedirectFor ?? "",
		pathPrefix: view.pathPrefix ?? "",
		stripPrefix: view.stripPrefix ?? false,
		addPrefix: view.addPrefix ?? "",
		kind: view.kind ?? "serve",
		redirectTo: view.redirectTo ?? "",
		redirectCode: view.redirectCode ?? 308,
		preservePath: view.preservePath ?? true,
		tlsMode: view.tlsMode ?? "auto",
		certificateId: blank(view.certificateId),
		options: view.options ?? {},
		authUsers: view.authUsers ?? [],
	};
}

export function toServer(view: Gen.Server): Server {
	const proxy: Server["proxy"] = {
		status: view.proxy?.status ?? "",
		v2: view.proxy?.v2 ?? false,
		generation: view.proxy?.generation ?? 0,
		appliedGeneration: view.proxy?.appliedGeneration ?? 0,
		tailnet: view.proxy?.tailnet ?? null,
	};
	if (view.proxy?.error) proxy.error = view.proxy.error;
	return {
		id: view.id,
		name: view.name,
		status: view.status,
		publicIp: view.publicIp ?? null,
		baseDomain: view.baseDomain ?? null,
		acmeEmail: view.acmeEmail ?? null,
		agentVersion: view.agentVersion ?? null,
		protocolVersion: view.protocolVersion ?? null,
		facts: view.facts?.os ? view.facts : null,
		lastSeenAt: view.lastSeenAt ?? null,
		createdAt: view.createdAt,
		proxy,
		latest: view.latest?.sampledAt ? view.latest : null,
		serviceCount: view.serviceCount,
		drainState: view.drainState,
		drainMessage: view.drainMessage,
		acceptWorkloads: view.acceptWorkloads,
		buildEnabled: view.buildEnabled,
		buildMaxConcurrent: view.buildMaxConcurrent,
		buildAllowEmulation: view.buildAllowEmulation,
		buildAllowPreviews: view.buildAllowPreviews,
		controlPlane: view.controlPlane,
		tunnel: view.tunnel ?? null,
		unrestricted: view.unrestricted,
		composeVersion: view.composeVersion ?? "",
	};
}

type DeploymentWire = Gen.DeploymentView | Gen.RollbackView;

export function toDeployment(view: DeploymentWire): Deployment {
	return {
		id: view.id,
		serviceId: view.serviceId,
		releaseId: blank(view.releaseId),
		serverId: blank(view.serverId),
		buildServerId: "buildServerId" in view ? blank(view.buildServerId) : null,
		trigger: view.trigger,
		actorType: view.actorType,
		actorLabel: view.actorLabel,
		status: asDeploymentStatus(view.status),
		strategy: view.strategy,
		statusMessage: blank(view.statusMessage),
		message: blank(view.message),
		rolledBackFrom: blank(view.rolledBackFrom),
		generation: view.generation,
		createdAt: view.createdAt,
		startedAt: view.startedAt ?? null,
		finishedAt: view.finishedAt ?? null,
		release: view.release?.id ? view.release : null,
		waitingFor: view.waitingFor?.serverId ? view.waitingFor : null,
		commit: view.commit?.sha ? view.commit : null,
		cancelRequestedAt: view.cancelRequestedAt ?? null,
		canceledAtStep: blank(view.canceledAtStep),
	};
}

export function toVariable(view: Gen.Variable): Variable {
	return {
		key: view.key,
		value: view.isSecret ? undefined : (view.value ?? ""),
		isSecret: view.isSecret,
		computedSecret: view.computedSecret,
		source: view.source,
		scope: view.scope,
		version: view.version,
		comment: view.comment,
		generated: view.generated ?? [],
		references: view.references ?? [],
		rotateEveryDays: view.rotateEveryDays,
		ageDays: view.ageDays,
		rotationDue: view.rotationDue,
		problem: view.problem,
		updatedAt: view.updatedAt,
	};
}

/** A mesh blocker the plan already lists under `blocking` is shown there, once. */
function withoutListedBlockers(
	mesh: MeshPlan | null,
	listed: readonly Gen.PlanBlocking[],
): MeshPlan | null {
	if (!mesh) return null;
	const codes = new Set(listed.map((block) => block.code));
	return {
		...mesh,
		blockers: mesh.blockers.filter((blocker) => !codes.has(blocker.code)),
	};
}

export function toPlan(view: Gen.Plan): PlanResult {
	return {
		strategy: view.strategy,
		reasons: view.reasons ?? [],
		warnings: view.warnings ?? [],
		changes: view.changes ?? [],
		blocking: view.blocking ?? [],
		source: view.source,
		server: view.server?.id ? view.server : null,
		placement: toPlacementView(view.placement, view.warnings ?? []),
		mesh: withoutListedBlockers(toMeshPlan(view.mesh), view.blocking ?? []),
	};
}

export function toCanvasDocument(view: Gen.Canvas): CanvasDocument {
	return {
		version: view.version,
		layout: {
			nodes: view.layout.nodes ?? {},
			groups: (view.layout.groups ?? []).map((group) => ({
				...group,
				serviceIds: group.serviceIds ?? [],
			})),
			viewport: view.layout.viewport ?? null,
		},
	};
}

/** The generated wire shape of a layout (what PUT /canvas accepts). */
export function fromCanvasLayout(layout: CanvasLayout): Gen.Layout {
	return {
		nodes: layout.nodes,
		groups: layout.groups,
		viewport: layout.viewport ?? { x: 0, y: 0, zoom: 1 },
	};
}

/** Production first, then the rest by name: the landing choice for a project. */
export function productionFirst<
	T extends { isProduction: boolean; name: string },
>(environments: readonly T[]): T[] {
	return [...environments].sort((a, b) => {
		if (a.isProduction !== b.isProduction) return a.isProduction ? -1 : 1;
		return a.name.localeCompare(b.name);
	});
}

export type Me = Gen.MeBody;
export type Project = Gen.Project;
export type Environment = Gen.Environment;
