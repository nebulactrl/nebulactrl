import { cn } from "cn";
import type { LogLine } from "#/api/sse";
import { Badge } from "#/components/ui/badge";
import type { LampTone } from "#/components/ui/lamp";
import { formatValue } from "#/features/logs/search/facets";
import { Highlighted } from "#/features/logs/search/highlight";
import type { ParsedLine } from "#/features/logs/search/line";
import type { HighlightTerm } from "#/features/logs/search/match";
import { escapeControls, SafeText } from "./safe-text";

/** What a request line shows, read from the JSON the server stored as its text. */
export interface RequestFields {
	method: string;
	host: string;
	/** The path, with its query string when the server keeps those. */
	path: string;
	status?: number;
	durationMs?: number;
	process?: string;
	/** page, api, asset or other: what the proxy took the request for. */
	kind?: string;
	/** Bytes sent in the response. */
	bytes?: number;
}

function textOf(value: unknown): string | undefined {
	return typeof value === "string" && value !== "" ? value : undefined;
}

/**
 * The request behind a parsed line: its method, host and path, and the status,
 * duration and process when the line states them. `null` when the line is not
 * a request record (no method or no path), so it is drawn as plain text.
 */
export function requestFields(parsed: ParsedLine): RequestFields | null {
	const { attrs } = parsed;
	const method = textOf(attrs.method);
	const path = textOf(attrs.path);
	if (!method || !path) return null;
	const query = textOf(attrs.query);
	const fields: RequestFields = {
		method,
		host: textOf(attrs.host) ?? "",
		path: query ? `${path}?${query}` : path,
	};
	if (parsed.status !== undefined) fields.status = parsed.status;
	const durationMs = attrs.duration_ms;
	if (typeof durationMs === "number" && Number.isFinite(durationMs)) {
		fields.durationMs = durationMs;
	}
	const process = textOf(attrs.process);
	if (process) fields.process = process;
	const kind = textOf(attrs.kind);
	if (kind) fields.kind = kind;
	const bytes = attrs.bytes;
	if (typeof bytes === "number" && Number.isFinite(bytes)) fields.bytes = bytes;
	return fields;
}

/** 2xx ok, 3xx muted, 4xx warning, 5xx failed. */
export function statusTone(status: number): LampTone {
	if (status >= 500) return "bad";
	if (status >= 400) return "warn";
	if (status >= 300) return "off";
	return "ok";
}

/** "<1 ms", "12 ms", "1.2 s", "2m 05s". */
export function formatRequestDuration(ms: number): string {
	if (ms < 1) return "<1 ms";
	if (ms < 1_000) return `${Math.round(ms)} ms`;
	if (ms < 60_000) return `${(ms / 1_000).toFixed(1)} s`;
	const minutes = Math.floor(ms / 60_000);
	const seconds = Math.floor((ms % 60_000) / 1_000);
	return `${minutes}m ${String(seconds).padStart(2, "0")}s`;
}

/** Slow requests read in colour: from 1 s a warning, from 5 s a failure. The number stays the cue. */
export function durationClass(ms: number): string {
	if (ms >= 5_000) return "text-status-failed-on";
	if (ms >= 1_000) return "text-status-staged-on";
	return "text-ink-3";
}

/**
 * The text column of a request row: method (fixed width), the status as a
 * toned token, host and path (the path in full ink, cut with an ellipsis and
 * whole in the tooltip, host included, so a hidden host is still one hover
 * away), a subtle tag for an asset or other request, the process when there
 * is one, the duration at the right edge. The numbers are the cues; the
 * tones (status, a slow duration) only back them.
 */
export function RequestLine({
	fields,
	terms,
	showHost = true,
}: {
	fields: RequestFields;
	terms: readonly HighlightTerm[];
	/** The host before the path; off when every row in view has the same one. */
	showHost?: boolean;
}) {
	const { method, host, status, durationMs, process, kind } = fields;
	// What the client sent is shown with its control and bidi characters spelled out.
	const path = escapeControls(fields.path);
	return (
		<span className="flex min-w-0 flex-1 items-center gap-3">
			<span className="w-[7ch] shrink-0 font-semibold text-ink-1">
				<SafeText text={method} />
			</span>
			{status === undefined ? (
				<span className="w-[34px] shrink-0" aria-hidden />
			) : (
				<Badge
					tone={statusTone(status)}
					className="w-[34px] shrink-0 py-0.5 tabular-nums"
				>
					{status}
				</Badge>
			)}
			<span
				className="min-w-0 max-w-[56ch] truncate whitespace-pre"
				title={escapeControls(`${host}${path}`)}
			>
				{showHost && host ? (
					<span className="text-ink-4">
						<SafeText text={host} />
					</span>
				) : null}
				<span className="text-ink-1">
					<Highlighted text={path} lower={path.toLowerCase()} terms={terms} />
				</span>
			</span>
			{kind === "asset" || kind === "other" ? (
				<span className="shrink-0 rounded-tip px-1 text-[10px] text-ink-4 leading-4 ring-1 ring-line-faint">
					<SafeText text={kind} />
				</span>
			) : null}
			{process ? (
				<span className="shrink-0 text-ink-4">
					<SafeText text={process} />
				</span>
			) : null}
			<span
				className={cn(
					"ml-auto w-[8ch] shrink-0 text-right tabular-nums",
					durationMs === undefined ? "text-ink-3" : durationClass(durationMs),
				)}
			>
				{durationMs === undefined ? "" : formatRequestDuration(durationMs)}
			</span>
		</span>
	);
}

/** How far either side of a request "Show logs around" reaches. */
const AROUND_WINDOW_MS = 30_000;

/**
 * The search that shows what the same service logged around a line: its time
 * ± 30 s as a custom range, and a query for its service (and replica when it
 * states one). `null` for a line with no readable time or no service name.
 */
export function logsAround(
	line: LogLine,
): { from: string; to: string; query: string } | null {
	const at = line.ts ? Date.parse(line.ts) : Number.NaN;
	if (Number.isNaN(at) || !line.serviceName) return null;
	const terms = [`service:${formatValue(line.serviceName)}`];
	if (line.replica !== undefined && line.replica !== "") {
		terms.push(`replica:${formatValue(String(line.replica))}`);
	}
	return {
		from: new Date(at - AROUND_WINDOW_MS).toISOString(),
		to: new Date(at + AROUND_WINDOW_MS).toISOString(),
		query: terms.join(" "),
	};
}
