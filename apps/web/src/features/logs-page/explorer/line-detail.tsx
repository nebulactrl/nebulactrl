import { cn } from "cn";
import { HistoryIcon, MinusIcon, PlusIcon, XIcon } from "lucide-react";
import { useMemo } from "react";
import type { LogLine } from "#/api/sse";
import { CopyButton } from "#/components/common/copy-button";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { STAMP_LOCALE } from "#/features/logs/history/model";
import {
	type FacetMode,
	facetTermState,
	toggleFacetTerm,
} from "#/features/logs/search/facets";
import { facetValue, parsedFor } from "#/features/logs/search/line";
import { FACET_KEYS } from "#/features/logs/search/query";
import { formatBytes } from "#/lib/format";
import {
	durationClass,
	formatRequestDuration,
	type RequestFields,
	requestFields,
	statusTone,
} from "./request-line";
import { escapeControls, SafeText } from "./safe-text";

export interface LineDetailProps {
	line: LogLine;
	/** The query text in the search box; Filter and Exclude edit it. */
	query: string;
	onQueryChange(next: string): void;
	onClose(): void;
	/**
	 * For a request line: show the logs around it (History, ±30 s, its service).
	 * Without it the button is left out.
	 */
	onShowAround?: () => void;
	className?: string;
}

interface Field {
	/** As the reader knows it: `level`, `@duration_ms`. */
	key: string;
	value: string;
}

/**
 * What a line says about itself: its built-in facets (service, level, stream,
 * replica, status, deployment) first, then every attribute of its JSON or
 * logfmt. A built-in's key is the facet key (`level:error`), an attribute's is
 * `@path`; the attribute that is a facet already (`status`) is listed once.
 */
function fieldsOf(line: LogLine): Field[] {
	const parsed = parsedFor(line);
	const fields: Field[] = [];
	for (const key of FACET_KEYS) {
		const value = facetValue(parsed, key);
		if (value !== undefined) fields.push({ key, value });
	}
	for (const [path, value] of parsed.flat) {
		if ((FACET_KEYS as readonly string[]).includes(path)) continue;
		fields.push({ key: `@${path}`, value: String(value) });
	}
	return fields;
}

function ModeButton({
	mode,
	field,
	query,
	onQueryChange,
}: {
	mode: FacetMode;
	field: Field;
	query: string;
	onQueryChange(next: string): void;
}) {
	const pressed = facetTermState(query, field.key, field.value) === mode;
	const word = mode === "include" ? "Filter" : "Exclude";
	const Icon = mode === "include" ? PlusIcon : MinusIcon;
	return (
		<button
			type="button"
			aria-pressed={pressed}
			aria-label={`${word} ${escapeControls(`${field.key}:${field.value}`)}`}
			title={`${word} ${escapeControls(`${field.key}:${field.value}`)}`}
			onClick={() =>
				onQueryChange(toggleFacetTerm(query, field.key, field.value, mode))
			}
			className={cn(
				"grid size-6 shrink-0 place-items-center rounded-tip text-ink-3 outline-none hover:bg-white/10 hover:text-ink-1 focus-visible:focus-ring",
				pressed && "bg-accent-wash text-accent-ink ring-1 ring-accent-ink/60",
			)}
		>
			<Icon aria-hidden className="size-3.5" />
		</button>
	);
}

/**
 * What a request says in one glance: `METHOD status path`, then its duration,
 * size and time, and a button to see what its service logged around it.
 */
function RequestSummary({
	request,
	ts,
	onShowAround,
}: {
	request: RequestFields;
	ts: string | undefined;
	onShowAround?: () => void;
}) {
	const { method, host, path, status, durationMs, bytes } = request;
	const time = ts ? new Date(ts) : null;
	const facts = [
		durationMs === undefined ? null : (
			<span key="duration" className={durationClass(durationMs)}>
				{formatRequestDuration(durationMs)}
			</span>
		),
		bytes === undefined ? null : <span key="bytes">{formatBytes(bytes)}</span>,
		time && !Number.isNaN(time.getTime()) ? (
			<span key="time">
				{time.toLocaleTimeString(STAMP_LOCALE, { hour12: false })}
			</span>
		) : null,
	].filter((fact) => fact !== null);
	return (
		<section aria-label="Request" className="flex flex-col gap-1.5">
			<p className="flex items-start gap-2 font-mono text-ink-1 text-xs">
				<span className="shrink-0 font-semibold">
					<SafeText text={method} />
				</span>
				{status === undefined ? null : (
					<Badge tone={statusTone(status)} className="shrink-0 tabular-nums">
						{status}
					</Badge>
				)}
				<span className="min-w-0 break-all">
					<SafeText text={path} />
				</span>
			</p>
			{host ? (
				<p className="break-all font-mono text-ink-3 text-xs">
					<SafeText text={host} />
				</p>
			) : null}
			{facts.length > 0 ? (
				<p className="flex flex-wrap items-center gap-x-1.5 font-mono text-ink-3 text-xs tabular-nums">
					{facts.map((fact, index) => (
						<span key={fact.key} className="flex items-center gap-1.5">
							{index > 0 ? <span aria-hidden>·</span> : null}
							{fact}
						</span>
					))}
				</p>
			) : null}
			{onShowAround ? (
				<Button
					variant="secondary"
					size="sm"
					className="w-fit"
					onClick={onShowAround}
				>
					<HistoryIcon aria-hidden />
					Show logs around this request
				</Button>
			) : null}
		</section>
	);
}

/**
 * The right-hand pane of the explorer for one line: the raw line with a copy
 * button, then every field as a key and a value, each with Filter (+) and
 * Exclude (−) that add the term to the search box (a second click takes it
 * out again). The pane belongs to the explorer; closing it is Escape or the
 * close button.
 */
export function LineDetail({
	line,
	query,
	onQueryChange,
	onClose,
	onShowAround,
	className,
}: LineDetailProps) {
	const parsed = parsedFor(line);
	const fields = useMemo(() => fieldsOf(line), [line]);
	const request = line.stream === "requests" ? requestFields(parsed) : null;
	return (
		<aside
			aria-label="Log line detail"
			className={cn(
				"surface-well flex w-[360px] shrink-0 flex-col overflow-hidden rounded-card",
				className,
			)}
		>
			<header className="flex shrink-0 items-center gap-2 border-line-faint border-b py-1.5 pr-1.5 pl-3">
				<h2 className="min-w-0 flex-1 truncate font-medium text-ink-1 text-xs">
					Log line
					{line.ts ? (
						<span className="ml-2 font-mono font-normal text-ink-3 tabular-nums">
							{line.ts}
						</span>
					) : null}
				</h2>
				<Button
					variant="ghost"
					size="icon-sm"
					aria-label="Close detail"
					title="Close (Esc)"
					onClick={onClose}
				>
					<XIcon aria-hidden />
				</Button>
			</header>
			<div className="flex min-h-0 flex-1 flex-col gap-3 overflow-auto overscroll-contain p-3">
				{request ? (
					<RequestSummary
						request={request}
						ts={line.ts}
						{...(onShowAround ? { onShowAround } : {})}
					/>
				) : null}
				<section aria-label="Raw line" className="flex flex-col gap-1.5">
					<div className="flex items-center justify-between">
						<h3 className="font-medium text-ink-2 text-xs">Raw line</h3>
						<CopyButton
							value={line.line}
							label="Copy raw line"
							variant="ghost"
							size="icon-sm"
						/>
					</div>
					<pre className="max-h-48 overflow-auto overscroll-contain whitespace-pre-wrap break-all rounded-ctl bg-black/20 p-2 font-mono text-[12px] text-ink-1 leading-snug">
						<SafeText text={parsed.text} layout />
					</pre>
				</section>
				<section aria-label="Fields" className="flex flex-col gap-1">
					<h3 className="font-medium text-ink-2 text-xs">Fields</h3>
					{fields.length === 0 ? (
						<p className="text-ink-3 text-xs leading-snug">
							This line states no fields. JSON and key=value lines show theirs
							here.
						</p>
					) : (
						<ul className="flex flex-col">
							{fields.map((field) => (
								<li
									key={field.key}
									className="flex items-start gap-1.5 rounded-ctl py-0.5 pl-1 hover:bg-white/5"
								>
									<span className="w-[104px] shrink-0 break-all pt-0.5 font-mono text-ink-3 text-xs">
										<SafeText text={field.key} />
									</span>
									<span className="min-w-0 flex-1 break-all pt-0.5 font-mono text-ink-1 text-xs">
										<SafeText text={field.value} />
									</span>
									<ModeButton
										mode="include"
										field={field}
										query={query}
										onQueryChange={onQueryChange}
									/>
									<ModeButton
										mode="exclude"
										field={field}
										query={query}
										onQueryChange={onQueryChange}
									/>
								</li>
							))}
						</ul>
					)}
				</section>
			</div>
		</aside>
	);
}
