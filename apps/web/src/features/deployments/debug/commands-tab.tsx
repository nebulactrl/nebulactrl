import { cn } from "cn";
import { useId, useState } from "react";
import { EmptyState } from "#/components/common/empty-state";
import { ErrorState } from "#/components/common/error-state";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { DisclosureChevron, DisclosurePanel } from "#/components/ui/disclosure";
import { Skeleton } from "#/components/ui/skeleton";
import { StatusIcon, type StatusKind } from "#/components/ui/status-icon";
import { JsonView } from "./json-view";
import type { DebugCommand } from "./queries";
import { clockWithMs, formatOffset } from "./timeline-model";

/** The delivery state of a command as a status icon; the word always accompanies it. */
const STATUS_KIND: Record<string, StatusKind> = {
	pending: "pending",
	sent: "deploying",
	acked: "building",
	succeeded: "live",
	failed: "failed",
	expired: "warning",
	canceled: "stopped",
};

const STATUS_LABEL: Record<string, string> = {
	pending: "Waiting to be sent",
	sent: "Sent, not yet acknowledged",
	acked: "Acknowledged, running",
	succeeded: "Succeeded",
	failed: "Failed",
	expired: "Expired before delivery",
	canceled: "Canceled",
};

function ms(iso: string | null | undefined): number | null {
	if (!iso) return null;
	const value = new Date(iso).getTime();
	return Number.isNaN(value) ? null : value;
}

/** The delivery steps of a command with the wait since the one before. */
function commandTimes(command: DebugCommand) {
	const steps = [
		{ label: "Enqueued", at: command.createdAt },
		{ label: "Sent", at: command.sentAt },
		{ label: "Acknowledged", at: command.ackedAt },
		{ label: "Finished", at: command.finishedAt },
	];
	let previous: number | null = null;
	return steps.map((step) => {
		const at = ms(step.at);
		const since = at !== null && previous !== null ? at - previous : null;
		if (at !== null) previous = at;
		return { ...step, since };
	});
}

function JsonBlock({ title, value }: { title: string; value: unknown }) {
	const [open, setOpen] = useState(false);
	const panelId = useId();
	const buttonId = useId();
	const empty = value === null || value === undefined;
	return (
		<div className="border-line-faint border-t">
			<button
				id={buttonId}
				type="button"
				aria-expanded={open}
				aria-controls={panelId}
				disabled={empty}
				onClick={() => setOpen((current) => !current)}
				className="flex w-full cursor-pointer items-center gap-2 px-3 py-2 text-left text-ink-2 text-xs outline-none transition-[background-color] duration-quick ease-out hover:bg-surface-hover focus-visible:focus-ring disabled:cursor-default disabled:text-ink-4 disabled:hover:bg-transparent"
			>
				<DisclosureChevron open={open} className="size-3.5 text-ink-3" />
				{title}
				{empty ? <span className="text-ink-4">· none yet</span> : null}
			</button>
			<DisclosurePanel open={open} id={panelId} labelledBy={buttonId}>
				<div className="px-3 pb-3">
					<JsonView label={`${title} as JSON`} value={value} />
				</div>
			</DisclosurePanel>
		</div>
	);
}

function CommandCard({ command }: { command: DebugCommand }) {
	const kind = STATUS_KIND[command.status] ?? "unknown";
	return (
		<li className="surface-raised flex min-w-0 flex-col overflow-hidden rounded-card">
			<div className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2.5">
				<span className="font-medium font-mono text-ink-1 text-sm">
					{command.kind}
				</span>
				<span className="flex items-center gap-1.5 text-ink-2 text-xs">
					<StatusIcon kind={kind} label="" />
					{STATUS_LABEL[command.status] ?? command.status}
					{command.outcome && command.outcome !== command.status
						? ` · ${command.outcome.replaceAll("_", " ")}`
						: ""}
				</span>
				<span className="ml-auto text-ink-3 text-xs">
					on <span className="text-ink-2">{command.serverName}</span>
				</span>
			</div>
			<p
				className="px-3 pb-2 font-mono text-[11px] text-ink-4"
				title="Command id"
			>
				{command.id}
			</p>
			<dl className="grid grid-cols-2 gap-x-4 gap-y-2 border-line-faint border-t px-3 py-2.5 sm:grid-cols-4">
				{commandTimes(command).map((step) => (
					<div key={step.label} className="flex min-w-0 flex-col gap-0.5">
						<dt className="type-micro text-ink-3">{step.label}</dt>
						<dd
							className={cn(
								"font-mono text-[11.5px] tabular-nums",
								step.at ? "text-ink-1" : "text-ink-4",
							)}
						>
							{step.at ? clockWithMs(step.at) : "—"}
							{step.since !== null ? (
								<span className="ml-1.5 text-ink-3">
									{formatOffset(step.since)}
								</span>
							) : null}
						</dd>
					</div>
				))}
			</dl>
			{command.error ? (
				<Alert variant="destructive" className="mx-3 mb-3 w-auto">
					<AlertDescription className="whitespace-pre-wrap break-words">
						{command.error}
					</AlertDescription>
				</Alert>
			) : null}
			<JsonBlock title="Payload (redacted)" value={command.payload} />
			<JsonBlock title="Result (redacted)" value={command.result} />
		</li>
	);
}

/**
 * Each agent command of the deployment: what was sent, when it was enqueued,
 * sent, acknowledged and finished (with the wait between), and how it ended.
 * The control plane redacts payloads and results: no secret, token, file
 * content or environment value reaches this tab.
 */
export function CommandsTab({
	commands,
	pending,
	error,
	onRetry,
}: {
	commands: readonly DebugCommand[] | undefined;
	pending: boolean;
	error: boolean;
	onRetry(): void;
}) {
	if (pending) {
		return (
			<div className="flex flex-col gap-3 p-5" aria-busy>
				<Skeleton className="h-28 w-full" />
				<Skeleton className="h-10 w-full" />
			</div>
		);
	}
	if (error || commands === undefined) {
		return (
			<ErrorState
				title="The commands did not load"
				body="Nothing was changed. Try again."
				retry={onRetry}
				className="p-5"
			/>
		);
	}
	if (commands.length === 0) {
		return (
			<EmptyState
				illustration="radar"
				title="No agent commands"
				body="The control plane sends a command to the server when the deployment is dispatched."
				frame="none"
				className="p-5"
			/>
		);
	}
	return (
		<div className="flex min-w-0 flex-col gap-3 p-5">
			<p className="text-ink-3 text-xs">
				Payloads are shown as persisted: variable keys, never values. Tokens,
				credentials and file contents are masked.
			</p>
			<ul aria-label="Agent commands" className="flex flex-col gap-3">
				{commands.map((command) => (
					<CommandCard key={command.id} command={command} />
				))}
			</ul>
		</div>
	);
}
