import { cn } from "cn";
import { InfoIcon } from "lucide-react";
import { type ReactNode, useId, useReducer } from "react";
import {
	Tooltip,
	TooltipContent,
	TooltipTrigger,
} from "#/components/ui/tooltip";
import {
	INFO_TIP_CLOSED,
	infoTipEventFor,
	reduceInfoTip,
} from "./info-tip-state";

/**
 * The longer explanation behind a one-line setting description: a 16px info
 * icon that opens a tooltip on hover, on keyboard focus and on click or tap
 * (a click pins it open; a second click, Escape or a press elsewhere closes
 * it). The button is named by `label`, and the text is also attached to it as
 * its description, so a screen reader hears it without opening anything.
 * Tooltips opened one after another skip the delay and the animation (the
 * app's TooltipProvider groups them), so scanning a page of these never
 * waits.
 *
 * Only for explanation. A warning, or anything that affects data, belongs in
 * the page where it can be read without hovering.
 */
export function InfoTip({
	label,
	children,
	className,
}: {
	/** The accessible name: what the tip explains ("About the pre-deploy command"). */
	label: string;
	children: ReactNode;
	className?: string;
}) {
	const descriptionId = useId();
	const [state, dispatch] = useReducer(reduceInfoTip, INFO_TIP_CLOSED);
	return (
		<>
			<Tooltip
				open={state.open}
				onOpenChange={(open, details) => {
					const event = infoTipEventFor(details.reason, open);
					if (event) dispatch(event);
				}}
			>
				<TooltipTrigger
					onClick={() => dispatch({ type: "press" })}
					render={
						<button
							type="button"
							aria-label={label}
							aria-describedby={descriptionId}
							className={cn(
								// The 16px icon keeps a 24px target through the pseudo-element.
								"relative inline-flex size-4 shrink-0 cursor-help items-center justify-center rounded-full text-ink-3 outline-none transition-colors duration-quick ease-out before:absolute before:-inset-1 hover:text-ink-1 focus-visible:focus-ring data-[popup-open]:text-ink-1 motion-reduce:transition-none",
								className,
							)}
						/>
					}
				>
					<InfoIcon aria-hidden className="size-4" />
				</TooltipTrigger>
				<TooltipContent className="max-w-[18rem] items-start">
					{children}
				</TooltipContent>
			</Tooltip>
			{/* Always in the tree, so the description is there before the tip opens. */}
			<span id={descriptionId} hidden>
				{children}
			</span>
		</>
	);
}
