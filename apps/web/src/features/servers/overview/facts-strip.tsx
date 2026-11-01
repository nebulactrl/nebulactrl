import { cn } from "cn";
import { EYEBROW } from "#/features/metrics/ui/eyebrow";
import { formatBytes } from "#/lib/format";
import { diskTotalBytes, type ServerView } from "../model";

/** What the host reports about itself, on one raised strip: OS, CPUs, memory, disk, Docker and the agent. */
export function FactsStrip({ server }: { server: ServerView }) {
	const facts = server.facts;
	const disk = diskTotalBytes(server);
	const items: [string, string][] = [
		["OS", facts ? `${facts.os} · ${facts.arch}` : "–"],
		["CPUs", facts ? String(facts.cpus) : "–"],
		["Memory", facts ? formatBytes(facts.memTotal) : "–"],
		// The newest heartbeat's size, the one the charts and lists quote.
		["Disk", disk ? formatBytes(disk) : "–"],
		["Docker", facts ? facts.dockerVersion : "–"],
		["Agent", server.agentVersion ?? "not connected"],
	];
	return (
		<dl className="surface-raised grid grid-cols-2 gap-x-4 gap-y-3 rounded-card px-4 py-3 sm:grid-cols-3 lg:grid-cols-6">
			{items.map(([label, value]) => (
				<div key={label} title={value} className="flex min-w-0 flex-col gap-1">
					<dt className={cn(EYEBROW, "truncate")}>{label}</dt>
					<dd className="truncate font-medium font-mono text-ink-1 text-sm tabular-nums">
						{value}
					</dd>
				</div>
			))}
		</dl>
	);
}
