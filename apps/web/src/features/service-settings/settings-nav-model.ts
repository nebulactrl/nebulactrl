import { buildPatch, type SettingsDraft } from "./draft";

/**
 * The model behind the service Settings navigation: which categories a
 * service shows, which sections each holds, which category an edited field, a
 * validation error or a staged change belongs to, and the settings search.
 * Pure: the rail and the tab only draw what this decides.
 */

export const CATEGORY_IDS = [
	"general",
	"deploy",
	"networking",
	"resources",
	"storage",
	"operations",
	"advanced",
	"danger",
] as const;

export type CategoryId = (typeof CATEGORY_IDS)[number];

/**
 * A section's id is the slug of its title, which `Section` also writes into
 * `data-anchor`, so a search result can find it in the page.
 */
export type SectionId =
	| "identity"
	| "source"
	| "build"
	| "deploy"
	| "processes"
	| "deploy-hook"
	| "releases"
	| "networking"
	| "tailnet"
	| "mesh"
	| "health-check"
	| "uptime-monitoring"
	| "scaling"
	| "resources"
	| "docker-options"
	| "placement"
	| "volumes"
	| "files"
	| "tasks"
	| "maintenance"
	| "danger-zone"
	| "upstreams"
	| "target"
	| "port-and-access"
	| "domains"
	| "compose"
	| "stack-deploy"
	| "stack-limits"
	| "stack-networking";

interface SectionRef {
	id: SectionId;
	label: string;
}

export interface Category {
	id: CategoryId;
	label: string;
	/** One line under the category's heading. */
	description: string;
	sections: SectionRef[];
}

/** What decides which categories apply. `oneshot` follows the live draft. */
export interface SettingsSubject {
	kind: string;
	sourceType: string;
	oneshot: boolean;
	/** Only for a route: `http`, `tcp` or `udp`. */
	routeProtocol?: string;
}

const CATEGORY_COPY: Record<
	CategoryId,
	{ label: string; description: string }
> = {
	general: {
		label: "General",
		description: "Name, server, source and how the image is built.",
	},
	deploy: {
		label: "Deploy",
		description: "How a new release starts and goes live.",
	},
	networking: {
		label: "Networking",
		description: "How people and other services reach this service.",
	},
	resources: {
		label: "Resources",
		description: "How many copies run and how much each one can use.",
	},
	storage: {
		label: "Storage",
		description: "Volumes and files written into the container.",
	},
	operations: {
		label: "Operations",
		description: "Uptime checks, scheduled tasks and maintenance mode.",
	},
	advanced: {
		label: "Advanced",
		description: "Container hardening and runtime options.",
	},
	danger: {
		label: "Danger zone",
		description: "Deleting the service.",
	},
};

/** Category ids that older links and bookmarks still carry, and where they land now. */
const CATEGORY_ALIAS: Readonly<Record<string, CategoryId>> = {
	runtime: "resources",
};

const SECTION_LABEL: Record<SectionId, string> = {
	identity: "Identity",
	source: "Source",
	build: "Build",
	deploy: "Commands",
	processes: "Processes",
	"deploy-hook": "Deploy hook",
	releases: "Releases",
	networking: "Networking",
	tailnet: "Tailnet",
	mesh: "Mesh",
	"health-check": "Health check",
	"uptime-monitoring": "Uptime monitoring",
	scaling: "Scaling",
	resources: "Limits",
	"docker-options": "Docker options",
	placement: "Server",
	volumes: "Volumes",
	files: "Files",
	tasks: "Tasks",
	maintenance: "Maintenance",
	"danger-zone": "Danger zone",
	upstreams: "Upstreams",
	target: "Target",
	"port-and-access": "Port and access",
	domains: "Domains",
	compose: "Compose file",
	"stack-deploy": "Deploy",
	"stack-limits": "Component limits",
	"stack-networking": "Networking",
};

function isRoute(
	subject: Pick<SettingsSubject, "kind" | "sourceType">,
): boolean {
	return subject.kind === "route" || subject.sourceType === "external";
}

function sectionsFor(
	subject: SettingsSubject,
): Record<CategoryId, SectionId[]> {
	if (isRoute(subject)) {
		const http =
			subject.routeProtocol !== "tcp" && subject.routeProtocol !== "udp";
		return {
			general: ["identity"],
			deploy: [],
			networking: [
				http ? "upstreams" : "target",
				http ? "domains" : "port-and-access",
			],
			resources: [],
			storage: [],
			operations: [],
			advanced: [],
			danger: ["danger-zone"],
		};
	}
	if (subject.kind === "stack") {
		// The compose file declares the image, ports, health checks, volumes and
		// options; what a stack keeps here is the file, its deploy, limits,
		// server, domains and the danger zone.
		return {
			general: ["identity", "placement", "compose"],
			deploy: ["stack-deploy", "deploy-hook"],
			networking: ["stack-networking"],
			resources: ["stack-limits"],
			storage: [],
			operations: ["uptime-monitoring"],
			advanced: [],
			danger: ["danger-zone"],
		};
	}
	const { oneshot } = subject;
	// Several processes share one build and release, which only a service
	// that runs long-lived containers from git or an image can have.
	const multiProcess =
		!oneshot &&
		(subject.kind === "app" || subject.kind === "worker") &&
		(subject.sourceType === "git" || subject.sourceType === "image");
	return {
		general: [
			"identity",
			"placement",
			"source",
			...(subject.sourceType === "git" ? (["build"] as const) : []),
		],
		deploy: [
			"deploy",
			...(multiProcess ? (["processes"] as const) : []),
			...(oneshot ? [] : (["health-check"] as const)),
			"deploy-hook",
			"releases",
		],
		networking: oneshot ? [] : ["networking", "tailnet", "mesh"],
		resources: ["scaling", "resources"],
		storage: ["volumes", "files"],
		operations: oneshot ? [] : ["uptime-monitoring", "tasks", "maintenance"],
		advanced: ["docker-options"],
		danger: ["danger-zone"],
	};
}

/**
 * The categories a service shows, in rail order. A category with nothing to
 * configure for this kind of service (Storage for a route, Networking for a
 * one-shot job) is left out.
 */
export function categoriesFor(subject: SettingsSubject): Category[] {
	const sections = sectionsFor(subject);
	return CATEGORY_IDS.flatMap((id) =>
		sections[id].length === 0
			? []
			: [
					{
						id,
						...CATEGORY_COPY[id],
						sections: sections[id].map((section) => ({
							id: section,
							label: SECTION_LABEL[section],
						})),
					},
				],
	);
}

/**
 * The category a `?section=` value names: a category id, or the id of a
 * section inside one. Undefined for anything else, or a category this
 * service does not have.
 */
export function resolveCategory(
	value: string | undefined,
	categories: readonly Category[],
): CategoryId | undefined {
	if (value === undefined) return undefined;
	const wanted = Object.hasOwn(CATEGORY_ALIAS, value)
		? CATEGORY_ALIAS[value]
		: value;
	const byId = categories.find((category) => category.id === wanted);
	if (byId) return byId.id;
	return categories.find((category) =>
		category.sections.some((section) => section.id === value),
	)?.id;
}

/** The first category, which is where Settings opens. */
export function defaultCategory(categories: readonly Category[]): CategoryId {
	return categories[0]?.id ?? "general";
}

// --- drafts --------------------------------------------------------------

/** Every draft field belongs to exactly one category; the compiler checks it. */
const FIELD_CATEGORY = {
	name: "general",
	image: "general",
	gitUrl: "general",
	gitBranch: "general",
	dockerfilePath: "general",
	buildContext: "general",
	buildType: "general",
	submodules: "general",
	buildLocation: "general",
	buildServerId: "general",
	registryId: "general",
	processName: "deploy",
	singleton: "deploy",
	processes: "deploy",
	startCommand: "deploy",
	preDeployCommand: "deploy",
	preDeployTimeoutSeconds: "deploy",
	postDeployCommand: "deploy",
	postDeployTimeoutSeconds: "deploy",
	stopGraceSeconds: "deploy",
	imageRetention: "deploy",
	gitToken: "general",
	clearGitToken: "general",
	registryUsername: "general",
	registryPassword: "general",
	clearRegistryAuth: "general",
	oneshot: "general",
	port: "networking",
	blockEgress: "networking",
	tailnet: "networking",
	serverId: "general",
	healthType: "deploy",
	healthCommand: "deploy",
	healthPath: "deploy",
	healthPort: "deploy",
	intervalSeconds: "deploy",
	timeoutSeconds: "deploy",
	startPeriodSeconds: "deploy",
	deadlineSeconds: "deploy",
	successThreshold: "deploy",
	memoryMb: "resources",
	cpus: "resources",
	files: "storage",
	docker: "advanced",
	stackCompose: "general",
	stackWaitSeconds: "deploy",
	stackResources: "resources",
} as const satisfies Record<keyof SettingsDraft, CategoryId>;

/** Every field of the settings draft, as the category map lists them. */
export const DRAFT_KEYS = Object.keys(
	FIELD_CATEGORY,
) as (keyof SettingsDraft)[];

/**
 * Whether the category holds any field the save bar saves. Operations and the
 * Danger zone act at once, so they have no use for it.
 */
export function categoryHasDraftFields(id: CategoryId): boolean {
	return DRAFT_KEYS.some((key) => FIELD_CATEGORY[key] === id);
}

/** The category of a draft field, or of a validation error keyed by one. */
export function categoryOfField(key: string): CategoryId {
	if (Object.hasOwn(FIELD_CATEGORY, key)) {
		return FIELD_CATEGORY[key as keyof SettingsDraft];
	}
	// `tailnet`, `tailnetHostname`, `tailnetTcp`, ...
	return key.startsWith("tailnet") ? "networking" : "general";
}

/**
 * The categories holding unsaved changes. Each is judged like the Save
 * button is: the baseline plus only that category's fields must produce a
 * patch, so a trailing space the patch ignores is not "edited".
 */
export function editedCategories(
	baseline: SettingsDraft,
	draft: SettingsDraft,
	sourceType: Parameters<typeof buildPatch>[2],
): Set<CategoryId> {
	const edited = new Set<CategoryId>();
	for (const id of CATEGORY_IDS) {
		const mixed: SettingsDraft = { ...baseline };
		for (const key of DRAFT_KEYS) {
			if (FIELD_CATEGORY[key] === id) {
				(mixed as unknown as Record<string, unknown>)[key] = draft[key];
			}
		}
		if (buildPatch(baseline, mixed, sourceType) !== null) edited.add(id);
	}
	return edited;
}

/** The categories with something to fix, from the keys `validateDraft` returned. */
export function invalidCategories(
	errorKeys: readonly string[],
): Set<CategoryId> {
	return new Set(errorKeys.map(categoryOfField));
}

/** The first category in rail order that holds an error, to land on after a failed save. */
export function firstInvalidCategory(
	errorKeys: readonly string[],
	categories: readonly Category[],
): CategoryId | undefined {
	const invalid = invalidCategories(errorKeys);
	return categories.find((category) => invalid.has(category.id))?.id;
}

// --- staged changes ------------------------------------------------------

const CONFIG_CATEGORY: Record<string, CategoryId> = {
	port: "networking",
	tailnet: "networking",
	blockEgress: "networking",
	route: "networking",
	startCommand: "deploy",
	preDeployCommand: "deploy",
	preDeployTimeoutSeconds: "deploy",
	postDeployCommand: "deploy",
	postDeployTimeoutSeconds: "deploy",
	stopGraceSeconds: "deploy",
	imageRetention: "deploy",
	processName: "deploy",
	singleton: "deploy",
	processes: "deploy",
	healthcheck: "deploy",
	memoryMb: "resources",
	cpus: "resources",
	files: "storage",
	docker: "advanced",
};

const TOP_CATEGORY: Record<string, CategoryId | null> = {
	name: "general",
	server: "general",
	domains: "networking",
	networks: "networking",
	volumes: "storage",
	// Variables have their own tab, and a first deployment is not a setting.
	variables: null,
	deployment: null,
};

/**
 * The category a pending change (`config.<path>`, `server`, `domains.<host>`,
 * `volumes.<name>`, `networks.<name>`) shows under, or null for changes that
 * are not settings. Source and build fields fall under General.
 */
export function stagedCategory(field: string): CategoryId | null {
	const [head = "", second = "", third = ""] = field.split(".");
	if (head === "config") {
		// A stack's compose file is General, its wait timeout is Deploy and its limits are Resources.
		if (second === "stack") {
			if (third === "resources") return "resources";
			return third === "waitSeconds" ? "deploy" : "general";
		}
		return Object.hasOwn(CONFIG_CATEGORY, second)
			? (CONFIG_CATEGORY[second] ?? "general")
			: "general";
	}
	return Object.hasOwn(TOP_CATEGORY, head)
		? (TOP_CATEGORY[head] ?? null)
		: "general";
}

/** Staged changes per category. */
export function stagedCounts(
	fields: readonly string[],
): Record<CategoryId, number> {
	const counts = Object.fromEntries(
		CATEGORY_IDS.map((id) => [id, 0]),
	) as Record<CategoryId, number>;
	for (const field of fields) {
		const id = stagedCategory(field);
		if (id !== null) counts[id] += 1;
	}
	return counts;
}

// --- search --------------------------------------------------------------

export interface SearchEntry {
	/** The visible label of the field. */
	label: string;
	/** Other words people look it up by. */
	keywords: readonly string[];
	section: SectionId;
	/** Id of the control, or `data-anchor` of the block, to jump to. */
	target: string;
	/** Present only for this source type. */
	only?: "image" | "git" | "compose";
	/** Absent for this source type. */
	not?: "compose";
}

const entry = (
	section: SectionId,
	label: string,
	target: string,
	keywords: readonly string[] = [],
	only?: "image" | "git" | "compose",
	not?: "compose",
): SearchEntry => ({
	section,
	label,
	target,
	keywords,
	...(only ? { only } : {}),
	...(not ? { not } : {}),
});

/** Every field and block a person might look for, in page order. */
export const SEARCH_ENTRIES: readonly SearchEntry[] = [
	entry("identity", "Name", "set-name", ["rename", "title"]),
	entry(
		"identity",
		"One-shot job",
		"set-oneshot",
		["run once", "batch", "migration", "job"],
		undefined,
		"compose",
	),
	entry(
		"source",
		"Change source",
		"change-source",
		[
			"switch",
			"switch to git",
			"switch to image",
			"docker image",
			"git repository",
			"convert",
			"source type",
			"from image to git",
			"from git to image",
		],
		undefined,
		"compose",
	),
	entry(
		"source",
		"Image",
		"set-image",
		["docker", "tag", "repository"],
		"image",
	),
	entry(
		"source",
		"Registry login",
		"registry-login",
		["private", "credentials", "username", "password", "auth", "secret"],
		"image",
	),
	entry(
		"source",
		"Repository",
		"set-git-url",
		["git", "url", "github", "clone"],
		"git",
	),
	entry("source", "Branch", "set-git-branch", ["git", "ref"], "git"),
	entry(
		"source",
		"Build method",
		"set-build-type",
		["builder", "railpack", "dockerfile", "buildpack", "starter", "nixpacks"],
		"git",
	),
	entry(
		"source",
		"Access token",
		"git-token",
		["private repository", "git", "pat", "credentials", "secret", "token"],
		"git",
	),
	entry(
		"build",
		"Build location",
		"set-build-location",
		[
			"build server",
			"remote build",
			"builder",
			"where builds run",
			"on the server",
			"target",
		],
		"git",
	),
	entry(
		"build",
		"Build server",
		"set-build-pin",
		["pin", "builder", "sticky", "which server"],
		"git",
	),
	entry(
		"build",
		"Build registry",
		"set-build-registry",
		["registry", "push", "image registry", "ghcr", "docker hub", "gitlab"],
		"git",
	),
	entry("build", "Dockerfile path", "set-dockerfile", ["build"], "git"),
	entry(
		"build",
		"Build context",
		"set-context",
		["directory", "folder", "monorepo", "root"],
		"git",
	),
	entry(
		"build",
		"Git submodules",
		"set-submodules",
		["submodule", "recursive", "gitmodules", "vendor"],
		"git",
	),
	entry("releases", "Stop grace period", "set-stop-grace", [
		"shutdown",
		"sigterm",
		"kill",
		"timeout",
		"restart",
	]),
	entry("releases", "Images kept", "set-retention", [
		"retention",
		"rollback",
		"prune",
		"cleanup",
	]),
	entry("deploy", "Start command", "set-command", ["entrypoint", "cmd", "run"]),
	entry("deploy", "Pre-deploy command", "set-predeploy", [
		"migrate",
		"migration",
		"release command",
		"hook",
	]),
	entry("deploy", "Post-deploy command", "set-postdeploy", [
		"warm up",
		"warm-up",
		"reindex",
		"cache",
		"hook",
	]),
	entry("deploy", "Singleton", "set-singleton", [
		"scheduler",
		"one instance",
		"never run two",
		"stop before start",
		"overlap",
	]),
	entry("processes", "Processes", "processes", [
		"worker",
		"queue",
		"sidekiq",
		"celery",
		"procfile",
		"background jobs",
		"several processes",
		"multiple processes",
		"same build",
		"one service",
	]),
	entry("processes", "Add process", "add-process", [
		"new process",
		"worker",
		"scheduler",
		"queue",
	]),
	entry("processes", "Default process name", "set-process-name", [
		"web",
		"rename",
		"main process",
	]),
	entry("releases", "Strategy", "strategy", [
		"rolling",
		"recreate",
		"downtime",
		"zero downtime",
		"next deploy",
		"restart",
	]),
	entry("deploy-hook", "Deploy hook", "deploy-hook-url", [
		"webhook",
		"ci",
		"github actions",
		"gitlab",
		"docker hub",
		"ghcr",
		"curl",
		"trigger",
		"url",
		"secret",
		"redeploy",
		"restart",
	]),
	entry("networking", "Port", "set-port", ["http", "listen", "expose"]),
	entry("networking", "Domains", "domains", [
		"hostname",
		"dns",
		"tls",
		"certificate",
		"redirect",
		"url",
		"cloudflare",
	]),
	entry("networking", "Cloudflare DNS", "domains", [
		"cloudflare",
		"proxy",
		"proxied",
		"orange cloud",
		"origin lock",
		"zone",
	]),
	entry("networking", "Generate domain", "domains", ["hostname", "sslip"]),
	entry("networking", "TCP and UDP ports", "port-exposures", [
		"expose",
		"minecraft",
		"database",
		"raw",
		"l4",
	]),
	entry("networking", "Block outbound internet", "set-block-egress", [
		"egress",
		"isolate",
		"firewall",
		"offline",
	]),
	entry("networking", "Shared networks", "networks", [
		"private",
		"attach",
		"alias",
		"link",
	]),
	entry("tailnet", "Reachable over your tailnet", "set-tailnet", [
		"tailscale",
		"headscale",
		"vpn",
		"wireguard",
		"funnel",
	]),
	entry("tailnet", "Tailnet hostname", "set-tailnet-hostname", [
		"tailscale",
		"device",
		"magicdns",
	]),
	entry("tailnet", "Tailnet web", "set-tailnet-web", [
		"tailscale",
		"https",
		"serve",
		"certificate",
	]),
	entry("tailnet", "Tailnet TCP ports", "tailnet-tcp", [
		"tailscale",
		"raw",
		"wireguard",
		"database",
		"game server",
	]),
	entry("mesh", "Reaches over the mesh", "mesh-routes", [
		"another server",
		"cross server",
		"tailnet",
		"link",
		"remote",
	]),
	entry("mesh", "Shared with other servers", "mesh-exposures", [
		"expose",
		"mesh port",
		"tailnet",
		"other server",
	]),
	entry("health-check", "Health check", "health-check", [
		"probe",
		"liveness",
		"readiness",
		"type",
		"http",
		"tcp",
		"exec",
	]),
	entry("health-check", "Health check path", "set-health-path", [
		"probe",
		"url",
		"endpoint",
	]),
	entry("health-check", "Health check port", "set-health-port", ["probe"]),
	entry("health-check", "Interval", "set-health-interval", [
		"health",
		"probe",
		"seconds",
	]),
	entry("health-check", "Timeout", "set-health-timeout", [
		"health",
		"probe",
		"seconds",
	]),
	entry("health-check", "Start period", "set-health-start", [
		"health",
		"grace",
		"warm up",
		"boot",
	]),
	entry("health-check", "Deadline", "set-health-deadline", [
		"health",
		"give up",
		"wait",
		"seconds",
	]),
	entry("health-check", "Success threshold", "set-health-success", [
		"health",
		"passes",
		"consecutive",
	]),
	entry("uptime-monitoring", "Uptime monitoring", "uptime-monitoring", [
		"uptime",
		"availability",
		"monitor",
		"checks",
		"probe",
		"outage",
		"downtime",
		"status page",
	]),
	entry("uptime-monitoring", "Monitor this service", "set-uptime-enabled", [
		"uptime",
		"pause",
		"stop checking",
		"probe",
	]),
	entry("uptime-monitoring", "Uptime alerts", "set-uptime-alerts", [
		"notify",
		"notification",
		"outage",
		"down",
		"recovered",
	]),
	entry(
		"uptime-monitoring",
		"Failed checks before an outage",
		"set-uptime-failure",
		["uptime", "threshold", "outage", "retries", "consecutive", "down"],
	),
	entry(
		"uptime-monitoring",
		"Passing checks to recover",
		"set-uptime-recovery",
		["uptime", "threshold", "recovered", "consecutive", "up"],
	),
	entry("uptime-monitoring", "Remind while down", "set-uptime-reminder", [
		"uptime",
		"repeat",
		"resend",
		"alert again",
		"reminder",
	]),
	entry("scaling", "Scaling", "scaling", [
		"scale",
		"replicas",
		"instances",
		"copies",
		"autoscale",
		"horizontal",
	]),
	entry("resources", "Memory", "set-memory", ["ram", "limit", "mb", "oom"]),
	entry("resources", "CPUs", "set-cpus", [
		"cpu",
		"cores",
		"limit",
		"processor",
	]),
	entry("docker-options", "Capabilities", "docker-capabilities", [
		"cap add",
		"cap_add",
		"linux capabilities",
		"net_bind_service",
		"sys_ptrace",
		"net_admin",
		"sys_admin",
	]),
	entry("docker-options", "Ulimits", "docker-ulimits", [
		"nofile",
		"memlock",
		"open files",
		"file descriptors",
		"limits",
	]),
	entry("docker-options", "Shared memory", "set-docker-shm", [
		"shm",
		"dev/shm",
		"chrome",
		"postgres",
		"mib",
	]),
	entry("docker-options", "Extra hosts", "docker-extra-hosts", [
		"add-host",
		"hosts file",
		"host-gateway",
		"etc/hosts",
	]),
	entry("docker-options", "Container labels", "docker-labels", [
		"label",
		"metadata",
		"docker label",
	]),
	entry("docker-options", "Init process", "docker-init", [
		"tini",
		"pid 1",
		"zombie",
		"reaping",
		"signals",
	]),
	entry("docker-options", "Stop signal", "set-docker-stop-signal", [
		"sigterm",
		"sigint",
		"sigquit",
		"shutdown",
		"graceful",
	]),
	entry("docker-options", "tmpfs mounts", "docker-tmpfs", [
		"tmpfs",
		"ram disk",
		"scratch",
		"memory filesystem",
	]),
	entry("docker-options", "Hardened preset", "docker-hardened", [
		"harden",
		"security",
		"lockdown",
		"drop all",
		"read only",
	]),
	entry("docker-options", "Drop capabilities", "docker-cap-drop", [
		"cap drop",
		"cap_drop",
		"drop all",
		"linux capabilities",
	]),
	entry("docker-options", "No new privileges", "docker-no-new-privileges", [
		"setuid",
		"sudo",
		"privilege escalation",
		"security opt",
	]),
	entry("docker-options", "Read-only root", "docker-read-only-rootfs", [
		"read only",
		"readonly",
		"root filesystem",
		"rootfs",
	]),
	entry("docker-options", "Process limit", "set-docker-pids-limit", [
		"pids",
		"threads",
		"processes",
		"fork bomb",
	]),
	entry("docker-options", "Hostname", "set-docker-hostname", [
		"host name",
		"container name",
		"slug",
	]),
	entry("docker-options", "User", "set-docker-user", [
		"uid",
		"gid",
		"non-root",
		"run as",
	]),
	entry("docker-options", "Supplementary groups", "docker-group-add", [
		"group add",
		"gid",
		"render",
		"video",
	]),
	entry("docker-options", "DNS servers", "docker-dns", [
		"resolver",
		"nameserver",
		"dns",
	]),
	entry("docker-options", "Sysctls", "docker-sysctls", [
		"kernel",
		"somaxconn",
		"ip_forward",
		"net.core",
	]),
	entry("docker-options", "Host requirements", "docker-host-sysctls", [
		"vm.max_map_count",
		"elasticsearch",
		"inotify",
		"file-max",
		"kernel parameter",
	]),
	entry("docker-options", "Devices", "docker-devices", [
		"device",
		"dri",
		"render",
		"hardware",
		"unrestricted",
	]),
	entry("docker-options", "GPUs", "docker-gpus", [
		"gpu",
		"nvidia",
		"cuda",
		"unrestricted",
	]),
	entry("docker-options", "Runtime", "set-docker-runtime", [
		"runsc",
		"gvisor",
		"sysbox",
		"oci",
		"unrestricted",
	]),
	entry("docker-options", "Privileged", "docker-privileged", [
		"privileged",
		"host root",
		"unrestricted",
	]),
	entry("docker-options", "Host PID namespace", "docker-pid-host", [
		"pid host",
		"process namespace",
		"unrestricted",
	]),
	entry("docker-options", "Seccomp", "docker-seccomp", [
		"syscall filter",
		"unconfined",
		"unrestricted",
	]),
	entry("docker-options", "AppArmor", "docker-apparmor", [
		"unconfined",
		"profile",
		"unrestricted",
	]),
	entry("placement", "Server", "set-server", [
		"host",
		"move",
		"placement",
		"machine",
	]),
	entry("volumes", "Volumes", "volumes", [
		"disk",
		"data",
		"persistent",
		"mount",
		"storage",
	]),
	entry("files", "Files", "files", [
		"config file",
		"mount",
		"write",
		"contents",
		"template",
		"env file",
		"dotenv",
	]),
	entry("tasks", "Tasks", "tasks", [
		"cron",
		"schedule",
		"scheduled",
		"command",
		"job",
		"run now",
	]),
	entry("maintenance", "Maintenance mode", "maintenance", [
		"503",
		"offline",
		"downtime",
		"page",
		"retry",
	]),
	entry("danger-zone", "Delete service", "delete-confirm", [
		"remove",
		"destroy",
		"slug",
	]),
	// A route
	entry("upstreams", "Upstreams", "upstreams", [
		"address",
		"forward",
		"backend",
		"load balance",
		"origin",
	]),
	entry("upstreams", "Health path", "route-set-health", [
		"probe",
		"check",
		"endpoint",
	]),
	entry("target", "Target", "target", ["address", "forward", "backend"]),
	entry("port-and-access", "Port on the server", "route-set-port", [
		"listen",
		"publish",
		"tcp",
		"udp",
	]),
	entry("domains", "Domains", "domains", [
		"hostname",
		"dns",
		"tls",
		"certificate",
		"url",
		"cloudflare",
	]),
	entry("domains", "Cloudflare DNS", "domains", [
		"cloudflare",
		"proxy",
		"proxied",
		"orange cloud",
		"origin lock",
		"zone",
	]),
	// A Compose stack
	entry(
		"compose",
		"Compose file",
		"set-compose",
		["yaml", "docker-compose", "services", "components", "stack", "editor"],
		"compose",
	),
	entry(
		"stack-deploy",
		"Wait timeout",
		"set-stack-wait",
		["healthy", "wait", "up", "deadline", "seconds", "compose"],
		"compose",
	),
	entry(
		"stack-deploy",
		"Stop grace period",
		"set-stop-grace",
		["shutdown", "sigterm", "kill", "timeout", "restart"],
		"compose",
	),
	entry(
		"stack-limits",
		"Component limits",
		"stack-limits",
		["memory", "cpu", "ram", "limit", "resources", "cores"],
		"compose",
	),
	entry(
		"stack-networking",
		"Domains",
		"domains",
		[
			"hostname",
			"dns",
			"tls",
			"certificate",
			"redirect",
			"url",
			"component",
			"cloudflare",
		],
		"compose",
	),
	entry(
		"stack-networking",
		"Cloudflare DNS",
		"domains",
		["cloudflare", "proxy", "proxied", "orange cloud", "origin lock", "zone"],
		"compose",
	),
	entry(
		"stack-networking",
		"Generate domain",
		"domains",
		["hostname", "sslip", "component"],
		"compose",
	),
	entry(
		"stack-networking",
		"Block outbound internet",
		"set-block-egress",
		["egress", "isolate", "firewall", "offline"],
		"compose",
	),
	entry(
		"stack-networking",
		"Shared networks",
		"networks",
		["private", "attach", "alias", "link"],
		"compose",
	),
];

export interface SearchResult {
	entry: SearchEntry;
	category: CategoryId;
	categoryLabel: string;
	sectionLabel: string;
	score: number;
}

const words = (text: string): string[] =>
	text
		.toLowerCase()
		.replace(/[^a-z0-9]+/g, " ")
		.trim()
		.split(" ")
		.filter(Boolean);

const normalise = (text: string): string => words(text).join(" ");

/**
 * A query word with its ending taken off, so "scale" finds "Scaling" and
 * "restarts" finds "restart". Words too short to lose a letter stay as they
 * are.
 */
function stem(token: string): string {
	const bare = token.replace(/(ing|ed|es|s|e)$/, "");
	return bare.length >= 4 ? bare : token;
}

/** How well one query word matches the entry, 0 for not at all. */
function tokenScore(
	token: string,
	label: string,
	keywords: readonly string[],
	where: string,
): number {
	const labelWords = label.split(" ");
	let best = 0;
	if (label === token) best = 100;
	else if (label.startsWith(token)) best = 80;
	else if (labelWords.some((word) => word.startsWith(token))) best = 60;
	else if (label.includes(token)) best = 40;
	// The same word in another form ("scale" and "Scaling") is nearly a label
	// match: it must outrank a keyword that merely contains the letters.
	const root = stem(token);
	if (root !== token && labelWords.some((word) => word.startsWith(root))) {
		best = Math.max(best, 55);
	}
	for (const keyword of keywords) {
		const keywordWords = keyword.split(" ");
		if (keyword === token) best = Math.max(best, 50);
		else if (keywordWords.some((word) => word.startsWith(token))) {
			best = Math.max(best, 30);
		} else if (root !== token && keywordWords.some((w) => w.startsWith(root))) {
			best = Math.max(best, 25);
		} else if (keyword.includes(token)) best = Math.max(best, 15);
	}
	if (best === 0 && where.includes(token)) best = 8;
	return best;
}

/**
 * The fields matching `query`, best first. Every word of the query must
 * match the label, a keyword or the section it sits in; a label match beats a
 * keyword match, and an exact or leading match beats one in the middle. Ties
 * keep page order. Only fields this service has are searched.
 */
export function searchSettings(
	query: string,
	subject: SettingsSubject,
	limit = 8,
): SearchResult[] {
	const tokens = words(query);
	if (tokens.length === 0) return [];
	const categories = categoriesFor(subject);
	const whole = tokens.join(" ");
	const results: (SearchResult & { order: number })[] = [];
	SEARCH_ENTRIES.forEach((candidate, order) => {
		if (candidate.only && candidate.only !== subject.sourceType) return;
		if (candidate.not !== undefined && candidate.not === subject.sourceType)
			return;
		const category = categories.find((c) =>
			c.sections.some((section) => section.id === candidate.section),
		);
		if (!category) return;
		const sectionLabel = SECTION_LABEL[candidate.section];
		const label = normalise(candidate.label);
		const keywords = candidate.keywords.map(normalise);
		const where = normalise(`${sectionLabel} ${category.label}`);
		let score = 0;
		for (const token of tokens) {
			const part = tokenScore(token, label, keywords, where);
			if (part === 0) return;
			score += part;
		}
		if (label === whole) score += 50;
		else if (label.startsWith(whole)) score += 20;
		results.push({
			entry: candidate,
			category: category.id,
			categoryLabel: category.label,
			sectionLabel,
			score,
			order,
		});
	});
	results.sort((a, b) => b.score - a.score || a.order - b.order);
	return results.slice(0, limit).map(({ order: _order, ...result }) => result);
}

// --- ⌘K ------------------------------------------------------------------

/** One palette entry for a category a service has, found by what is inside it. */
export interface CategoryCommand {
	category: CategoryId;
	/** The category's name: "Deploy". */
	label: string;
	/** Its sections, its fields and what people call them, plus the name it had before. */
	keywords: string[];
}

/**
 * The ⌘K command of every category `subject` shows. Typing a section, a field
 * or one of its keywords ("health check", "dockerfile", "ram") finds the
 * category, so the palette reaches every settings page without a second box.
 */
export function categoryCommands(subject: SettingsSubject): CategoryCommand[] {
	return categoriesFor(subject).map((category) => {
		const sections = new Set<SectionId>(
			category.sections.map((section) => section.id),
		);
		const keywords = new Set<string>(
			category.sections.map((section) => section.label.toLowerCase()),
		);
		for (const [old, id] of Object.entries(CATEGORY_ALIAS)) {
			if (id === category.id) keywords.add(old);
		}
		for (const candidate of SEARCH_ENTRIES) {
			if (!sections.has(candidate.section)) continue;
			if (candidate.only && candidate.only !== subject.sourceType) continue;
			if (candidate.not !== undefined && candidate.not === subject.sourceType)
				continue;
			keywords.add(candidate.label.toLowerCase());
			for (const keyword of candidate.keywords) keywords.add(keyword);
		}
		keywords.delete(category.label.toLowerCase());
		return {
			category: category.id,
			label: category.label,
			keywords: [...keywords],
		};
	});
}
