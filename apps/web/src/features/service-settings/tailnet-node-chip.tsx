import { GlobeIcon, LockIcon, NetworkIcon } from "lucide-react";
import type { TailnetView } from "#/api/gen/types.gen";
import { StatusIcon } from "#/components/ui/status-icon";
import { tailnetChipInfo } from "./tailnet-chip";

/**
 * The service card's tailnet indicator: the node's hostname, an icon for its
 * state, a "Public" mark when Funnel is on and a lock when public domains are
 * paused. It is not interactive, so it never fights node selection or
 * dragging; the tooltip and the screen reader text carry the state in words,
 * and "Tailnet only" when public domains are paused.
 */
export function TailnetChip({ tailnet }: { tailnet: TailnetView }) {
	const chip = tailnetChipInfo(tailnet);
	return (
		<span
			data-slot="tailnet-chip"
			title={chip.tooltip}
			className="inline-flex h-5 min-w-0 max-w-[132px] items-center gap-[5px] rounded-md border border-line-faint bg-white/[0.045] px-[7px] font-mono text-[11px] text-ink-3 leading-none"
		>
			<NetworkIcon aria-hidden className="size-3 shrink-0" />
			<span className="truncate">{chip.label}</span>
			<StatusIcon kind={chip.kind} label="" className="size-3" />
			{chip.isPublic ? (
				<span className="inline-flex shrink-0 items-center gap-0.5 text-warning-text">
					<GlobeIcon aria-hidden className="size-3" />
					Public
				</span>
			) : chip.tailnetOnly ? (
				<LockIcon aria-hidden className="size-3 shrink-0" />
			) : null}
			<span className="sr-only">{chip.tooltip}</span>
		</span>
	);
}
