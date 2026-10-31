import { cn } from "cn";
import { FileCheck2Icon, FileDiffIcon, PencilLineIcon } from "lucide-react";
import { Badge } from "#/components/ui/badge";
import {
	Tooltip,
	TooltipContent,
	TooltipTrigger,
} from "#/components/ui/tooltip";
import { type ChipKind, type ConfigChip, configChip } from "./model";
import { useEnvironmentConfigSource, useServiceConfigSource } from "./queries";

const ICONS = {
	differs: FileDiffIcon,
	edited: PencilLineIcon,
	managed: FileCheck2Icon,
} as const satisfies Record<ChipKind, unknown>;

/**
 * "nebula.toml differs" (amber), "edited here" (amber) or "from nebula.toml"
 * (neutral): always words, with a file icon that is the kind of thing it is
 * about, not a status. The sentence behind it is the tooltip.
 */
export function ConfigChipView({
	chip,
	className,
}: {
	chip: ConfigChip;
	className?: string | undefined;
}) {
	const Icon = ICONS[chip.kind];
	return (
		<Tooltip>
			<TooltipTrigger
				render={
					<Badge
						variant={chip.kind === "managed" ? "default" : "warning"}
						className={cn("font-sans text-[11px]", className)}
					/>
				}
			>
				<Icon aria-hidden />
				{chip.label}
			</TooltipTrigger>
			<TooltipContent>{chip.detail}</TooltipContent>
		</Tooltip>
	);
}

/** The chip in the inspector's header: one request for the service, no file read. */
export function ServiceConfigChip({
	serviceId,
	className,
}: {
	serviceId: string;
	className?: string | undefined;
}) {
	const source = useServiceConfigSource(serviceId);
	const chip = configChip(source.data?.managed ?? false, source.data?.state);
	if (!chip) return null;
	return <ConfigChipView chip={chip} className={className} />;
}

/**
 * The chip on a canvas card. It reads the environment's own answer, which
 * lists every service's state at once, so a canvas of fifty cards asks once.
 */
export function NodeConfigChip({
	environmentId,
	slug,
	className,
}: {
	environmentId: string;
	slug: string;
	className?: string | undefined;
}) {
	const source = useEnvironmentConfigSource(environmentId);
	const entry = source.data?.services?.find((service) => service.slug === slug);
	const chip = entry ? configChip(true, entry.state) : null;
	if (!chip) return null;
	return <ConfigChipView chip={chip} className={className} />;
}
