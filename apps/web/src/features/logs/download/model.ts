export type LogFileFormat = "txt" | "ndjson";

export const LOG_FILE_FORMATS: readonly {
	id: LogFileFormat;
	label: string;
}[] = [
	{ id: "txt", label: "Plain text (.txt)" },
	{ id: "ndjson", label: "NDJSON (.ndjson)" },
];

export interface DownloadOptions {
	format: LogFileFormat;
	/** RFC 3339 instants; empty = open. */
	from?: string | undefined;
	to?: string | undefined;
	/** A log search query (service logs only). */
	q?: string | undefined;
	/** Compose stacks: one component's lines. */
	component?: string | undefined;
	/** Services with processes: one process' lines. */
	process?: string | undefined;
}

/** The query of a download request: empty values are left out. */
export function downloadQuery(
	options: DownloadOptions,
): Record<string, string> {
	const query: Record<string, string> = { format: options.format };
	if (options.from) query.from = options.from;
	if (options.to) query.to = options.to;
	if (options.q?.trim()) query.q = options.q.trim();
	if (options.component) query.component = options.component;
	if (options.process) query.process = options.process;
	return query;
}

/** The file name a `Content-Disposition: attachment; filename="…"` header suggests. */
export function filenameFromDisposition(
	header: string | null,
	fallback: string,
): string {
	const match = /filename="([^"\\/]+)"/.exec(header ?? "");
	return match?.[1] ?? fallback;
}
