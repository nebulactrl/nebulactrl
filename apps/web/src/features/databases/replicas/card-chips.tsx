import { CopyIcon, ShieldOffIcon, WaypointsIcon } from "lucide-react";
import type { Service } from "#/api/models";
import { statusKindOfTone } from "#/components/ui/lamp";
import { StatusIcon } from "#/components/ui/status-icon";
import { useNow } from "#/features/canvas/inspector/overview/use-now";
import { formatBytes } from "#/lib/format";
import { type CardChip, cardChip, shownState } from "./model";
import { useReplication } from "./queries";

const KIND_ICON = {
	primary: WaypointsIcon,
	replica: CopyIcon,
	fenced: ShieldOffIcon,
} as const;

/**
 * The canvas chip of a Postgres service in a read-replica group: "Primary · 2
 * replicas", "Replica of <primary>" with its lag, or "Fenced" for the old
 * primary of a promote. A standalone database draws nothing. It reads the
 * same replication view as the Replicas panel (one cached request each), and
 * is not interactive, so it never fights node selection or dragging.
 */
export function ReplicaChip({ service }: { service: Service }) {
	const managed = service.config.database?.engine === "postgres";
	const view = useReplication(service.id, managed);
	const now = useNow(15_000);
	const data = view.data;
	if (!managed || !data || data.role === "standalone") return null;

	const rows = data.replicas ?? [];
	const self = data.self;
	const state =
		data.role === "replica" && self
			? shownState(self.state, self.observedAt, now)
			: null;
	const chip = cardChip({
		role: data.role,
		replicaCount: rows.length,
		primarySlug: data.primary?.slug ?? null,
		state,
	});
	// A primary shows the worst thing its replicas say.
	const worst =
		data.role === "primary"
			? rows
					.map((row) => shownState(row.state, row.observedAt, now))
					.find(
						(s) => s === "broken" || s === "needs_rebuild" || s === "failed",
					)
			: undefined;
	const lag =
		data.role === "replica" && self?.lagBytes != null && state === "streaming"
			? self.lagBytes === 0
				? "in sync"
				: formatBytes(self.lagBytes)
			: null;
	return <ChipView chip={chip} warn={worst !== undefined} lag={lag} />;
}

function ChipView({
	chip,
	warn,
	lag,
}: {
	chip: CardChip;
	warn: boolean;
	lag: string | null;
}) {
	const Icon = KIND_ICON[chip.kind];
	const tone = warn ? "bad" : chip.tone;
	const title = warn
		? `${chip.title.replace(/\.$/, "")}. A replica needs attention: open the Replicas panel.`
		: chip.title;
	return (
		<div className="mt-2 flex">
			<span
				data-slot="replica-chip"
				data-kind={chip.kind}
				title={title}
				className="inline-flex h-5 min-w-0 max-w-full items-center gap-[5px] rounded-md border border-line-faint bg-white/[0.045] px-[7px] font-mono text-[11px] text-ink-3 leading-none"
			>
				<Icon aria-hidden className="size-3 shrink-0" />
				<span className="truncate">{chip.label}</span>
				{lag ? <span className="shrink-0 text-ink-2">· {lag}</span> : null}
				{tone === "info" ? null : (
					<StatusIcon
						kind={statusKindOfTone(tone)}
						label=""
						className="size-3"
					/>
				)}
				<span className="sr-only">{title}</span>
			</span>
		</div>
	);
}
