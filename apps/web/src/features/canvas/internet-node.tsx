import { type Node, type NodeProps, Position } from "@xyflow/react";
import { GlobeIcon } from "lucide-react";
import { type CSSProperties, memo } from "react";
import { Badge } from "#/components/ui/badge";
import { count } from "#/lib/format";
import { DIMMED_OPACITY, useCanvasUi } from "./canvas-context";
import { JackHandle } from "./jack-handle";
import {
	INTERNET_HEIGHT,
	INTERNET_WIDTH,
	type InternetSummary,
} from "./lib/internet";

export type InternetFlowNode = Node<{ summary: InternetSummary }, "internet">;

/** Hostnames shown as chips before the rest fold into "+N". */
const VISIBLE_DOMAINS = 2;

function InternetNodeImpl({ data }: NodeProps<InternetFlowNode>) {
	const { matchIds } = useCanvasUi();
	const { domains, serviceIds } = data.summary;
	const shown = domains.slice(0, VISIBLE_DOMAINS);
	const more = domains.length - shown.length;
	const dimmed =
		matchIds !== null && !serviceIds.some((id) => matchIds.has(id));
	return (
		<div
			data-internet-node
			className="relative flex items-center gap-[11px] rounded-[14px] border border-line-control border-dashed bg-surface-raised px-3.5 py-[11px] shadow-[inset_0_1px_0_rgb(255_255_255/0.05)] transition-opacity duration-quick"
			style={{
				width: INTERNET_WIDTH,
				minHeight: INTERNET_HEIGHT,
				opacity: dimmed ? DIMMED_OPACITY : 1,
			}}
		>
			<span
				aria-hidden
				className="service-tile size-7 rounded-lg text-white"
				style={{ "--t": "oklch(0.45 0.03 262)" } as CSSProperties}
			>
				<GlobeIcon className="size-4" strokeWidth={1.8} />
			</span>
			<div className="min-w-0">
				<div className="font-semibold text-[14px] text-ink-1 leading-[1.1]">
					Internet
				</div>
				<div className="mt-[3px] text-[11.5px] text-ink-3 leading-none">
					{count(domains.length, "public domain")}
				</div>
			</div>
			<div className="ml-auto flex min-w-0 flex-col items-end gap-1">
				{shown.map((hostname) => (
					<Badge
						key={hostname}
						title={hostname}
						className="max-w-[150px] justify-start font-mono text-[11px]"
					>
						<span className="truncate">{hostname}</span>
					</Badge>
				))}
				{more > 0 ? (
					<span className="font-mono text-[10.5px] text-ink-3">
						+{more} more
					</span>
				) : null}
			</div>
			<JackHandle
				type="source"
				position={Position.Bottom}
				id="bottom"
				plugged
				label="Internet entry"
				connectable={false}
			/>
		</div>
	);
}

export const InternetNode = memo(
	InternetNodeImpl,
	(prev, next) => prev.data.summary === next.data.summary,
);
