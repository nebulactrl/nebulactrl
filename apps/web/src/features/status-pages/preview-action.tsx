import { ExternalLinkIcon, EyeIcon } from "lucide-react";
import type { StatusPageView } from "#/api/gen/types.gen";
import { Button } from "#/components/ui/button";
import { previewBlocker, publicUrl } from "./model";

/**
 * Preview of a status page. The control plane renders a page only on its own
 * hostname and only once it is published, so a published page opens there. A
 * draft has nothing to open: the button asks for the reason
 * (`previewBlocker`) to be shown by whoever owns the space for it.
 */
export function PreviewAction({
	page,
	size,
	variant = "secondary",
	explaining,
	onExplain,
}: {
	page: Pick<StatusPageView, "hostname" | "published">;
	size?: "sm";
	variant?: "secondary" | "ghost";
	/** Whether the reason is on screen. */
	explaining: boolean;
	onExplain: () => void;
}) {
	if (previewBlocker(page) === null && page.hostname) {
		return (
			<Button
				variant={variant}
				{...(size ? { size } : {})}
				nativeButton={false}
				render={
					<a
						href={publicUrl(page.hostname)}
						target="_blank"
						rel="noopener noreferrer"
					/>
				}
			>
				<ExternalLinkIcon />
				Open page
				<span className="sr-only"> (opens in a new tab)</span>
			</Button>
		);
	}
	return (
		<Button
			type="button"
			variant={variant}
			{...(size ? { size } : {})}
			aria-expanded={explaining}
			onClick={onExplain}
		>
			<EyeIcon />
			Preview
		</Button>
	);
}
