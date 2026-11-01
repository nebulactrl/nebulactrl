import { ExternalLinkIcon, ScaleIcon, TriangleAlertIcon } from "lucide-react";
import type { TemplateSummary } from "#/api/gen/types.gen";
import { Badge } from "#/components/ui/badge";
import {
	Tooltip,
	TooltipContent,
	TooltipTrigger,
} from "#/components/ui/tooltip";
import { licenseClassLabel, licenseExplanation, licenseTone } from "./lib";

type LicenseFields = Pick<TemplateSummary, "license" | "licenseClass"> &
	Partial<Pick<TemplateSummary, "licenseUrl">>;

/**
 * The upstream licence. OSI-approved licences are a quiet neutral chip;
 * source-available and proprietary software carries a warning badge that
 * says so in words, with the licence name and a link in its tooltip.
 */
export function LicenseChip({ item }: { item: LicenseFields }) {
	const flagged = licenseTone(item.licenseClass) === "flagged";
	return (
		<Tooltip>
			<TooltipTrigger
				render={
					<Badge
						variant={flagged ? "warning" : "default"}
						tabIndex={0}
						className="cursor-default outline-none focus-visible:focus-ring"
					/>
				}
			>
				{flagged ? (
					<TriangleAlertIcon aria-hidden />
				) : (
					<ScaleIcon aria-hidden />
				)}
				{flagged ? licenseClassLabel(item.licenseClass) : item.license}
			</TooltipTrigger>
			<TooltipContent className="flex-col items-start gap-1">
				<span>{licenseExplanation(item)}</span>
				{item.licenseUrl ? (
					<a
						href={item.licenseUrl}
						target="_blank"
						rel="noreferrer noopener"
						className="inline-flex items-center gap-1 text-accent-ink hover:text-ink-1"
					>
						Read the {item.license} licence
						<ExternalLinkIcon aria-hidden className="size-3" />
					</a>
				) : null}
			</TooltipContent>
		</Tooltip>
	);
}
