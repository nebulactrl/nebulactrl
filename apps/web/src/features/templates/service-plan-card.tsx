import {
	DatabaseIcon,
	HardDriveIcon,
	NetworkIcon,
	TimerIcon,
} from "lucide-react";
import { Badge } from "#/components/ui/badge";
import { ServiceIcon } from "#/features/canvas/service-icon";
import {
	databaseLabel,
	publishedPortsText,
	type ServiceCardModel,
} from "./lib";

/** The image reference as the card shows it: no registry host, no digest. */
function shortImage(image: string): string {
	const withoutDigest = image.split("@")[0] ?? image;
	return withoutDigest.replace(/^(ghcr\.io|docker\.io|quay\.io)\//, "");
}

/**
 * One service the install will create: kind mark, name, what it runs (image,
 * or the managed database), and the facts that matter before installing:
 * published ports, volumes, and whether it is a one-shot job.
 */
export function ServicePlanCard({ card }: { card: ServiceCardModel }) {
	const published = publishedPortsText(card);
	return (
		<li
			data-slot="service-plan-card"
			className="surface-well flex items-start gap-3 rounded-ctl border border-line px-3 py-2.5"
		>
			<ServiceIcon
				service={{
					kind: card.kind,
					...(card.image ? { config: { image: card.image } } : {}),
				}}
				size={28}
			/>
			<div className="flex min-w-0 flex-1 flex-col gap-1.5">
				<div className="flex flex-wrap items-center gap-x-2 gap-y-1">
					<span className="font-medium text-[13px] text-ink-1">
						{card.name}
					</span>
					{card.database ? (
						<Badge>
							<DatabaseIcon aria-hidden />
							{databaseLabel(card.database)}
						</Badge>
					) : null}
					{card.oneshot ? (
						<Badge variant="accent">
							<TimerIcon aria-hidden />
							One-shot job
						</Badge>
					) : card.kind === "worker" ? (
						<Badge>Worker</Badge>
					) : null}
				</div>
				<div className="truncate font-mono text-2xs text-ink-3">
					{card.image
						? shortImage(card.image)
						: card.database
							? "Managed database on its own volume"
							: "No image"}
				</div>
				<div className="flex flex-wrap items-center gap-x-3 gap-y-1 font-mono text-2xs text-ink-3 tabular-nums">
					{card.httpPort ? <span>http :{card.httpPort}</span> : null}
					{published ? (
						<span className="flex items-center gap-1 text-accent-ink">
							<NetworkIcon aria-hidden className="size-3" />
							{published}
						</span>
					) : null}
					{card.volumes > 0 ? (
						<span className="flex items-center gap-1">
							<HardDriveIcon aria-hidden className="size-3" />
							{card.volumes} {card.volumes === 1 ? "volume" : "volumes"}
						</span>
					) : null}
				</div>
			</div>
		</li>
	);
}
