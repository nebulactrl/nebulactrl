// Pure parts of a server's log settings: the size fields (an amount and a unit
// on screen, bytes on the wire), their validation against the API's bounds,
// and the usage the agent last reported. Sizes are 1024-based and named MB and
// GB, as the API's own messages name them.

import type {
	LogServerUsage,
	LogSettingsView,
	PutSettingsInputBody,
} from "#/api/gen/types.gen";

export const MB = 1024 * 1024;
export const GB = 1024 * MB;

/** The API's bounds: a cap is at least 32 MB, retention is 1 to 90 days. */
export const MIN_CAP_BYTES = 32 * MB;
export const RETENTION_LIMITS = { min: 1, max: 90 } as const;

export type CapUnit = "MB" | "GB";

export const CAP_UNITS: readonly { value: CapUnit; label: string }[] = [
	{ value: "MB", label: "MB" },
	{ value: "GB", label: "GB" },
];

export interface CapField {
	amount: string;
	unit: CapUnit;
}

export interface LogSettingsDraft {
	capture: boolean;
	retentionDays: string;
	perService: CapField;
	perServer: CapField;
}

const UNIT_BYTES: Record<CapUnit, number> = { MB, GB };

/** A size as the field shows it: whole gigabytes in GB, anything else in MB. */
export function capField(bytes: number): CapField {
	if (bytes >= GB && bytes % GB === 0) {
		return { amount: String(bytes / GB), unit: "GB" };
	}
	return { amount: String(Math.round(bytes / MB)), unit: "MB" };
}

/** The bytes a field means, or null when it is not a positive number. */
export function capBytes(field: CapField): number | null {
	const amount = Number(field.amount);
	if (field.amount.trim() === "" || !Number.isFinite(amount) || amount <= 0) {
		return null;
	}
	return Math.round(amount * UNIT_BYTES[field.unit]);
}

export function draftOf(
	view: Pick<
		LogSettingsView,
		"capture" | "retentionDays" | "maxServiceBytes" | "maxServerBytes"
	>,
): LogSettingsDraft {
	return {
		capture: view.capture,
		retentionDays: String(view.retentionDays),
		perService: capField(view.maxServiceBytes),
		perServer: capField(view.maxServerBytes),
	};
}

export function parseRetention(text: string): number | null {
	const value = Number(text);
	return text.trim() !== "" &&
		Number.isInteger(value) &&
		value >= RETENTION_LIMITS.min &&
		value <= RETENTION_LIMITS.max
		? value
		: null;
}

export interface LogSettingsErrors {
	retentionDays?: string;
	perService?: string;
	perServer?: string;
}

/** What is wrong with each field, by field. The server checks again. */
export function validate(draft: LogSettingsDraft): LogSettingsErrors {
	const errors: LogSettingsErrors = {};
	if (parseRetention(draft.retentionDays) === null) {
		errors.retentionDays = `Keep ${RETENTION_LIMITS.min}–${RETENTION_LIMITS.max} days.`;
	}
	const service = capBytes(draft.perService);
	const server = capBytes(draft.perServer);
	if (service === null) errors.perService = "Enter a size.";
	else if (service < MIN_CAP_BYTES) errors.perService = "At least 32 MB.";
	if (server === null) errors.perServer = "Enter a size.";
	else if (server < MIN_CAP_BYTES) errors.perServer = "At least 32 MB.";
	if (
		service !== null &&
		server !== null &&
		!errors.perService &&
		!errors.perServer &&
		service > server
	) {
		errors.perService = "The per-service limit cannot exceed the server limit.";
	}
	return errors;
}

/** The PUT body of a valid draft; null while a field is wrong. */
export function requestBody(
	draft: LogSettingsDraft,
): PutSettingsInputBody | null {
	const retentionDays = parseRetention(draft.retentionDays);
	const maxServiceBytes = capBytes(draft.perService);
	const maxServerBytes = capBytes(draft.perServer);
	if (
		retentionDays === null ||
		maxServiceBytes === null ||
		maxServerBytes === null ||
		Object.keys(validate(draft)).length > 0
	) {
		return null;
	}
	return {
		capture: draft.capture,
		retentionDays,
		maxServiceBytes,
		maxServerBytes,
	};
}

/** "500 MB", "5 GB", "1.2 GB". */
export function formatCap(bytes: number): string {
	if (bytes >= GB) {
		const gb = Math.round((bytes / GB) * 10) / 10;
		return `${gb} GB`;
	}
	if (bytes > 0 && bytes < MB / 2) return "<1 MB";
	return `${Math.round(bytes / MB)} MB`;
}

/** Share of the server's limit in use, 0 to 100. */
export function usagePercent(usedBytes: number, limitBytes: number): number {
	if (limitBytes <= 0) return 0;
	return Math.min(100, Math.max(0, (usedBytes / limitBytes) * 100));
}

/** "Using 1.2 GB of 5 GB on this server". */
export function usageLine(usedBytes: number, limitBytes: number): string {
	return `Using ${formatCap(usedBytes)} of ${formatCap(limitBytes)} on this server`;
}

export interface UsageRow {
	serviceId: string;
	/** Undefined when no service of the team has this id (it was deleted). */
	name: string | undefined;
	bytes: number;
	lines: number;
	oldest: string | undefined;
}

/** The first eight characters of an id: enough to tell two rows apart. */
export function shortId(id: string): string {
	return id.slice(0, 8);
}

/**
 * The services that use the most space, biggest first. A service no one
 * knows any more (deleted, its logs have not expired yet) has no name.
 */
export function topServices(
	usage: Pick<LogServerUsage, "services"> | undefined,
	names: ReadonlyMap<string, string>,
	limit = 5,
): UsageRow[] {
	return [...(usage?.services ?? [])]
		.sort((a, b) => b.bytes - a.bytes)
		.slice(0, limit)
		.map((service) => ({
			serviceId: service.serviceId,
			name: names.get(service.serviceId),
			bytes: service.bytes,
			lines: service.lines,
			oldest: service.oldest,
		}));
}
