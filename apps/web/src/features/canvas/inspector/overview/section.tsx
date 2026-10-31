import { ArrowRightIcon } from "lucide-react";
import type { ReactNode } from "react";
import { Button } from "#/components/ui/button";

/**
 * A titled block of the Overview: a `■ TITLE ····` header with an optional
 * accent link after the leader ("Metrics →", "View all →"), then the content.
 * Lists under it are ruled rows, not cards.
 */
export function OverviewSection({
	title,
	id,
	action,
	children,
}: {
	title: string;
	/** The DOM id the jump bar scrolls to. */
	id?: string | undefined;
	action?: { label: string; onClick(): void } | undefined;
	children: ReactNode;
}) {
	return (
		<section
			id={id}
			aria-label={title}
			// Clears the sticky jump bar when a chip scrolls here.
			className="flex scroll-mt-12 flex-col gap-2"
		>
			<h3 className="section-head type-micro">
				<span>{title}</span>
				{action ? (
					<Button
						variant="link"
						size="default"
						className="font-sans text-xs normal-case tracking-normal max-[759px]:-my-2.5 max-[759px]:min-h-10 max-[759px]:px-2"
						onClick={action.onClick}
					>
						{action.label}
						<ArrowRightIcon aria-hidden className="size-3" />
					</Button>
				) : null}
			</h3>
			{children}
		</section>
	);
}
