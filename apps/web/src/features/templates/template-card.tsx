import { DatabaseIcon, NetworkIcon } from "lucide-react";
import type { TemplateSummary } from "#/api/gen/types.gen";
import { Badge } from "#/components/ui/badge";
import {
	Tooltip,
	TooltipContent,
	TooltipTrigger,
} from "#/components/ui/tooltip";
import { databaseLabel, memoryHint, needsUnrestricted } from "./lib";
import { LicenseChip } from "./license-chip";
import { TemplateIcon } from "./template-icon";
import { UnrestrictedBadge } from "./unrestricted-badge";

/** One chip per engine, however many services use it. */
const uniqueEngines = (databases: readonly string[]): string[] => [
	...new Set(databases),
];

/**
 * A catalog card: mark, name, one line, what it costs (services, memory), the
 * managed databases it brings, its licence and, for game servers, the marker
 * that it opens TCP/UDP ports. The whole card opens the template; the licence
 * badge sits above that overlay so its tooltip stays reachable. Every card is
 * the same height: the chips share one clipped row, and databases beyond the
 * first collapse into "+N".
 */
export function TemplateCard({
	item,
	onOpen,
}: {
	item: TemplateSummary;
	onOpen(id: string): void;
}) {
	const engines = uniqueEngines(item.databases ?? []);
	return (
		<li
			data-slot="template-card"
			className="surface-raised group/card relative flex h-[140px] flex-col gap-2.5 rounded-card p-3 transition-[border-color,background-color] duration-150 ease-out has-[button:focus-visible]:focus-ring hover:border-line-control"
		>
			<button
				type="button"
				aria-label={`${item.name}: ${item.summary}`}
				onClick={() => onOpen(item.id)}
				className="absolute inset-0 cursor-pointer rounded-card outline-none"
			/>
			<div className="pointer-events-none flex items-start gap-3">
				<TemplateIcon icon={item.icon} name={item.name} size={36} />
				<div className="min-w-0 flex-1">
					<div className="truncate font-semibold text-[14px] text-ink-1 leading-tight">
						{item.name}
					</div>
					<p className="mt-1 line-clamp-2 text-pretty text-[12px] text-ink-3 leading-[1.45]">
						{item.summary}
					</p>
				</div>
			</div>
			{needsUnrestricted(item) ? (
				<UnrestrictedBadge className="w-fit max-w-full shrink-0 self-start" />
			) : null}
			<div className="mt-auto flex min-w-0 flex-nowrap items-center gap-1.5 overflow-hidden">
				<span className="pointer-events-none shrink-0 whitespace-nowrap font-mono text-2xs text-ink-3 tabular-nums">
					{item.services} {item.services === 1 ? "service" : "services"}
					{item.memoryMb > 0 ? ` · ~${memoryHint(item.memoryMb)}` : ""}
				</span>
				{engines.slice(0, 1).map((engine) => (
					<Badge key={engine} className="pointer-events-none min-w-0 shrink">
						<DatabaseIcon aria-hidden />
						{databaseLabel(engine)}
					</Badge>
				))}
				{engines.length > 1 ? (
					<Badge
						className="pointer-events-none"
						title={engines.map(databaseLabel).join(", ")}
					>
						+{engines.length - 1}
					</Badge>
				) : null}
				{item.heavy ? (
					<Tooltip>
						<TooltipTrigger
							render={
								<Badge
									variant="warning"
									tabIndex={0}
									className="relative shrink-0 cursor-default outline-none focus-visible:focus-ring"
								/>
							}
						>
							Heavy
						</TooltipTrigger>
						<TooltipContent>
							Several GB to download, a lot of RAM or a slow first start. Check
							the server has the room.
						</TooltipContent>
					</Tooltip>
				) : null}
				{item.publishesPorts ? (
					<Tooltip>
						<TooltipTrigger
							render={
								<Badge
									variant="accent"
									tabIndex={0}
									className="relative shrink-0 cursor-default outline-none focus-visible:focus-ring"
								/>
							}
						>
							<NetworkIcon aria-hidden />
							TCP/UDP ports
						</TooltipTrigger>
						<TooltipContent>
							Opens TCP/UDP ports on the server's public interface for players
							to connect.
						</TooltipContent>
					</Tooltip>
				) : null}
				<span className="relative ml-auto shrink-0">
					<LicenseChip item={item} />
				</span>
			</div>
		</li>
	);
}
