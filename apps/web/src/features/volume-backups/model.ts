import { formatBytes } from "#/lib/format";

// Pure helpers of the volume backups UI: copy, the config form's draft and its
// validation, the history line of a run, the restore dialog's state and the
// path helpers of the file browser. No React, no API client: the shapes below
// are the fields of the API views these functions read.

/** How the service is treated while its files are copied. */
export type VolumeMode = "freeze" | "stop" | "live";

export interface ModeOption {
	value: VolumeMode;
	label: string;
	description: string;
}

export const MODE_OPTIONS: readonly ModeOption[] = [
	{
		value: "freeze",
		label: "Brief freeze (recommended)",
		description:
			"Requests pause for about a second while the files are copied locally, then the service continues. Connections are kept; nothing is dropped. The upload happens afterwards, in the background.",
	},
	{
		value: "stop",
		label: "Stop during backup",
		description:
			"The service stops, files are copied, then it starts again. Connections are reset and the service is unavailable until it is back. Use this for apps that cannot be paused safely.",
	},
	{
		value: "live",
		label: "Keep running, no freeze",
		description:
			"Files are read while the app keeps writing. The backup can contain files from different moments. Only use this for static uploads, or together with a before-backup command that produces a consistent file.",
	},
];

/** The label of a mode as the history shows it; unknown values pass through. */
export function modeLabel(mode: string): string {
	const option = MODE_OPTIONS.find((o) => o.value === mode);
	if (!option) return mode;
	return option.label.replace(" (recommended)", "");
}

export const FREEZE_BUDGET_DEFAULT = 5;
export const FREEZE_BUDGET_MIN = 1;
export const FREEZE_BUDGET_MAX = 60;
export const HOOK_TIMEOUT_DEFAULT = 300;
export const HOOK_TIMEOUT_MAX = 3600;

/** What a local destination does and does not protect against. */
export const LOCAL_DESTINATION_WARNING =
	"Stored on the same server. This does not protect against losing the server.";

export const ENCRYPTION_NOTE =
	"Backups are encrypted on the server before upload with a key unique to this service. NebulaCtrl stores the key sealed with your secret key. Download the recovery key: without it, or without the secret key, backups cannot be restored.";

/** Copy-paste starting points for a hook. Shown as templates, never run for you. */
export interface HookTemplate {
	id: "sqlite" | "valkey";
	label: string;
	command: string;
	/** What to know before using it. */
	note: string;
}

export const HOOK_TEMPLATES: readonly HookTemplate[] = [
	{
		id: "sqlite",
		label: "SQLite",
		command: `sqlite3 /data/app.db ".backup '/data/.nc-backup/app.db'"`,
		note: "Needs sqlite3 in the image and an existing /data/.nc-backup folder. Adjust the paths to your volume.",
	},
	{
		id: "valkey",
		label: "Valkey or Redis",
		command: "redis-cli BGSAVE",
		note: "Starts a snapshot in the background; it may not be finished when the freeze starts. Needs redis-cli (or valkey-cli) in the image.",
	},
];

// ---------------------------------------------------------------------------
// Command lines

/**
 * Splits a command line into an argument list the way a POSIX shell reads
 * quotes: single quotes are literal, double quotes allow `\"` and `\\`, a
 * backslash escapes the next character outside quotes. Nothing is expanded and
 * no shell runs it: the result is the argv the agent execs. Returns null for an
 * unterminated quote.
 */
export function splitCommand(line: string): string[] | null {
	const args: string[] = [];
	let current = "";
	let inArg = false;
	let quote: "'" | '"' | null = null;
	for (let i = 0; i < line.length; i++) {
		const c = line.charAt(i);
		if (quote === "'") {
			if (c === "'") quote = null;
			else current += c;
			continue;
		}
		if (quote === '"') {
			if (c === '"') quote = null;
			else if (c === "\\" && (line[i + 1] === '"' || line[i + 1] === "\\")) {
				current += line.charAt(i + 1);
				i++;
			} else current += c;
			continue;
		}
		if (c === "'" || c === '"') {
			quote = c;
			inArg = true;
		} else if (c === "\\" && i + 1 < line.length) {
			current += line.charAt(i + 1);
			inArg = true;
			i++;
		} else if (/\s/.test(c)) {
			if (inArg) {
				args.push(current);
				current = "";
				inArg = false;
			}
		} else {
			current += c;
			inArg = true;
		}
	}
	if (quote !== null) return null;
	if (inArg) args.push(current);
	return args;
}

const BARE_ARG = /^[\w@%+=:,./-]+$/;

/** The inverse of `splitCommand`: quotes only the arguments that need it. */
export function joinCommand(args: readonly string[]): string {
	return args
		.map((a) => {
			if (a !== "" && BARE_ARG.test(a)) return a;
			if (!a.includes("'")) return `'${a}'`;
			return `"${a.replace(/["\\]/g, "\\$&")}"`;
		})
		.join(" ");
}

// ---------------------------------------------------------------------------
// The config form

/** One hook as the form edits it. */
export interface HookDraft {
	command: string;
	user: string;
	timeout: string;
	continueOnError: boolean;
}

export const EMPTY_HOOK: HookDraft = {
	command: "",
	user: "",
	timeout: String(HOOK_TIMEOUT_DEFAULT),
	continueOnError: false,
};

/** The schedule choices of the form: "manual" is no cron at all. */
export type ScheduleChoice =
	| "manual"
	| "hourly"
	| "daily"
	| "weekly"
	| "custom";

export const SCHEDULE_CHOICES: readonly {
	id: ScheduleChoice;
	label: string;
	cron: string;
}[] = [
	{ id: "manual", label: "Manual only", cron: "" },
	{ id: "hourly", label: "Hourly", cron: "@hourly" },
	{ id: "daily", label: "Daily 03:00", cron: "0 3 * * *" },
	{ id: "weekly", label: "Weekly Sun 03:00", cron: "0 3 * * 0" },
	{ id: "custom", label: "Custom", cron: "" },
];

/** The choice a stored expression matches. */
export function scheduleChoiceFor(cron: string): ScheduleChoice {
	const trimmed = cron.trim();
	if (trimmed === "") return "manual";
	const hit = SCHEDULE_CHOICES.find(
		(c) => c.id !== "manual" && c.id !== "custom" && c.cron === trimmed,
	);
	return hit?.id ?? "custom";
}

export interface ConfigDraft {
	destinationId: string;
	mode: VolumeMode;
	freezeBudget: string;
	preHook: HookDraft;
	postHook: HookDraft;
	/** One pattern per line. */
	excludes: string;
	keepDaily: string;
	keepWeekly: string;
	keepMonthly: string;
	schedule: ScheduleChoice;
	cron: string;
	timezone: string;
	enabled: boolean;
}

/** The fields of the config view the form starts from. */
export interface ConfigLike {
	destinationId?: string | undefined;
	mode: string;
	freezeBudgetSeconds: number;
	preHook?: HookLike | undefined;
	postHook?: HookLike | undefined;
	excludes: readonly string[] | null;
	keepDaily: number;
	keepWeekly: number;
	keepMonthly: number;
	cron: string;
	timezone: string;
	enabled: boolean;
}

export interface HookLike {
	command: readonly string[] | null;
	user?: string | undefined;
	timeoutSeconds?: number | undefined;
	continueOnError?: boolean | undefined;
}

function hookDraft(hook: HookLike | undefined): HookDraft {
	if (!hook?.command || hook.command.length === 0) return { ...EMPTY_HOOK };
	return {
		command: joinCommand(hook.command),
		user: hook.user ?? "",
		timeout: String(hook.timeoutSeconds ?? HOOK_TIMEOUT_DEFAULT),
		continueOnError: hook.continueOnError ?? false,
	};
}

function asMode(mode: string): VolumeMode {
	return mode === "stop" || mode === "live" ? mode : "freeze";
}

/** The form's draft for a stored (or default) config; `fallbackDestination` fills an unset destination. */
export function draftFromConfig(
	config: ConfigLike,
	fallbackDestination = "",
): ConfigDraft {
	return {
		destinationId: config.destinationId ?? fallbackDestination,
		mode: asMode(config.mode),
		freezeBudget: String(config.freezeBudgetSeconds || FREEZE_BUDGET_DEFAULT),
		preHook: hookDraft(config.preHook),
		postHook: hookDraft(config.postHook),
		excludes: (config.excludes ?? []).join("\n"),
		keepDaily: String(config.keepDaily),
		keepWeekly: String(config.keepWeekly),
		keepMonthly: String(config.keepMonthly),
		schedule: scheduleChoiceFor(config.cron),
		cron: config.cron,
		timezone: config.timezone || "UTC",
		enabled: config.cron.trim() === "" ? true : config.enabled,
	};
}

/** Exclude patterns from the textarea: trimmed, no blanks, no repeats. */
export function parseExcludes(text: string): string[] {
	const seen = new Set<string>();
	for (const line of text.split("\n")) {
		const pattern = line.trim();
		if (pattern !== "") seen.add(pattern);
	}
	return [...seen];
}

/** The wire shape of a hook (`VolumeBackupHook`). */
export interface HookInput {
	command: string[];
	user?: string;
	timeoutSeconds?: number;
	continueOnError: boolean;
}

/** The wire shape of the config (`VolumeBackupConfigInput`). */
export interface ConfigInput {
	destinationId: string;
	mode: VolumeMode;
	freezeBudgetSeconds: number;
	preHook?: HookInput;
	postHook?: HookInput;
	excludes: string[];
	keepDaily: number;
	keepWeekly: number;
	keepMonthly: number;
	cron: string;
	timezone: string;
	enabled: boolean;
}

export type ConfigField =
	| "destination"
	| "freezeBudget"
	| "preHook"
	| "preHookTimeout"
	| "postHook"
	| "postHookTimeout"
	| "keepDaily"
	| "keepWeekly"
	| "keepMonthly"
	| "cron";

export type ConfigResult =
	| { ok: true; input: ConfigInput }
	| { ok: false; errors: Partial<Record<ConfigField, string>> };

function wholeNumber(text: string): number | null {
	const trimmed = text.trim();
	if (!/^\d+$/.test(trimmed)) return null;
	return Number.parseInt(trimmed, 10);
}

function hookInput(
	draft: HookDraft,
	field: "preHook" | "postHook",
	errors: Partial<Record<ConfigField, string>>,
): HookInput | undefined {
	if (draft.command.trim() === "") return undefined;
	const command = splitCommand(draft.command);
	if (command === null) {
		errors[field] = "A quote is not closed.";
		return undefined;
	}
	if (command.length === 0) return undefined;
	const timeoutField = `${field}Timeout` as const;
	const timeout = wholeNumber(draft.timeout);
	if (timeout === null || timeout < 1 || timeout > HOOK_TIMEOUT_MAX) {
		errors[timeoutField] = `Enter 1 to ${HOOK_TIMEOUT_MAX} seconds.`;
		return undefined;
	}
	const user = draft.user.trim();
	return {
		command,
		...(user === "" ? {} : { user }),
		timeoutSeconds: timeout,
		continueOnError: draft.continueOnError,
	};
}

/** Validates the draft and builds the request body, or says what is wrong per field. */
export function buildConfigInput(draft: ConfigDraft): ConfigResult {
	const errors: Partial<Record<ConfigField, string>> = {};
	if (draft.destinationId === "") errors.destination = "Choose a destination.";

	const budget = wholeNumber(draft.freezeBudget);
	if (
		budget === null ||
		budget < FREEZE_BUDGET_MIN ||
		budget > FREEZE_BUDGET_MAX
	)
		errors.freezeBudget = `Enter ${FREEZE_BUDGET_MIN} to ${FREEZE_BUDGET_MAX} seconds.`;

	const keeps = {
		keepDaily: wholeNumber(draft.keepDaily),
		keepWeekly: wholeNumber(draft.keepWeekly),
		keepMonthly: wholeNumber(draft.keepMonthly),
	};
	for (const key of ["keepDaily", "keepWeekly", "keepMonthly"] as const) {
		if (keeps[key] === null) errors[key] = "Enter 0 or more; 0 means no limit.";
	}

	const preHook = hookInput(draft.preHook, "preHook", errors);
	const postHook = hookInput(draft.postHook, "postHook", errors);

	const manual = draft.schedule === "manual";
	const cron = manual ? "" : draft.cron.trim();
	if (!manual && cron === "") errors.cron = "Enter a cron expression.";

	if (
		Object.keys(errors).length > 0 ||
		budget === null ||
		keeps.keepDaily === null ||
		keeps.keepWeekly === null ||
		keeps.keepMonthly === null
	) {
		return { ok: false, errors };
	}
	return {
		ok: true,
		input: {
			destinationId: draft.destinationId,
			mode: draft.mode,
			freezeBudgetSeconds: budget,
			...(preHook ? { preHook } : {}),
			...(postHook ? { postHook } : {}),
			excludes: parseExcludes(draft.excludes),
			keepDaily: keeps.keepDaily,
			keepWeekly: keeps.keepWeekly,
			keepMonthly: keeps.keepMonthly,
			cron,
			timezone: draft.timezone.trim() || "UTC",
			enabled: manual ? false : draft.enabled,
		},
	};
}

/** True when the draft differs from the one the stored config produces. */
export function isDirty(draft: ConfigDraft, saved: ConfigDraft): boolean {
	return JSON.stringify(draft) !== JSON.stringify(saved);
}

/** One line on the schedule: "Every day at 03:00 · UTC", or manual only. */
export function scheduleLine(
	cron: string,
	timezone: string,
	describe: (cron: string) => string,
): string {
	if (cron.trim() === "") return "Manual only";
	return `${describe(cron)} · ${timezone || "UTC"}`;
}

/** "Keep 7 daily, 4 weekly, 6 monthly", with 0 left out; "Keep everything" when none. */
export function describeKeep(
	daily: number,
	weekly: number,
	monthly: number,
): string {
	const parts = [
		daily > 0 ? `${daily} daily` : "",
		weekly > 0 ? `${weekly} weekly` : "",
		monthly > 0 ? `${monthly} monthly` : "",
	].filter((p) => p !== "");
	return parts.length === 0 ? "Keep everything" : `Keep ${parts.join(", ")}`;
}

// ---------------------------------------------------------------------------
// History

/** "0.6 s", "12 s": how long the service was paused or stopped. */
export function formatPause(ms: number): string {
	const seconds = Math.max(0, ms) / 1000;
	if (seconds >= 10) return `${Math.round(seconds)} s`;
	return `${seconds.toFixed(1)} s`;
}

/** The fields of a backup the history line reads. */
export interface BackupLike {
	status: string;
	mode: string;
	consistency?: string | undefined;
	freezeMs: number;
	sizeBytes: number;
	bytesAdded: number;
	files: number;
	hookOutcome?: string | undefined;
	error?: string | undefined;
}

/**
 * The sentence under a run: what it did, how long the service was held and how
 * much went over the wire. In-flight, failed and skipped runs say so plainly.
 */
export function backupSummary(backup: BackupLike): string {
	switch (backup.status) {
		case "queued":
			return "Waiting to start";
		case "running":
			return "Backing up…";
		case "failed":
			return backup.error || "Failed";
		case "skipped":
			return backup.error || "Skipped";
		case "expired":
			return "Not delivered: server offline. No changes were made.";
		case "succeeded":
			break;
		default:
			return backup.status;
	}
	const used = backup.consistency || backup.mode;
	let held = "";
	if (used === "freeze") held = `paused ${formatPause(backup.freezeMs)}`;
	else if (used === "stop") held = `stopped ${formatPause(backup.freezeMs)}`;
	else if (used === "live") held = "service kept running";
	const uploaded =
		backup.bytesAdded <= 0
			? "nothing new to upload"
			: `uploaded ${formatBytes(backup.bytesAdded)}`;
	const dedup =
		backup.sizeBytes > 0 && backup.bytesAdded < backup.sizeBytes
			? " (deduplicated)"
			: "";
	return `Done — ${held === "" ? "" : `${held}, `}${uploaded}${dedup}`;
}

/** Notes beside a run: a before or after command that failed but was allowed to. */
export function hookNote(backup: Pick<BackupLike, "hookOutcome">): string {
	return backup.hookOutcome === "failed_continued"
		? "A command failed; the backup went on as you allowed."
		: "";
}

/** "1 volume", "2 volumes: data, uploads". */
export function volumesLine(volumes: readonly string[]): string {
	if (volumes.length === 0) return "";
	if (volumes.length === 1) return `Volume ${volumes[0]}`;
	return `${volumes.length} volumes: ${volumes.join(", ")}`;
}

/** "a1b2c3d4": the short id of a restic snapshot. */
export function shortSnapshotId(id: string | undefined): string {
	return id ? id.slice(0, 8) : "";
}

const MONTHS = [
	"Jan",
	"Feb",
	"Mar",
	"Apr",
	"May",
	"Jun",
	"Jul",
	"Aug",
	"Sep",
	"Oct",
	"Nov",
	"Dec",
];

/** "Jan 12, 03:30" in UTC: the label a restore carries on the staged change. */
export function formatBackupTime(iso: string): string {
	const date = new Date(iso);
	const pad = (n: number) => n.toString().padStart(2, "0");
	return `${MONTHS[date.getUTCMonth()]} ${date.getUTCDate()}, ${pad(date.getUTCHours())}:${pad(date.getUTCMinutes())}`;
}

// ---------------------------------------------------------------------------
// Restore

export type RestoreMode = "new_volume" | "in_place";

export interface RestoreState {
	volume: string;
	mode: RestoreMode;
	/** What the person typed to confirm an in-place restore. */
	confirm: string;
}

/** The volumes a backup holds that the service still has, and the ones it lost. */
export function restorableVolumes(
	inBackup: readonly string[],
	current: readonly string[],
): { available: string[]; missing: string[] } {
	const have = new Set(current);
	return {
		available: inBackup.filter((v) => have.has(v)),
		missing: inBackup.filter((v) => !have.has(v)),
	};
}

/** A fresh dialog: the first volume, into a new volume, nothing typed. */
export function initialRestoreState(
	available: readonly string[],
): RestoreState {
	return { volume: available[0] ?? "", mode: "new_volume", confirm: "" };
}

export function restoreReady(state: RestoreState, slug: string): boolean {
	if (state.volume === "") return false;
	return state.mode === "new_volume" || state.confirm === slug;
}

/** The request body of the restore call. */
export function restoreRequest(state: RestoreState): {
	volume: string;
	mode: RestoreMode;
	confirmSlug?: string;
} {
	return state.mode === "in_place"
		? { volume: state.volume, mode: "in_place", confirmSlug: state.confirm }
		: { volume: state.volume, mode: "new_volume" };
}

/** The lead sentence of the new-volume choice. */
export function newVolumeSentence(iso: string): string {
	return `Restore ${formatBackupTime(iso)} into a new volume. Your current data is kept. Nothing changes until you press Deploy.`;
}

/** What the in-place choice does to the volume. */
export function inPlaceSentence(volume: string): string {
	return `This overwrites the files in ${volume || "the volume"}. A backup of the current state is taken first; if that fails nothing is changed. The service is stopped while the files are replaced.`;
}

/** The staged bar's pointer after a new-volume restore; `iso` is the backup's time when known. */
export function stagedPointer(
	volume: string,
	iso: string | undefined,
	staged: number,
): string {
	const from = iso ? ` from ${formatBackupTime(iso)}` : "";
	const n = Math.max(staged, 1);
	return `Restore of ${volume}${from} is staged: ${n} ${n === 1 ? "change" : "changes"} to apply. Press Deploy · ${n} in the top bar to switch to it.`;
}

/** Restore row words: "Into a new volume" / "In place". */
export function restoreModeLabel(mode: string): string {
	return mode === "in_place" ? "In place" : "Into a new volume";
}

/** Restore statuses as the UI words them, with the tone of the shared pill. */
export function restoreStatusLabel(
	status: string,
	swapStaged: boolean,
): { label: string; tone: "building" | "deploying" | "ok" | "bad" | "off" } {
	switch (status) {
		case "pre_backup":
			return { label: "Safety backup", tone: "deploying" };
		case "restoring":
			return { label: "Restoring", tone: "deploying" };
		case "succeeded":
			return {
				label: swapStaged ? "Restored, waiting for Deploy" : "Restored",
				tone: "ok",
			};
		case "failed":
			return { label: "Failed", tone: "bad" };
		default:
			return { label: status, tone: "off" };
	}
}

// ---------------------------------------------------------------------------
// File browser

/** Absolute path inside a volume: leading slash, no repeats, `.` and `..` resolved, never above the root. */
export function normalizeVolumePath(path: string): string {
	const parts: string[] = [];
	for (const part of path.split("/")) {
		if (part === "" || part === ".") continue;
		if (part === "..") parts.pop();
		else parts.push(part);
	}
	return `/${parts.join("/")}`;
}

export function joinVolumePath(dir: string, name: string): string {
	return normalizeVolumePath(`${dir}/${name}`);
}

export interface Crumb {
	label: string;
	path: string;
}

/** The volume name, then one crumb per directory: `/a/b` gives [vol, a, b]. */
export function breadcrumbs(volume: string, path: string): Crumb[] {
	const crumbs: Crumb[] = [{ label: volume, path: "/" }];
	let at = "/";
	for (const part of normalizeVolumePath(path).split("/").filter(Boolean)) {
		at = joinVolumePath(at, part);
		crumbs.push({ label: part, path: at });
	}
	return crumbs;
}

export interface EntryLike {
	name: string;
	type: string;
	size: number;
}

/** Directories first, then everything else, each by name (case-insensitive, numeric-aware). */
export function sortEntries<T extends EntryLike>(entries: readonly T[]): T[] {
	const rank = (e: T) => (e.type === "dir" ? 0 : 1);
	return [...entries].sort(
		(a, b) =>
			rank(a) - rank(b) ||
			a.name.localeCompare(b.name, undefined, {
				sensitivity: "base",
				numeric: true,
			}),
	);
}

/** A file can be downloaded as it is; a directory as a .zip. Links and special files are shown, not offered. */
export function entryDownload(type: string): "file" | "archive" | null {
	if (type === "file") return "file";
	if (type === "dir") return "archive";
	return null;
}

/** Same-origin URL of the raw download route of a backup's files. */
export function volumeFileUrl(
	backupId: string,
	volume: string,
	path: string,
	archive: boolean,
): string {
	const q = new URLSearchParams({
		volume,
		path: normalizeVolumePath(path),
	});
	if (archive) q.set("archive", "true");
	return `/api/v1/volume-backups/${encodeURIComponent(backupId)}/files/content?${q.toString()}`;
}

/** The notice under a directory the agent cut short. */
export function truncatedNotice(shown: number): string {
	return `Showing the first ${shown.toLocaleString("en-US")} entries of this folder. Download it as a .zip to get everything.`;
}

// ---------------------------------------------------------------------------
// Recovery key

export interface RecoveryKeyLike {
	repository: string;
	password: string;
	note: string;
}

/** The file the recovery key downloads as. */
export function recoveryKeyFile(
	key: RecoveryKeyLike,
	serviceSlug: string,
): { filename: string; text: string } {
	const text = [
		`NebulaCtrl volume backup recovery key for ${serviceSlug}`,
		"",
		`Repository: ${key.repository}`,
		`Password:   ${key.password}`,
		"",
		key.note,
		"",
		"Keep this file somewhere safe and apart from the backups. Anyone with it and access to the repository can read the backed-up files.",
		"",
	].join("\n");
	return {
		filename: `nebulactrl-${serviceSlug}-volume-backup-recovery-key.txt`,
		text,
	};
}
