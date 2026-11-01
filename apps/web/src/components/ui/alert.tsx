import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "cn";
import type * as React from "react";
import { type CalloutTone, ToneIcon } from "#/components/common/tone-icon";

// A callout names what happened and what to do next. The default sits
// recessed on the page (a faint well). The status variants carry the alert
// marker: a tone icon at the start of the first line (the Alert renders it, so
// callers pass no icon), a very subtle tone wash and a 1px tone border. The
// title always carries the state in words; the icon is never the only cue.
const alertVariants = cva(
	"group/alert relative grid w-full gap-[5px] rounded-card px-3 py-2.5 text-left text-ink-2 text-xs leading-[1.5] has-[>svg]:grid-cols-[auto_1fr] has-[>svg]:gap-x-2.5 sm:has-data-[slot=alert-action]:pr-44 *:[svg]:row-span-2 *:[svg]:translate-y-0.5",
	{
		variants: {
			variant: {
				default:
					"border border-line bg-surface-well *:[svg]:text-ink-3 *:[svg:not([class*='size-'])]:size-4",
				// Status callouts: a warm note, an error panel, a done row, a note.
				warning: "tone-callout",
				destructive:
					"tone-callout [&_[data-slot=alert-title]]:text-destructive-ink-bright [&_[data-slot=alert-description]]:text-destructive-ink-soft",
				success: "tone-callout",
				info: "tone-callout",
			},
		},
		defaultVariants: {
			variant: "default",
		},
	},
);

const ALERT_TONE: Record<
	NonNullable<VariantProps<typeof alertVariants>["variant"]>,
	CalloutTone | null
> = {
	default: null,
	warning: "warning",
	destructive: "danger",
	success: "success",
	info: "info",
};

function Alert({
	className,
	variant,
	children,
	...props
}: React.ComponentProps<"div"> & VariantProps<typeof alertVariants>) {
	const tone = ALERT_TONE[variant ?? "default"];
	return (
		<div
			data-slot="alert"
			role="alert"
			data-tone={tone ?? undefined}
			className={cn(alertVariants({ variant }), className)}
			{...props}
		>
			{tone ? <ToneIcon tone={tone} /> : null}
			{children}
		</div>
	);
}

function AlertTitle({ className, ...props }: React.ComponentProps<"div">) {
	return (
		<div
			data-slot="alert-title"
			className={cn(
				"font-medium text-ink-1 text-sm leading-[1.3] group-has-[>svg]/alert:col-start-2",
				className,
			)}
			{...props}
		/>
	);
}

function AlertDescription({
	className,
	...props
}: React.ComponentProps<"div">) {
	return (
		<div
			data-slot="alert-description"
			className={cn(
				"min-w-0 break-words text-ink-3 text-xs leading-[1.5] group-has-[>svg]/alert:col-start-2 [&_p:not(:last-child)]:mb-2",
				className,
			)}
			{...props}
		/>
	);
}

// On a phone the action flows under the text (beside the icon column, not
// under it): pinned to the right it would cover the description, and
// reserving a fixed gutter for it would leave the text a sliver.
function AlertAction({ className, ...props }: React.ComponentProps<"div">) {
	return (
		<div
			data-slot="alert-action"
			className={cn(
				"mt-2 group-has-[>svg]/alert:col-start-2 sm:absolute sm:top-1/2 sm:right-3 sm:mt-0 sm:-translate-y-1/2",
				className,
			)}
			{...props}
		/>
	);
}

export { Alert, AlertAction, AlertDescription, AlertTitle };
