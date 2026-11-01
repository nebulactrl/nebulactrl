import { cn } from "cn";
import {
	CircleAlertIcon,
	CircleCheckIcon,
	InfoIcon,
	TriangleAlertIcon,
} from "lucide-react";

/** The four tones of a message: an alert, banner, callout, toast or error state. */
export type CalloutTone = "danger" | "warning" | "info" | "success";

const ICONS = {
	danger: CircleAlertIcon,
	warning: TriangleAlertIcon,
	info: InfoIcon,
	success: CircleCheckIcon,
} as const satisfies Record<CalloutTone, unknown>;

const COLOURS = {
	danger: "text-status-failed",
	warning: "text-status-staged",
	info: "text-status-building",
	success: "text-status-live",
} as const satisfies Record<CalloutTone, string>;

/**
 * The alert marker: a 16px Lucide icon in the tone colour at the start of a
 * message's first line. Decorative (`aria-hidden`): the words carry the
 * meaning. Pair it with the `tone-callout` wash. A lamp is for live resource
 * status only, never for a message.
 */
export function ToneIcon({
	tone,
	className,
}: {
	tone: CalloutTone;
	className?: string;
}) {
	const Icon = ICONS[tone];
	return (
		<Icon
			aria-hidden
			className={cn("size-4 shrink-0", COLOURS[tone], className)}
		/>
	);
}
