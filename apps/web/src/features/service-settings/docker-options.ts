import { errorCode, errorMessage, isApiError } from "#/api/client-config";
import type { DockerOptions, DockerOptionsCatalog } from "#/api/gen/types.gen";
import { quoteArg, splitCommand } from "./command-line";

/**
 * The typed Docker options of a service as a form, and the same rules the
 * control plane applies (`pkg/agentprotocol/dockeropts.go`) so a problem shows
 * next to the field instead of as a 422 on Save. Pure: the section draws what
 * this decides.
 *
 * Two tiers: most fields work on every server. The "host access" fields
 * (devices, GPUs, runtime, privileged, host PID namespace, unconfined seccomp
 * or AppArmor, and the NET_ADMIN and SYS_ADMIN capabilities) work only on a
 * server an instance admin made Unrestricted. Whether the target server allows
 * them is a placement question the form cannot judge, so the control plane's
 * 422 `requires_unrestricted_server` is the authority; `unrestrictedFields`
 * says what in a form needs the tier.
 */

export type UlimitName = "nofile" | "memlock";

/** `nproc` is not offered: it is per user ID and not enforced for root; `pidsLimit` is the real cap. */
export const ULIMIT_NAMES: readonly UlimitName[] = ["nofile", "memlock"];

export const MAX_DOCKER_LABELS = 30;
export const MAX_DOCKER_EXTRA_HOSTS = 20;
export const MAX_DOCKER_TMPFS = 10;
export const MAX_DOCKER_DNS = 3;
export const MAX_DOCKER_GROUP_ADD = 8;
export const MAX_DOCKER_SYSCTLS = 16;
export const MAX_DOCKER_DEVICES = 16;
export const MAX_DOCKER_GPUS = 16;
export const MAX_ENTRYPOINT_ARGS = 32;
export const MAX_ENTRYPOINT_ARG_LENGTH = 4096;
export const MAX_SIZE_MB = 65536;
export const MAX_PIDS_LIMIT = 4194304;
export const MAX_HOST_SYSCTL_VALUE = 2 ** 32;
export const DEFAULT_TMPFS_SIZE_MB = 64;
/** What the Hardened preset caps the container's processes and threads at. */
export const HARDENED_PIDS_LIMIT = 512;
const MAX_ULIMIT = 2 ** 31 - 1;
const MAX_ID = 2 ** 31 - 1;

export interface UlimitRow {
	name: UlimitName;
	/** A number, `-1`, or `unlimited`. */
	soft: string;
	hard: string;
}

export interface ExtraHostRow {
	name: string;
	/** An IPv4 or IPv6 address, or `host-gateway`. */
	address: string;
}

export interface LabelRow {
	key: string;
	value: string;
}

export interface TmpfsRow {
	path: string;
	/** Empty means the default of 64 MiB. */
	sizeMb: string;
	/** Octal, three or four digits; empty means the default. */
	mode: string;
}

/** A container sysctl: its key and the number (or space-separated numbers) it is set to. */
export interface SysctlRow {
	key: string;
	value: string;
}

/** A host kernel parameter the service needs, and the minimum value it needs. */
export interface HostSysctlRow {
	key: string;
	/** A whole number from 1 to 4294967296. */
	value: string;
}

export interface DeviceRow {
	/** A device node below /dev. */
	hostPath: string;
	/** Empty means the same path. */
	containerPath: string;
	/** A subset of `rwm`; empty means all three. */
	permissions: string;
}

export type GpuMode = "none" | "all" | "count";

export interface GpuForm {
	mode: GpuMode;
	/** The number of GPUs, 1 to 16, for the `count` mode. */
	count: string;
	/** NVIDIA driver capabilities; empty means the toolkit's default. */
	capabilities: string[];
}

/** What the reader typed; every number stays text so a half-typed row is representable. */
export interface DockerForm {
	/** Normalized: upper case, no `CAP_` prefix. */
	capAdd: string[];
	/** Normalized like `capAdd`; `ALL` or capability names. */
	capDrop: string[];
	noNewPrivileges: boolean;
	readOnlyRootfs: boolean;
	/** 0 or empty means no limit. */
	pidsLimit: string;
	/** Empty means the service slug. */
	hostname: string;
	/** Resolver IP addresses, at most 3. */
	dns: string[];
	sysctls: SysctlRow[];
	hostSysctls: HostSysctlRow[];
	/** `uid[:gid]`, numbers or names; empty means the image's. */
	user: string;
	/** Numeric group IDs. */
	groupAdd: string[];
	ulimits: UlimitRow[];
	shmSizeMb: string;
	extraHosts: ExtraHostRow[];
	labels: LabelRow[];
	init: boolean;
	/** Empty means the image's default. */
	stopSignal: string;
	tmpfs: TmpfsRow[];
	/** The exec-form argv as one line (`quoteArg`/`splitCommand`); empty means the image's. */
	entrypoint: string;
	// Unrestricted servers only from here on.
	devices: DeviceRow[];
	gpus: GpuForm;
	/** An OCI runtime configured in the daemon, for example `runsc`. */
	runtime: string;
	privileged: boolean;
	pidHost: boolean;
	seccompUnconfined: boolean;
	apparmorUnconfined: boolean;
}

export const EMPTY_GPU_FORM: GpuForm = {
	mode: "none",
	count: "",
	capabilities: [],
};

export const EMPTY_DOCKER_FORM: DockerForm = {
	capAdd: [],
	capDrop: [],
	noNewPrivileges: false,
	readOnlyRootfs: false,
	pidsLimit: "",
	hostname: "",
	dns: [],
	sysctls: [],
	hostSysctls: [],
	user: "",
	groupAdd: [],
	ulimits: [],
	shmSizeMb: "",
	extraHosts: [],
	labels: [],
	init: false,
	stopSignal: "",
	tmpfs: [],
	entrypoint: "",
	devices: [],
	gpus: EMPTY_GPU_FORM,
	runtime: "",
	privileged: false,
	pidHost: false,
	seccompUnconfined: false,
	apparmorUnconfined: false,
};

/** What the Docker options accept, as the control plane reports it. */
export interface DockerPolicy {
	safeCapabilities: readonly string[];
	warnCapabilities: readonly string[];
	/** Capabilities that need an unrestricted server. */
	unrestrictedCapabilities: readonly string[];
	/** Every Linux capability `capDrop` may name besides `ALL`. */
	knownCapabilities: readonly string[];
	allowedSysctls: readonly string[];
	hostSysctlKeys: readonly string[];
	gpuCapabilities: readonly string[];
	reservedLabels: readonly string[];
	stopSignals: readonly string[];
}

/**
 * What is known before the policy loads: the same lists the control plane
 * ships. An empty list means "ask the control plane": the form skips the
 * check it would feed.
 */
export const FALLBACK_POLICY: DockerPolicy = {
	safeCapabilities: [],
	warnCapabilities: ["SYS_PTRACE"],
	unrestrictedCapabilities: ["NET_ADMIN", "SYS_ADMIN"],
	knownCapabilities: [],
	allowedSysctls: [],
	hostSysctlKeys: [],
	gpuCapabilities: [
		"compat32",
		"compute",
		"display",
		"graphics",
		"utility",
		"video",
	],
	reservedLabels: ["com.docker.", "nebulactrl.", "traefik."],
	stopSignals: [
		"SIGTERM",
		"SIGINT",
		"SIGQUIT",
		"SIGHUP",
		"SIGUSR1",
		"SIGUSR2",
		"SIGWINCH",
		"SIGKILL",
	],
};

export function toDockerPolicy(catalog: DockerOptionsCatalog): DockerPolicy {
	return {
		safeCapabilities: catalog.safeCapabilities ?? [],
		warnCapabilities: catalog.warnCapabilities ?? [],
		unrestrictedCapabilities: catalog.unrestrictedCapabilities ?? [],
		knownCapabilities: catalog.knownCapabilities ?? [],
		allowedSysctls: catalog.allowedSysctls ?? [],
		hostSysctlKeys: catalog.hostSysctlKeys ?? [],
		gpuCapabilities: catalog.gpuCapabilities ?? [],
		reservedLabels: catalog.reservedLabels ?? [],
		stopSignals: catalog.stopSignals ?? [],
	};
}

/** `cap_net_raw ` → `NET_RAW`. */
export function normalizeCapability(value: string): string {
	const upper = value.trim().toUpperCase();
	return upper.startsWith("CAP_") ? upper.slice("CAP_".length) : upper;
}

/** Upper-cased, de-duplicated, sorted, without blanks: the control plane's order. */
function normalizeCapabilities(list: readonly string[]): string[] {
	return [...new Set(list.map(normalizeCapability))]
		.filter((cap) => cap !== "")
		.sort();
}

const limitText = (value: number): string =>
	value === -1 ? "unlimited" : String(value);

const byKey = (a: string, b: string): number => (a < b ? -1 : a > b ? 1 : 0);

export function toDockerForm(options: DockerOptions | undefined): DockerForm {
	if (!options) return EMPTY_DOCKER_FORM;
	return {
		capAdd: [
			...new Set((options.capAdd ?? []).map(normalizeCapability)),
		].sort(),
		capDrop: [
			...new Set((options.capDrop ?? []).map(normalizeCapability)),
		].sort(),
		noNewPrivileges: options.noNewPrivileges === true,
		readOnlyRootfs: options.readOnlyRootfs === true,
		pidsLimit: options.pidsLimit ? String(options.pidsLimit) : "",
		hostname: options.hostname ?? "",
		dns: [...(options.dns ?? [])],
		sysctls: Object.entries(options.sysctls ?? {})
			.sort(([a], [b]) => byKey(a, b))
			.map(([key, value]) => ({ key, value })),
		hostSysctls: Object.entries(options.hostSysctls ?? {})
			.sort(([a], [b]) => byKey(a, b))
			.map(([key, value]) => ({ key, value: String(value) })),
		user: options.user ?? "",
		groupAdd: [...(options.groupAdd ?? [])],
		ulimits: [...(options.ulimits ?? [])]
			.sort((a, b) => a.name.localeCompare(b.name))
			.map((limit) => ({
				name: limit.name,
				soft: limitText(limit.soft),
				hard: limitText(limit.hard),
			})),
		shmSizeMb: options.shmSizeMb ? String(options.shmSizeMb) : "",
		extraHosts: (options.extraHosts ?? []).map((host) => {
			const cut = host.indexOf(":");
			return cut < 0
				? { name: host, address: "" }
				: { name: host.slice(0, cut), address: host.slice(cut + 1) };
		}),
		labels: Object.entries(options.labels ?? {})
			.sort(([a], [b]) => byKey(a, b))
			.map(([key, value]) => ({ key, value })),
		init: options.init === true,
		stopSignal: (options.stopSignal ?? "").toUpperCase(),
		tmpfs: [...(options.tmpfs ?? [])]
			.sort((a, b) => byKey(a.path, b.path))
			.map((mount) => ({
				path: mount.path,
				sizeMb: mount.sizeMb ? String(mount.sizeMb) : "",
				mode: mount.mode ?? "",
			})),
		entrypoint: (options.entrypoint ?? []).map(quoteArg).join(" "),
		devices: (options.devices ?? []).map((device) => ({
			hostPath: device.hostPath,
			containerPath: device.containerPath ?? "",
			permissions: device.permissions ?? "",
		})),
		gpus: toGpuForm(options.gpus),
		runtime: options.runtime ?? "",
		privileged: options.privileged === true,
		pidHost: options.pidHost === true,
		seccompUnconfined: options.seccompUnconfined === true,
		apparmorUnconfined: options.apparmorUnconfined === true,
	};
}

function toGpuForm(gpus: DockerOptions["gpus"]): GpuForm {
	if (!gpus) return EMPTY_GPU_FORM;
	return {
		mode: gpus.count === -1 ? "all" : "count",
		count: gpus.count === -1 ? "" : String(gpus.count),
		capabilities: [
			...new Set((gpus.capabilities ?? []).map((cap) => cap.toLowerCase())),
		].sort(),
	};
}

// --- parsing -------------------------------------------------------------

/** `-1`, `unlimited` (any case) or a non-negative integer; NaN for anything else. */
export function parseLimit(value: string): number {
	const text = value.trim().toLowerCase();
	if (text === "unlimited") return -1;
	return /^-?\d+$/.test(text) ? Number.parseInt(text, 10) : Number.NaN;
}

function parseCount(value: string): number {
	const text = value.trim();
	return /^\d+$/.test(text) ? Number.parseInt(text, 10) : Number.NaN;
}

const blank = (value: string): boolean => value.trim() === "";

const ulimitBlank = (row: UlimitRow): boolean =>
	blank(row.soft) && blank(row.hard);
const hostBlank = (row: ExtraHostRow): boolean =>
	blank(row.name) && blank(row.address);
const labelBlank = (row: LabelRow): boolean =>
	blank(row.key) && blank(row.value);
const tmpfsBlank = (row: TmpfsRow): boolean =>
	blank(row.path) && blank(row.sizeMb) && blank(row.mode);
const pairBlank = (row: SysctlRow | HostSysctlRow): boolean =>
	blank(row.key) && blank(row.value);
const deviceBlank = (row: DeviceRow): boolean =>
	blank(row.hostPath) && blank(row.containerPath) && blank(row.permissions);

/** Rows that carry nothing are not sent and not validated. */
export function dropEmptyRows(form: DockerForm): DockerForm {
	return {
		...form,
		dns: form.dns.filter((value) => !blank(value)),
		groupAdd: form.groupAdd.filter((value) => !blank(value)),
		sysctls: form.sysctls.filter((row) => !pairBlank(row)),
		hostSysctls: form.hostSysctls.filter((row) => !pairBlank(row)),
		ulimits: form.ulimits.filter((row) => !ulimitBlank(row)),
		extraHosts: form.extraHosts.filter((row) => !hostBlank(row)),
		labels: form.labels.filter((row) => !labelBlank(row)),
		tmpfs: form.tmpfs.filter((row) => !tmpfsBlank(row)),
		devices: form.devices.filter((row) => !deviceBlank(row)),
	};
}

function buildGpus(gpus: GpuForm): DockerOptions["gpus"] {
	if (gpus.mode === "none") return undefined;
	const capabilities = [
		...new Set(
			gpus.capabilities.map((cap) => cap.trim().toLowerCase()).filter(Boolean),
		),
	].sort();
	return {
		count: gpus.mode === "all" ? -1 : parseCount(gpus.count),
		driver: "nvidia",
		...(capabilities.length > 0 ? { capabilities } : {}),
	};
}

/**
 * The `docker` config the form stands for, or undefined when it sets nothing.
 * Total: a value that does not parse becomes NaN or stays text, so a bad draft
 * still differs from the saved one (the form's validation blocks saving it).
 * Lists come out in the control plane's canonical order (`Normalized()` in Go),
 * so reordering rows does not read as a change and an untouched form never
 * stages one.
 */
export function buildDockerOptions(
	form: DockerForm,
): DockerOptions | undefined {
	const kept = dropEmptyRows(form);
	const options: DockerOptions = {};
	const caps = normalizeCapabilities(kept.capAdd);
	if (caps.length > 0) options.capAdd = caps;
	const dropped = normalizeCapabilities(kept.capDrop);
	if (dropped.length > 0) options.capDrop = dropped;
	if (kept.noNewPrivileges) options.noNewPrivileges = true;
	if (kept.readOnlyRootfs) options.readOnlyRootfs = true;
	if (!blank(kept.pidsLimit)) {
		const limit = parseCount(kept.pidsLimit);
		// 0 means no limit, the same as unset.
		if (limit !== 0) options.pidsLimit = limit;
	}
	const hostname = kept.hostname.trim().toLowerCase();
	if (hostname !== "") options.hostname = hostname;
	if (kept.dns.length > 0) options.dns = kept.dns.map((ip) => ip.trim());
	if (kept.sysctls.length > 0) {
		const sysctls: Record<string, string> = {};
		for (const row of [...kept.sysctls].sort((a, b) => byKey(a.key, b.key))) {
			sysctls[row.key.trim()] = row.value.trim();
		}
		options.sysctls = sysctls;
	}
	if (kept.hostSysctls.length > 0) {
		const hostSysctls: Record<string, number> = {};
		for (const row of [...kept.hostSysctls].sort((a, b) =>
			byKey(a.key, b.key),
		)) {
			hostSysctls[row.key.trim()] = parseCount(row.value);
		}
		options.hostSysctls = hostSysctls;
	}
	const user = kept.user.trim();
	if (user !== "") options.user = user;
	if (kept.groupAdd.length > 0) {
		options.groupAdd = kept.groupAdd.map((gid) => gid.trim());
	}
	if (kept.ulimits.length > 0) {
		options.ulimits = [...kept.ulimits]
			.sort((a, b) => a.name.localeCompare(b.name))
			.map((row) => ({
				name: row.name,
				soft: parseLimit(row.soft),
				hard: parseLimit(row.hard),
			}));
	}
	if (!blank(kept.shmSizeMb)) options.shmSizeMb = parseCount(kept.shmSizeMb);
	if (kept.extraHosts.length > 0) {
		options.extraHosts = kept.extraHosts.map(
			(row) => `${row.name.trim()}:${row.address.trim()}`,
		);
	}
	if (kept.labels.length > 0) {
		const labels: Record<string, string> = {};
		for (const row of [...kept.labels].sort((a, b) => byKey(a.key, b.key))) {
			labels[row.key.trim()] = row.value;
		}
		options.labels = labels;
	}
	if (kept.init) options.init = true;
	if (!blank(kept.stopSignal)) {
		options.stopSignal = kept.stopSignal.trim().toUpperCase();
	}
	if (kept.tmpfs.length > 0) {
		options.tmpfs = [...kept.tmpfs]
			.sort((a, b) => byKey(a.path, b.path))
			.map((row) => ({
				path: row.path.trim(),
				...(blank(row.sizeMb) ? {} : { sizeMb: parseCount(row.sizeMb) }),
				...(blank(row.mode) ? {} : { mode: row.mode.trim() }),
			}));
	}
	const entrypoint = splitCommand(kept.entrypoint);
	if (entrypoint.length > 0) options.entrypoint = entrypoint;
	if (kept.devices.length > 0) {
		options.devices = kept.devices.map((row) => ({
			hostPath: row.hostPath.trim(),
			...(blank(row.containerPath)
				? {}
				: { containerPath: row.containerPath.trim() }),
			...(blank(row.permissions)
				? {}
				: { permissions: row.permissions.trim() }),
		}));
	}
	const gpus = buildGpus(kept.gpus);
	if (gpus) options.gpus = gpus;
	const runtime = kept.runtime.trim();
	if (runtime !== "") options.runtime = runtime;
	if (kept.privileged) options.privileged = true;
	if (kept.pidHost) options.pidHost = true;
	if (kept.seccompUnconfined) options.seccompUnconfined = true;
	if (kept.apparmorUnconfined) options.apparmorUnconfined = true;
	return Object.keys(options).length === 0 ? undefined : options;
}

// --- the hardened preset -------------------------------------------------

/**
 * The form with the hardening options on: drop every capability, no new
 * privileges, a read-only root filesystem, a cap of 512 processes and, when no
 * tmpfs exists yet, a 64 MiB `/tmp`. Everything else is left as it is.
 */
export function applyHardened(form: DockerForm): DockerForm {
	const hasTmpfs = form.tmpfs.some((row) => !tmpfsBlank(row));
	return {
		...form,
		capDrop: ["ALL"],
		noNewPrivileges: true,
		readOnlyRootfs: true,
		pidsLimit: String(HARDENED_PIDS_LIMIT),
		tmpfs: hasTmpfs
			? form.tmpfs
			: [
					...form.tmpfs.filter((row) => !tmpfsBlank(row)),
					{ path: "/tmp", sizeMb: String(DEFAULT_TMPFS_SIZE_MB), mode: "" },
				],
	};
}

/** Whether the hardening options of the preset are all on. */
export function isHardened(form: DockerForm): boolean {
	return (
		form.capDrop.map(normalizeCapability).includes("ALL") &&
		form.noNewPrivileges &&
		form.readOnlyRootfs &&
		parseCount(form.pidsLimit) > 0
	);
}

// --- host access ---------------------------------------------------------

/**
 * What in the form needs an unrestricted server, in the control plane's words
 * (`UnrestrictedFields` in Go): the field names, and `capAdd NAME` for each
 * capability that needs it.
 */
export function unrestrictedFields(
	form: DockerForm,
	policy: DockerPolicy | undefined = FALLBACK_POLICY,
): string[] {
	const out: string[] = [];
	if (form.devices.some((row) => !deviceBlank(row))) out.push("devices");
	if (form.gpus.mode !== "none") out.push("gpus");
	if (!blank(form.runtime)) out.push("runtime");
	if (form.privileged) out.push("privileged");
	if (form.pidHost) out.push("pidHost");
	if (form.seccompUnconfined) out.push("seccompUnconfined");
	if (form.apparmorUnconfined) out.push("apparmorUnconfined");
	const needing =
		policy?.unrestrictedCapabilities ??
		FALLBACK_POLICY.unrestrictedCapabilities;
	for (const cap of normalizeCapabilities(form.capAdd)) {
		if (needing.includes(cap)) out.push(`capAdd ${cap}`);
	}
	return out;
}

/**
 * The form without anything that needs an unrestricted server: no devices,
 * GPUs, runtime or host-access switches, and no NET_ADMIN or SYS_ADMIN.
 */
export function clearHostAccess(
	form: DockerForm,
	policy: DockerPolicy | undefined = FALLBACK_POLICY,
): DockerForm {
	const needing =
		policy?.unrestrictedCapabilities ??
		FALLBACK_POLICY.unrestrictedCapabilities;
	return {
		...form,
		capAdd: form.capAdd.filter(
			(cap) => !needing.includes(normalizeCapability(cap)),
		),
		devices: [],
		gpus: EMPTY_GPU_FORM,
		runtime: "",
		privileged: false,
		pidHost: false,
		seccompUnconfined: false,
		apparmorUnconfined: false,
	};
}

const FIELD_LABELS: Readonly<Record<string, string>> = {
	devices: "host devices",
	gpus: "GPUs",
	runtime: "a custom runtime",
	privileged: "privileged mode",
	pidHost: "the host PID namespace",
	seccompUnconfined: "unconfined seccomp",
	apparmorUnconfined: "unconfined AppArmor",
};

/** `capAdd NET_ADMIN` → "the NET_ADMIN capability"; known field names in words. */
export function unrestrictedFieldLabel(field: string): string {
	const cap = /^capAdd (.+)$/.exec(field);
	if (cap) return `the ${cap[1]} capability`;
	return FIELD_LABELS[field] ?? field;
}

/** "a, b and c". */
export function joinWords(items: readonly string[]): string {
	if (items.length <= 1) return items[0] ?? "";
	return `${items.slice(0, -1).join(", ")} and ${items[items.length - 1]}`;
}

/** The warnings of the host-access options, worded like `DockerOptions.Warnings()` in Go. */
export const HOST_ACCESS_WARNINGS = {
	privileged:
		"Privileged mode gives the container all capabilities and devices: it is equivalent to root on the host.",
	pidHost:
		"The container shares the host's process namespace: it can see and signal every process on the host.",
	unconfined:
		"The default syscall or AppArmor profile is switched off: a kernel exploit from inside the container has fewer barriers.",
	hardware:
		"Host devices and GPUs widen the container's access to host hardware and drivers; the image must be trusted.",
} as const;

// --- addresses -----------------------------------------------------------

function isIPv4(value: string): boolean {
	const parts = value.split(".");
	return (
		parts.length === 4 &&
		parts.every(
			(part) => /^(0|[1-9]\d{0,2})$/.test(part) && Number(part) <= 255,
		)
	);
}

function isIPv6(input: string): boolean {
	const zone = input.indexOf("%");
	if (zone === 0 || zone === input.length - 1) return false;
	const value = zone > 0 ? input.slice(0, zone) : input;
	if (!/^[0-9a-fA-F:.]+$/.test(value)) return false;
	const halves = value.split("::");
	if (halves.length > 2) return false;
	const gap = halves.length === 2;
	const groups = (part: string | undefined): string[] =>
		part === undefined || part === "" ? [] : part.split(":");
	const head = groups(halves[0]);
	const tail = groups(halves[1]);
	const tokens = [...head, ...tail];
	let width = 0;
	for (const [index, token] of tokens.entries()) {
		if (token.includes(".")) {
			// An embedded IPv4 address fills the last 32 bits, so nothing may follow it.
			const last = index === tokens.length - 1 && !(gap && tail.length === 0);
			if (!last || !isIPv4(token)) return false;
			width += 2;
		} else if (/^[0-9a-fA-F]{1,4}$/.test(token)) {
			width += 1;
		} else {
			return false;
		}
	}
	return gap ? width <= 7 : width === 8;
}

export function isIPAddress(value: string): boolean {
	return isIPv4(value) || isIPv6(value);
}

/** The eight 16-bit groups of a valid IPv6 address without a zone. */
function ipv6Groups(value: string): number[] {
	const expand = (part: string | undefined): number[] =>
		part === undefined || part === ""
			? []
			: part.split(":").flatMap((token) => {
					if (!token.includes(".")) return [Number.parseInt(token, 16)];
					const [a = 0, b = 0, c = 0, d = 0] = token.split(".").map(Number);
					return [a * 256 + b, c * 256 + d];
				});
	const [head, tail] = value.split("::");
	const first = expand(head);
	const last = expand(tail);
	if (tail === undefined) return first;
	return [
		...first,
		...new Array<number>(8 - first.length - last.length).fill(0),
		...last,
	];
}

/**
 * Why a DNS server address is not usable, or undefined when it is, and a key
 * that is equal for equal addresses (so a duplicate can be told).
 */
function resolverProblem(value: string): {
	problem?: string;
	key: string;
} {
	if (isIPv4(value)) {
		const first = Number(value.split(".")[0]);
		const bad = value === "0.0.0.0" || (first >= 224 && first <= 239);
		return {
			key: `4:${value}`,
			...(bad ? { problem: `${value} is not a usable resolver address.` } : {}),
		};
	}
	if (!isIPv6(value) || value.includes("%")) {
		return {
			key: value,
			problem: `${value} must be an IP address (hostnames are not accepted).`,
		};
	}
	const groups = ipv6Groups(value);
	const unspecified = groups.every((group) => group === 0);
	const mapped =
		groups.slice(0, 5).every((group) => group === 0) && groups[5] === 0xffff;
	const first = (groups[6] ?? 0) >> 8;
	const multicast =
		(groups[0] ?? 0) >> 8 === 0xff || (mapped && first >= 224 && first <= 239);
	return {
		key: `6:${groups.join(":")}`,
		...(unspecified || multicast
			? { problem: `${value} is not a usable resolver address.` }
			: {}),
	};
}

// --- validation ----------------------------------------------------------

export type DockerField =
	| "capAdd"
	| "capDrop"
	| "pidsLimit"
	| "hostname"
	| "dns"
	| "sysctls"
	| "hostSysctls"
	| "user"
	| "groupAdd"
	| "ulimits"
	| "shmSizeMb"
	| "extraHosts"
	| "labels"
	| "stopSignal"
	| "tmpfs"
	| "entrypoint"
	| "devices"
	| "gpus"
	| "runtime";

/** One problem, on a field and (for the row editors) a row of it. */
export interface DockerIssue {
	field: DockerField;
	/** Index into the form's own rows, empty ones included. */
	row?: number;
	message: string;
}

const LABEL_KEY = /^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$/;
const TMPFS_MODE = /^[0-7]{3,4}$/;
const HOST_NAME = /^[A-Za-z0-9]([A-Za-z0-9._-]{0,251}[A-Za-z0-9])?$/;
const HOSTNAME_LABEL = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;
const SYSCTL_VALUE = /^[0-9]+( [0-9]+)*$/;
const USER_PART = /^([0-9]{1,10}|[a-z_][a-z0-9_.-]{0,31}\$?)$/;
const GROUP_ID = /^[0-9]{1,5}$/;
const RUNTIME_NAME = /^[a-z0-9][a-z0-9_.-]{0,62}$/;
const DEVICE_PERMISSIONS = /^[rwm]{1,3}$/;
const RESERVED_TMPFS = [
	"/proc",
	"/sys",
	"/dev",
	"/run/docker",
	"/var/run/docker",
];

/** POSIX `path.Clean`, for comparing a tmpfs path with a file mount's. */
export function cleanPath(value: string): string {
	const absolute = value.startsWith("/");
	const out: string[] = [];
	for (const part of value.split("/")) {
		if (part === "" || part === ".") continue;
		if (part === "..") {
			if (out.length > 0 && out[out.length - 1] !== "..") out.pop();
			else if (!absolute) out.push("..");
			continue;
		}
		out.push(part);
	}
	const joined = out.join("/");
	return absolute ? `/${joined}` : joined === "" ? "." : joined;
}

export interface DockerContext {
	policy?: DockerPolicy | undefined;
	/** Paths of the files mounted into the container: a tmpfs may not sit on one. */
	filePaths?: readonly string[];
}

function capabilityIssue(
	cap: string,
	policy: DockerPolicy | undefined,
): string | undefined {
	if (policy === undefined || policy.safeCapabilities.length === 0) {
		// The policy has not loaded: only the control plane can tell.
		return undefined;
	}
	if (policy.safeCapabilities.includes(cap)) return undefined;
	// The ones that need an unrestricted server are well-formed; the server
	// they are placed on decides.
	if (policy.unrestrictedCapabilities.includes(cap)) return undefined;
	return `${cap} is not an allowed capability.`;
}

/** Why `path` is not a device node below /dev, or undefined. */
function devicePathProblem(path: string): string | undefined {
	if (!path.startsWith("/") || cleanPath(path) !== path) {
		return "must be a clean absolute path";
	}
	if (!path.startsWith("/dev/") || path === "/dev/") {
		return "must be a device node below /dev";
	}
	if (/[,: \0]/.test(path)) return "must not contain spaces, commas or colons";
	return undefined;
}

function userProblem(user: string): boolean {
	const cut = user.indexOf(":");
	const uid = cut < 0 ? user : user.slice(0, cut);
	const gid = cut < 0 ? undefined : user.slice(cut + 1);
	const tooBig = (part: string): boolean =>
		/^[0-9]+$/.test(part) && Number(part) > MAX_ID;
	return (
		!USER_PART.test(uid) ||
		(gid !== undefined && !USER_PART.test(gid)) ||
		tooBig(uid) ||
		(gid !== undefined && tooBig(gid))
	);
}

/**
 * What is wrong with the form, field by field, with the same rules the control
 * plane applies on Save. Empty rows are skipped.
 */
export function validateDockerForm(
	form: DockerForm,
	context: DockerContext = {},
): DockerIssue[] {
	const policy = context.policy;
	const issues: DockerIssue[] = [];
	const add = (field: DockerField, message: string, row?: number) =>
		issues.push({ field, message, ...(row === undefined ? {} : { row }) });

	// Capabilities.
	const seenCaps = new Set<string>();
	for (const raw of form.capAdd) {
		const cap = normalizeCapability(raw);
		if (cap === "") continue;
		if (seenCaps.has(cap)) add("capAdd", `${cap} is listed twice.`);
		seenCaps.add(cap);
		const problem = capabilityIssue(cap, policy);
		if (problem) add("capAdd", problem);
	}

	// Dropped capabilities.
	const known = policy?.knownCapabilities ?? [];
	const droppedCaps = new Set<string>();
	for (const raw of form.capDrop) {
		const cap = normalizeCapability(raw);
		if (cap === "") continue;
		if (droppedCaps.has(cap)) {
			add("capDrop", `${cap} is listed twice.`);
		} else if (cap !== "ALL" && known.length > 0 && !known.includes(cap)) {
			add(
				"capDrop",
				`${raw.trim()} is not a Linux capability; use ALL or a name such as NET_RAW.`,
			);
		}
		droppedCaps.add(cap);
	}
	for (const cap of seenCaps) {
		if (droppedCaps.has(cap)) {
			add(
				"capDrop",
				`${cap} is in both the added and the dropped capabilities.`,
			);
		}
	}

	// Process limit.
	if (!blank(form.pidsLimit)) {
		const limit = parseCount(form.pidsLimit);
		if (Number.isNaN(limit) || limit > MAX_PIDS_LIMIT) {
			add(
				"pidsLimit",
				`The limit is 0 (none) or 1 to ${MAX_PIDS_LIMIT} processes and threads.`,
			);
		}
	}

	// Hostname.
	const hostname = form.hostname.trim().toLowerCase();
	if (hostname !== "" && !HOSTNAME_LABEL.test(hostname)) {
		add(
			"hostname",
			"The hostname is one label: letters, digits and dashes, at most 63 characters, starting and ending with a letter or digit.",
		);
	}

	// DNS servers.
	const resolvers = form.dns.filter((value) => !blank(value));
	if (resolvers.length > MAX_DOCKER_DNS) {
		add("dns", `At most ${MAX_DOCKER_DNS} DNS servers.`);
	}
	const seenResolvers = new Set<string>();
	for (const [index, raw] of form.dns.entries()) {
		if (blank(raw)) continue;
		const { problem, key } = resolverProblem(raw.trim());
		if (problem) add("dns", problem, index);
		else if (seenResolvers.has(key)) {
			add("dns", `${raw.trim()} is listed twice.`, index);
		}
		seenResolvers.add(key);
	}

	// Sysctls.
	const sysctls = form.sysctls.filter((row) => !pairBlank(row));
	if (sysctls.length > MAX_DOCKER_SYSCTLS) {
		add("sysctls", `At most ${MAX_DOCKER_SYSCTLS} sysctls.`);
	}
	const allowedSysctls = policy?.allowedSysctls ?? [];
	const hostKeys = policy?.hostSysctlKeys ?? [];
	const seenSysctls = new Set<string>();
	for (const [index, row] of form.sysctls.entries()) {
		if (pairBlank(row)) continue;
		const key = row.key.trim();
		const value = row.value.trim();
		if (key === "") {
			add("sysctls", "Pick the sysctl to set.", index);
			continue;
		}
		if (allowedSysctls.length > 0 && !allowedSysctls.includes(key)) {
			add(
				"sysctls",
				hostKeys.includes(key) || key.startsWith("vm.")
					? `${key} changes the host and cannot be set from a container; declare it under host requirements so the plan can warn.`
					: `${key} is not an allowed sysctl.`,
				index,
			);
		} else if (seenSysctls.has(key)) {
			add("sysctls", `${key} is listed twice.`, index);
		} else if (value.length > 64 || !SYSCTL_VALUE.test(value)) {
			add(
				"sysctls",
				"The value is a number, or numbers separated by single spaces, up to 64 characters.",
				index,
			);
		}
		seenSysctls.add(key);
	}

	// Host requirements.
	const seenHostKeys = new Set<string>();
	for (const [index, row] of form.hostSysctls.entries()) {
		if (pairBlank(row)) continue;
		const key = row.key.trim();
		if (key === "") {
			add("hostSysctls", "Pick the kernel parameter.", index);
			continue;
		}
		if (hostKeys.length > 0 && !hostKeys.includes(key)) {
			add("hostSysctls", `${key} is not a supported host parameter.`, index);
		} else if (seenHostKeys.has(key)) {
			add("hostSysctls", `${key} is listed twice.`, index);
		} else {
			const value = parseCount(row.value);
			if (Number.isNaN(value) || value < 1 || value > MAX_HOST_SYSCTL_VALUE) {
				add(
					"hostSysctls",
					`The minimum is a whole number from 1 to ${MAX_HOST_SYSCTL_VALUE}.`,
					index,
				);
			}
		}
		seenHostKeys.add(key);
	}

	// User.
	if (!blank(form.user) && userProblem(form.user.trim())) {
		add(
			"user",
			`The user is uid[:gid]: numbers (up to ${MAX_ID}) or user and group names, like 1000:1000 or app.`,
		);
	}

	// Supplementary groups.
	const groups = form.groupAdd.filter((value) => !blank(value));
	if (groups.length > MAX_DOCKER_GROUP_ADD) {
		add("groupAdd", `At most ${MAX_DOCKER_GROUP_ADD} supplementary groups.`);
	}
	const seenGroups = new Set<string>();
	for (const [index, raw] of form.groupAdd.entries()) {
		if (blank(raw)) continue;
		const gid = raw.trim();
		const number = Number(gid);
		if (!GROUP_ID.test(gid) || number < 1 || number > 65535) {
			add(
				"groupAdd",
				`${gid} is not a numeric group ID from 1 to 65535.`,
				index,
			);
		} else if (seenGroups.has(gid)) {
			add("groupAdd", `${gid} is listed twice.`, index);
		}
		seenGroups.add(gid);
	}

	// Ulimits.
	const seenLimits = new Set<string>();
	for (const [index, row] of form.ulimits.entries()) {
		if (ulimitBlank(row)) continue;
		if (seenLimits.has(row.name)) {
			add("ulimits", `${row.name} is listed twice.`, index);
		}
		seenLimits.add(row.name);
		const soft = parseLimit(row.soft);
		const hard = parseLimit(row.hard);
		if (Number.isNaN(soft) || Number.isNaN(hard)) {
			add(
				"ulimits",
				"Soft and hard need a number, or -1 or unlimited for no limit.",
				index,
			);
		} else if (
			soft < -1 ||
			hard < -1 ||
			soft > MAX_ULIMIT ||
			hard > MAX_ULIMIT
		) {
			add(
				"ulimits",
				`Soft and hard must be -1 (unlimited) or 0 to ${MAX_ULIMIT}.`,
				index,
			);
		} else if (hard !== -1 && (soft === -1 || soft > hard)) {
			add("ulimits", "Soft must not exceed hard.", index);
		}
	}
	if (
		form.ulimits.filter((row) => !ulimitBlank(row)).length > ULIMIT_NAMES.length
	) {
		add("ulimits", `At most ${ULIMIT_NAMES.length} ulimits.`);
	}

	// Shared memory.
	if (!blank(form.shmSizeMb)) {
		const size = parseCount(form.shmSizeMb);
		if (Number.isNaN(size) || size < 1 || size > MAX_SIZE_MB) {
			add("shmSizeMb", `Shared memory must be 1 to ${MAX_SIZE_MB} MiB.`);
		}
	}

	// Extra hosts.
	const hosts = form.extraHosts.filter((row) => !hostBlank(row));
	if (hosts.length > MAX_DOCKER_EXTRA_HOSTS) {
		add("extraHosts", `At most ${MAX_DOCKER_EXTRA_HOSTS} extra hosts.`);
	}
	for (const [index, row] of form.extraHosts.entries()) {
		if (hostBlank(row)) continue;
		const name = row.name.trim();
		const address = row.address.trim();
		if (!HOST_NAME.test(name)) {
			add(
				"extraHosts",
				"The host name may use letters, digits, dots, dashes and underscores.",
				index,
			);
		} else if (address !== "host-gateway" && !isIPAddress(address)) {
			add(
				"extraHosts",
				"The address must be an IPv4 or IPv6 address, or host-gateway.",
				index,
			);
		}
	}

	// Labels.
	const labels = form.labels.filter((row) => !labelBlank(row));
	if (labels.length > MAX_DOCKER_LABELS) {
		add("labels", `At most ${MAX_DOCKER_LABELS} labels.`);
	}
	const reserved = policy?.reservedLabels ?? FALLBACK_POLICY.reservedLabels;
	const seenKeys = new Set<string>();
	for (const [index, row] of form.labels.entries()) {
		if (labelBlank(row)) continue;
		const key = row.key.trim();
		if (!LABEL_KEY.test(key)) {
			add(
				"labels",
				"The key starts with a letter or digit and uses letters, digits, dots, dashes, underscores and slashes (up to 128).",
				index,
			);
			continue;
		}
		const prefix = reserved.find((p) => key.toLowerCase().startsWith(p));
		if (prefix) {
			add(
				"labels",
				`${prefix}* labels are managed by NebulaCtrl and cannot be set.`,
				index,
			);
		} else if (seenKeys.has(key)) {
			add("labels", `${key} is listed twice.`, index);
		} else if (row.value.length > 512 || /[\r\n\0]/.test(row.value)) {
			add(
				"labels",
				"The value is at most 512 characters on a single line.",
				index,
			);
		}
		seenKeys.add(key);
	}

	// Stop signal.
	if (!blank(form.stopSignal)) {
		const allowed = policy?.stopSignals ?? FALLBACK_POLICY.stopSignals;
		if (!allowed.includes(form.stopSignal.trim().toUpperCase())) {
			add("stopSignal", `Pick one of ${allowed.join(", ")}.`);
		}
	}

	// Entrypoint.
	if (!blank(form.entrypoint)) {
		const argv = splitCommand(form.entrypoint);
		if (argv.length > MAX_ENTRYPOINT_ARGS) {
			add("entrypoint", `At most ${MAX_ENTRYPOINT_ARGS} elements.`);
		} else if (argv.some((arg) => arg.length > MAX_ENTRYPOINT_ARG_LENGTH)) {
			add(
				"entrypoint",
				`An element is longer than ${MAX_ENTRYPOINT_ARG_LENGTH} characters.`,
			);
		} else if (argv[0] === "") {
			add("entrypoint", "The first element must be the program to run.");
		} else if (form.entrypoint.includes("\0")) {
			add("entrypoint", "The entrypoint cannot contain a NUL character.");
		}
	}

	// tmpfs.
	const mounts = form.tmpfs.filter((row) => !tmpfsBlank(row));
	if (mounts.length > MAX_DOCKER_TMPFS) {
		add("tmpfs", `At most ${MAX_DOCKER_TMPFS} tmpfs mounts.`);
	}
	const filePaths = new Set((context.filePaths ?? []).map(cleanPath));
	const seenPaths = new Set<string>();
	for (const [index, row] of form.tmpfs.entries()) {
		if (tmpfsBlank(row)) continue;
		const path = row.path.trim();
		const clean = cleanPath(path);
		if (
			!path.startsWith("/") ||
			clean !== path ||
			path === "/" ||
			/[,: \0]/.test(path)
		) {
			add(
				"tmpfs",
				"The path must be absolute and clean, without spaces, commas or colons.",
				index,
			);
			continue;
		}
		if (RESERVED_TMPFS.some((r) => path === r || path.startsWith(`${r}/`))) {
			add("tmpfs", `${path} is reserved by the system.`, index);
		} else if (seenPaths.has(path)) {
			add("tmpfs", `${path} is listed twice.`, index);
		} else if (filePaths.has(path)) {
			add("tmpfs", `${path} is also a file mount.`, index);
		}
		seenPaths.add(path);
		if (!blank(row.sizeMb)) {
			const size = parseCount(row.sizeMb);
			if (Number.isNaN(size) || size < 1 || size > MAX_SIZE_MB) {
				add("tmpfs", `The size must be 1 to ${MAX_SIZE_MB} MiB.`, index);
				continue;
			}
		}
		if (!blank(row.mode) && !TMPFS_MODE.test(row.mode.trim())) {
			add(
				"tmpfs",
				"The mode is octal with three or four digits, like 1777.",
				index,
			);
		}
	}

	// Devices.
	const devices = form.devices.filter((row) => !deviceBlank(row));
	if (devices.length > MAX_DOCKER_DEVICES) {
		add("devices", `At most ${MAX_DOCKER_DEVICES} devices.`);
	}
	const seenInside = new Set<string>();
	for (const [index, row] of form.devices.entries()) {
		if (deviceBlank(row)) continue;
		const hostPath = row.hostPath.trim();
		const inside = blank(row.containerPath)
			? hostPath
			: row.containerPath.trim();
		const hostProblem = devicePathProblem(hostPath);
		if (hostProblem) {
			add(
				"devices",
				`The host path ${hostProblem}, like /dev/dri/renderD128.`,
				index,
			);
			continue;
		}
		const insideProblem = devicePathProblem(inside);
		if (insideProblem) {
			add("devices", `The container path ${insideProblem}.`, index);
			continue;
		}
		const permissions = row.permissions.trim();
		if (
			permissions !== "" &&
			(!DEVICE_PERMISSIONS.test(permissions) ||
				new Set(permissions).size !== permissions.length)
		) {
			add(
				"devices",
				"Permissions are a subset of rwm (read, write, mknod), like rw.",
				index,
			);
			continue;
		}
		if (seenInside.has(inside)) {
			add("devices", `${inside} is mapped twice.`, index);
		}
		seenInside.add(inside);
	}

	// GPUs.
	if (form.gpus.mode === "count") {
		const count = parseCount(form.gpus.count);
		if (Number.isNaN(count) || count < 1 || count > MAX_DOCKER_GPUS) {
			add("gpus", `The number of GPUs is 1 to ${MAX_DOCKER_GPUS}.`);
		}
	}
	if (form.gpus.mode !== "none") {
		const supported =
			policy?.gpuCapabilities ?? FALLBACK_POLICY.gpuCapabilities;
		const seenGpuCaps = new Set<string>();
		for (const raw of form.gpus.capabilities) {
			const cap = raw.trim().toLowerCase();
			if (cap === "") continue;
			if (supported.length > 0 && !supported.includes(cap)) {
				add("gpus", `${cap} is not a supported GPU capability.`);
			} else if (seenGpuCaps.has(cap)) {
				add("gpus", `${cap} is listed twice.`);
			}
			seenGpuCaps.add(cap);
		}
	}

	// Runtime.
	if (!blank(form.runtime) && !RUNTIME_NAME.test(form.runtime.trim())) {
		add(
			"runtime",
			"The runtime is the name of an OCI runtime configured in the Docker daemon, like runsc: lower-case letters, digits, dots, dashes and underscores.",
		);
	}

	return issues;
}

/** The first problem as one line, for the draft's error map; undefined when valid. */
export function dockerError(
	form: DockerForm,
	context: DockerContext = {},
): string | undefined {
	const [first] = validateDockerForm(form, context);
	return first ? `Docker options: ${first.message}` : undefined;
}

// --- warnings ------------------------------------------------------------

/** The capabilities in the form that deserve a note: the warned ones and the ones that need an unrestricted server. */
export function capabilityWarnings(
	form: Pick<DockerForm, "capAdd">,
	policy: DockerPolicy | undefined = FALLBACK_POLICY,
): string[] {
	const warn = policy?.warnCapabilities ?? FALLBACK_POLICY.warnCapabilities;
	const unrestricted =
		policy?.unrestrictedCapabilities ??
		FALLBACK_POLICY.unrestrictedCapabilities;
	const out: string[] = [];
	for (const raw of form.capAdd) {
		const cap = normalizeCapability(raw);
		if (cap === "SYS_PTRACE" || warn.includes(cap)) {
			out.push(
				cap === "SYS_PTRACE"
					? "SYS_PTRACE lets processes in the container inspect and tamper with each other. Add it only when the application needs it."
					: `${cap} widens what the container may do. Add it only when the application needs it.`,
			);
		}
		if (unrestricted.includes(cap)) {
			out.push(
				`${cap} widens the container's power over the host and works only on an unrestricted server. Prefer a narrower capability.`,
			);
		}
	}
	return [...new Set(out)];
}

/**
 * A read-only root filesystem with no tmpfs: many applications write to /tmp
 * or /run and fail. Undefined when the combination is not there.
 */
export function readOnlyRootfsWarning(
	form: Pick<DockerForm, "readOnlyRootfs" | "tmpfs">,
): string | undefined {
	if (!form.readOnlyRootfs || form.tmpfs.some((row) => !tmpfsBlank(row))) {
		return undefined;
	}
	return "The root filesystem is read-only and no tmpfs is mounted: many applications write to /tmp or /run and will fail. Add tmpfs mounts or volumes for the paths that need writing.";
}

/**
 * The config files a read-only root filesystem cannot take: each file must sit
 * on one of the service's volumes (a tmpfs does not work, it would hide the
 * file). Empty when the root is writable.
 */
export function readOnlyRootfsFileProblems(
	form: Pick<DockerForm, "readOnlyRootfs">,
	filePaths: readonly string[],
	volumeMounts: readonly string[],
): string[] {
	if (!form.readOnlyRootfs) return [];
	const mounts = volumeMounts.map(cleanPath);
	return filePaths
		.filter((raw) => raw.trim() !== "")
		.map(cleanPath)
		.filter(
			(file) =>
				!mounts.some((mount) => file === mount || file.startsWith(`${mount}/`)),
		);
}

// --- server errors -------------------------------------------------------

/** The Docker options that need an unrestricted server, as the 422 names them. */
export const REQUIRES_UNRESTRICTED = "requires_unrestricted_server";

/** The fields a `requires_unrestricted_server` error names, empty when it names none. */
export function requiredUnrestrictedFields(error: unknown): string[] {
	if (!isApiError(error) || error.code !== REQUIRES_UNRESTRICTED) return [];
	const fields = error.details?.fields;
	return Array.isArray(fields)
		? fields.filter((field): field is string => typeof field === "string")
		: [];
}

/** The readable form of the options a server's tier refuses, for the form and the plan. */
export function requiresUnrestrictedMessage(fields: readonly string[]): string {
	const list =
		fields.length > 0
			? `: ${joinWords(fields.map(unrestrictedFieldLabel))}`
			: "";
	return `These options need a server an instance admin made Unrestricted${list}. Choose such a server, or remove these options.`;
}

/**
 * The message of a 422 about the Docker options (the validation text of
 * `invalid_config`, or `requires_unrestricted_server` in words), undefined for
 * any other error.
 */
export function dockerErrorMessage(error: unknown): string | undefined {
	const code = errorCode(error);
	if (code === REQUIRES_UNRESTRICTED) {
		return requiresUnrestrictedMessage(requiredUnrestrictedFields(error));
	}
	if (code === "invalid_config") {
		const message = errorMessage(error);
		return /^docker\b/i.test(message) ? message : undefined;
	}
	return undefined;
}
