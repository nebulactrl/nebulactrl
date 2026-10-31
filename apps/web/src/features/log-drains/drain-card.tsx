import { FlaskConicalIcon } from "lucide-react";
import type { LogDrainServerStatus, LogDrainView } from "#/api/gen/types.gen";
import { Relative } from "#/components/common/relative";
import { ToneIcon } from "#/components/common/tone-icon";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { Spinner } from "#/components/ui/spinner";
import { StatusIcon } from "#/components/ui/status-icon";
import { Switch } from "#/components/ui/switch";
import { DrainIcon } from "./drain-icon";
import {
	CENTRAL_LOGS_LINE,
	CENTRAL_LOGS_TITLE,
	deliveryParts,
	kindLabel,
	lastErrorOf,
	NEVER_REPORTED,
	scopeSummary,
	serverState,
	type TestOutcome,
	targetLabel,
} from "./model";

/** One server's delivery: its state in words with an icon, what it sent and what waits. */
function ServerHealth({ status }: { status: LogDrainServerStatus }) {
	const state = serverState(status);
	const error = lastErrorOf(status);
	return (
		<li className="flex min-w-0 flex-col gap-0.5">
			<span className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-ink-2 text-xs">
				<StatusIcon kind={state.kind} label="" />
				<span className="text-ink-1">{status.serverName}</span>
				<span>{state.label}</span>
				<span className="font-mono text-2xs text-ink-4 tabular-nums">
					{deliveryParts(status).join(" · ")}
				</span>
			</span>
			{error ? (
				<span
					className="truncate pl-[22px] text-destructive-text text-xs"
					title={error}
				>
					{error}
					{status.lastErrorAt ? (
						<span className="text-ink-4">
							{" "}
							· <Relative iso={status.lastErrorAt} />
						</span>
					) : null}
				</span>
			) : null}
		</li>
	);
}

/** The saved result of the last test, as the notification channels show theirs. */
function LastTest({ drain }: { drain: LogDrainView }) {
	const { ok, at, message } = drain.test;
	if (ok === undefined || ok === null) {
		return (
			<span className="flex items-center gap-2 text-ink-3 text-xs">
				<StatusIcon kind="pending" label="" /> Not tested yet
			</span>
		);
	}
	return (
		<span className="flex min-w-0 flex-col gap-0.5">
			<span className="flex items-center gap-2 text-ink-2 text-xs">
				<StatusIcon kind={ok ? "healthy" : "failed"} label="" />
				{ok ? "Last test line arrived" : "Last test failed"}
				{at ? (
					<span className="text-ink-4">
						· <Relative iso={at} />
					</span>
				) : null}
			</span>
			{ok || message === "" ? null : (
				<span
					className="truncate text-destructive-text text-xs"
					title={message}
				>
					{message}
				</span>
			)}
		</span>
	);
}

/** The result of the test just run, on a tone wash with its words. */
function TestResult({
	outcome,
	onDismiss,
}: {
	outcome: TestOutcome;
	onDismiss(): void;
}) {
	return (
		<div
			role="status"
			data-tone={outcome.tone}
			className="tone-callout relative flex items-start gap-2 rounded-ctl px-2.5 py-2 text-xs leading-snug"
		>
			<ToneIcon tone={outcome.tone} className="mt-px" />
			<span className="min-w-0 flex-1">
				<span className="block font-medium text-ink-1">{outcome.title}</span>
				{outcome.detail ? (
					<span className="block break-words text-ink-3">{outcome.detail}</span>
				) : null}
			</span>
			<Button variant="ghost" size="2xs" onClick={onDismiss}>
				Dismiss
			</Button>
		</div>
	);
}

/**
 * One log drain: what it is and where it ships to, which logs it takes, how
 * each server's delivery is doing, and the actions. The switch pauses it for
 * every server. The built-in central logs drain is the instance's, so it has
 * a switch and a test and nothing to edit or delete.
 */
export function DrainCard({
	drain,
	projectNames,
	testing,
	outcome,
	onToggle,
	onTest,
	onDismissOutcome,
	onEdit,
	onRemove,
}: {
	drain: LogDrainView;
	projectNames: ReadonlyMap<string, string>;
	testing: boolean;
	outcome: TestOutcome | undefined;
	onToggle(enabled: boolean): void;
	onTest(): void;
	onDismissOutcome(): void;
	onEdit(): void;
	onRemove(): void;
}) {
	const servers = drain.servers ?? [];
	const query = drain.query.trim();
	const title = drain.builtin ? CENTRAL_LOGS_TITLE : drain.name;
	return (
		<article
			data-testid="log-drain"
			aria-label={title}
			className="surface-raised flex min-w-0 flex-col gap-3.5 rounded-card p-4"
		>
			<header className="flex items-start gap-3">
				<DrainIcon kind={drain.kind} size={36} />
				<div className="flex min-w-0 flex-1 flex-col gap-0.5">
					<span className="flex items-center gap-2">
						<h3 className="m-0 truncate font-medium text-ink-1 text-sm">
							{title}
						</h3>
						{drain.enabled ? null : (
							<Badge shape="capsule">{drain.builtin ? "off" : "paused"}</Badge>
						)}
					</span>
					<span className="truncate font-mono text-2xs text-ink-3">
						{drain.builtin
							? CENTRAL_LOGS_LINE
							: `${kindLabel(drain.kind)}${targetLabel(drain) ? ` · ${targetLabel(drain)}` : ""}`}
					</span>
				</div>
				<Switch
					size="sm"
					checked={drain.enabled}
					onCheckedChange={onToggle}
					aria-label={
						drain.builtin
							? `${drain.enabled ? "Turn off" : "Turn on"} ${title.toLowerCase()}`
							: `${drain.enabled ? "Pause" : "Resume"} ${drain.name}`
					}
				/>
			</header>
			{drain.builtin ? (
				<p className="m-0 text-ink-2 text-xs leading-relaxed">
					{drain.enabled
						? "Your services' logs from every server are kept for 7 days on the control plane, in this team's own space, and show under Central logs on a service's Logs tab."
						: "Off. Turn it on to collect your services' logs from every server for 7 days. They then show under Central logs on a service's Logs tab."}
				</p>
			) : null}
			<p className="m-0 flex flex-wrap items-center gap-x-2 gap-y-1 text-ink-2 text-xs leading-relaxed">
				<span>{scopeSummary(drain, projectNames)}</span>
				{query ? (
					<>
						<span className="text-ink-4" aria-hidden>
							·
						</span>
						<span className="text-ink-3">Only lines matching</span>
						<code
							className="type-mono max-w-full truncate rounded-md bg-surface-well px-1.5 py-0.5 text-ink-1"
							title={query}
						>
							{query}
						</code>
					</>
				) : null}
			</p>
			<div className="flex flex-col gap-2">
				{servers.length === 0 ? (
					<span className="flex items-center gap-2 text-ink-3 text-xs">
						<StatusIcon kind={NEVER_REPORTED.kind} label="" />
						{NEVER_REPORTED.label}
					</span>
				) : (
					<ul className="m-0 flex list-none flex-col gap-1.5 p-0">
						{servers.map((status) => (
							<ServerHealth key={status.serverId} status={status} />
						))}
					</ul>
				)}
				<LastTest drain={drain} />
			</div>
			{outcome ? (
				<TestResult outcome={outcome} onDismiss={onDismissOutcome} />
			) : null}
			<footer className="-mb-1 flex flex-wrap items-center gap-1 border-line-faint border-t pt-2.5">
				<Button
					variant="secondary"
					size="sm"
					title="Send one test line from a connected server"
					onClick={onTest}
					disabled={testing}
				>
					{testing ? <Spinner /> : <FlaskConicalIcon />}
					{testing ? "Testing…" : "Test"}
				</Button>
				{drain.builtin ? null : (
					<>
						<Button variant="ghost" size="sm" onClick={onEdit}>
							Edit
						</Button>
						<Button
							variant="ghost"
							size="sm"
							className="ml-auto text-destructive-text"
							onClick={onRemove}
						>
							Delete
						</Button>
					</>
				)}
			</footer>
		</article>
	);
}
