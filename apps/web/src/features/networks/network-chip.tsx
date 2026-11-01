import { WaypointsIcon } from "lucide-react";
import type { NetworkRef } from "#/api/gen/types.gen";
import { networkChipLabel, networkChipTitle } from "./model";

/**
 * The service card's shared-network indicator: the network's name, or a count
 * when there are several. Not interactive (it never fights selection or
 * dragging); the tooltip and the screen reader text list every hostname.
 */
export function NetworkChip({ networks }: { networks: readonly NetworkRef[] }) {
	if (networks.length === 0) return null;
	const title = networkChipTitle(networks);
	return (
		<span
			data-slot="network-chip"
			title={title}
			className="inline-flex h-5 min-w-0 max-w-[110px] items-center gap-[5px] rounded-md border border-line-faint bg-white/[0.045] px-[7px] font-mono text-[11px] text-ink-3 leading-none"
		>
			<WaypointsIcon aria-hidden className="size-3 shrink-0" />
			<span className="truncate">{networkChipLabel(networks)}</span>
			<span className="sr-only">Shared network. {title}</span>
		</span>
	);
}
