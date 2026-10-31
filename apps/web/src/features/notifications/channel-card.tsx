import { SendIcon } from "lucide-react";
import type { ChannelView } from "#/api/gen/types.gen";
import { Relative } from "#/components/common/relative";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { Spinner } from "#/components/ui/spinner";
import { StatusIcon } from "#/components/ui/status-icon";
import { Switch } from "#/components/ui/switch";
import { ChannelIcon } from "./channel-icon";
import { kindLabel, ruleSummary, targetLabel } from "./model";

function TestLine({ channel }: { channel: ChannelView }) {
	const { ok, at, message } = channel.test;
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
				{ok ? "Test message arrived" : "Last test failed"}
				{at ? (
					<span className="text-ink-4">
						· <Relative iso={at} />
					</span>
				) : null}
			</span>
			{ok ? null : (
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

/** One channel: what it is, whom it tells about what, and how it has been doing. */
export function ChannelCard({
	channel,
	projectNames,
	testing,
	onToggle,
	onTest,
	onLog,
	onEdit,
	onRotate,
	onRemove,
}: {
	channel: ChannelView;
	projectNames: ReadonlyMap<string, string>;
	testing: boolean;
	onToggle: (enabled: boolean) => void;
	onTest: () => void;
	onLog: () => void;
	onEdit: () => void;
	onRotate: () => void;
	onRemove: () => void;
}) {
	const { delivered24h, failed24h } = channel.stats;
	const quiet = delivered24h + failed24h === 0;
	return (
		<article
			data-testid="notification-channel"
			aria-label={channel.name}
			className="surface-raised flex min-w-0 flex-col gap-3.5 rounded-card p-4"
		>
			<header className="flex items-start gap-3">
				<ChannelIcon kind={channel.kind} size={36} />
				<div className="flex min-w-0 flex-1 flex-col gap-0.5">
					<span className="flex items-center gap-2">
						<h3 className="m-0 truncate font-medium text-ink-1 text-sm">
							{channel.name}
						</h3>
						{channel.enabled ? null : <Badge shape="capsule">paused</Badge>}
					</span>
					<span className="truncate font-mono text-2xs text-ink-3">
						{kindLabel(channel.kind)} · {targetLabel(channel)}
					</span>
				</div>
				<Switch
					size="sm"
					checked={channel.enabled}
					onCheckedChange={onToggle}
					aria-label={`${channel.enabled ? "Pause" : "Resume"} ${channel.name}`}
				/>
			</header>
			<p className="m-0 text-ink-2 text-xs leading-relaxed">
				{ruleSummary(channel.rule, projectNames)}
			</p>
			<div className="flex flex-col gap-1.5">
				<TestLine channel={channel} />
				<span className="flex items-center gap-2 text-ink-3 text-xs">
					<StatusIcon
						kind={
							failed24h > 0
								? "warning"
								: delivered24h > 0
									? "healthy"
									: "unknown"
						}
						label=""
					/>
					{quiet
						? "Nothing sent in the last 24 hours"
						: `${delivered24h} delivered${failed24h > 0 ? `, ${failed24h} failed` : ""} in the last 24 hours`}
				</span>
			</div>
			<footer className="-mb-1 flex flex-wrap items-center gap-1 border-line-faint border-t pt-2.5">
				<Button
					variant="secondary"
					size="sm"
					onClick={onTest}
					disabled={testing}
				>
					{testing ? <Spinner /> : <SendIcon />}
					{testing ? "Sending…" : "Send test"}
				</Button>
				<Button variant="ghost" size="sm" onClick={onLog}>
					Delivery log
				</Button>
				<Button variant="ghost" size="sm" onClick={onEdit}>
					Edit
				</Button>
				{channel.kind === "webhook" ? (
					<Button variant="ghost" size="sm" onClick={onRotate}>
						Rotate secret
					</Button>
				) : null}
				<Button
					variant="ghost"
					size="sm"
					className="ml-auto text-destructive-text"
					onClick={onRemove}
				>
					Remove
				</Button>
			</footer>
		</article>
	);
}
