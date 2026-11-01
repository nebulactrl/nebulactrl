import { Dialog as DialogPrimitive } from "@base-ui/react/dialog";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "cn";
import { X } from "lucide-react";
import * as React from "react";
import { Button } from "#/components/ui/button";
import { trapTabKey } from "#/components/ui/focus-trap";
import { lastInputWasKeyboard } from "#/lib/input-modality";

/**
 * Whether the keyboard opened the dialog that `open` drives: true when the
 * reader's last input was a key press at the moment `open` turned true. Pass
 * the result to `DialogContent instant`, so a dialog opened by key appears
 * without its entrance (and leaves without its exit). The answer is kept
 * until the next opening.
 */
function useOpenedByKeyboard(open: boolean): boolean {
	const [instant, setInstant] = React.useState(
		() => open && lastInputWasKeyboard(),
	);
	const [wasOpen, setWasOpen] = React.useState(open);
	if (open !== wasOpen) {
		setWasOpen(open);
		if (open) setInstant(lastInputWasKeyboard());
	}
	return instant;
}

function Dialog({ ...props }: DialogPrimitive.Root.Props) {
	return <DialogPrimitive.Root data-slot="dialog" {...props} />;
}

function DialogPortal({ ...props }: DialogPrimitive.Portal.Props) {
	return <DialogPrimitive.Portal data-slot="dialog-portal" {...props} />;
}

function DialogOverlay({
	className,
	instant = false,
	...props
}: DialogPrimitive.Backdrop.Props & { instant?: boolean }) {
	return (
		<DialogPrimitive.Backdrop
			// A dialog opened from inside a sheet or another dialog is nested to
			// Base UI, which skips the backdrop unless told otherwise.
			forceRender
			data-slot="dialog-overlay"
			className={cn(
				"fixed inset-0 isolate z-50 bg-scrim",
				!instant && "t-scrim",
				className,
			)}
			{...props}
		/>
	);
}

// Top-anchored rather than centred: the title lands where the eye already
// is, and a dialog that grows (a step flow, a validation message) grows
// downward instead of jumping. It scales in from its own centre (the modal
// transition: scale 0.96 and opacity, 250ms in, 150ms out) unless `instant`
// drops the transition. Four width tiers: one
// decision (sm), a short form (md), a review or multi-part form (lg), a step
// flow with a live side panel (xl). Height always follows the content. The
// design's own widths (480, 520, 580, 600, 620) and top offsets (7 to 16vh)
// come through `width` and `top`, since every dialog there has its own.
// overflow-clip, not overflow-hidden, so scrollIntoView inside the body
// cannot scroll the shell and hide its header (see sheet.tsx).
const dialogContentVariants = cva(
	"surface-modal fixed top-(--dialog-top,12vh) left-1/2 z-50 flex max-h-[calc(100%_-_var(--dialog-top,12vh)_-_2rem)] w-full max-w-[92vw] -translate-x-1/2 flex-col overflow-clip rounded-dialog text-ink-1 outline-none",
	{
		variants: {
			size: {
				sm: "sm:w-[460px]",
				md: "sm:w-[560px]",
				lg: "sm:w-[600px]",
				xl: "sm:w-[720px]",
			},
		},
		defaultVariants: {
			size: "sm",
		},
	},
);

function DialogContent({
	className,
	children,
	size = "sm",
	width,
	top,
	showCloseButton = false,
	instant = false,
	style,
	onKeyDown,
	...props
}: DialogPrimitive.Popup.Props &
	VariantProps<typeof dialogContentVariants> & {
		showCloseButton?: boolean;
		/** Exact width in px, capped at 92vw; overrides the size tier. */
		width?: number;
		/** Distance from the top of the viewport, in vh. The default is 12. */
		top?: number;
		/**
		 * No entrance or exit: for a dialog the keyboard opened (see
		 * `useOpenedByKeyboard`), where the motion only makes the reader wait.
		 */
		instant?: boolean;
	}) {
	return (
		<DialogPortal>
			<DialogOverlay instant={instant} />
			<DialogPrimitive.Popup
				data-slot="dialog-content"
				data-size={size}
				className={cn(
					dialogContentVariants({ size }),
					!instant && "t-modal",
					className,
				)}
				style={
					{
						...(width !== undefined && { width: `min(${width}px, 92vw)` }),
						...(top !== undefined && { "--dialog-top": `${top}vh` }),
						...style,
					} as React.CSSProperties
				}
				{...props}
				onKeyDown={(event) => {
					onKeyDown?.(event);
					trapTabKey(event);
				}}
			>
				{children}
				{showCloseButton && (
					<DialogPrimitive.Close
						data-slot="dialog-close"
						render={
							<Button
								variant="ghost"
								className="absolute top-3.5 right-3.5"
								size="icon-sm"
							/>
						}
					>
						<X />
						<span className="sr-only">Close</span>
					</DialogPrimitive.Close>
				)}
			</DialogPrimitive.Popup>
		</DialogPortal>
	);
}

function DialogHeader({ className, ...props }: React.ComponentProps<"div">) {
	return (
		<div
			data-slot="dialog-header"
			className={cn(
				"flex flex-col gap-1.5 border-line border-b px-5 py-4",
				className,
			)}
			{...props}
		/>
	);
}

function DialogBody({ className, ...props }: React.ComponentProps<"div">) {
	return (
		<div
			data-slot="dialog-body"
			className={cn(
				"flex min-h-0 flex-col gap-4 overflow-y-auto overscroll-contain px-5 py-5",
				className,
			)}
			{...props}
		/>
	);
}

function DialogFooter({
	className,
	showCloseButton = false,
	children,
	...props
}: React.ComponentProps<"div"> & {
	showCloseButton?: boolean;
}) {
	return (
		<div
			data-slot="dialog-footer"
			className={cn(
				"flex shrink-0 flex-col-reverse gap-2 border-line border-t px-5 py-3.5 sm:flex-row sm:items-center sm:justify-end",
				className,
			)}
			{...props}
		>
			{children}
			{showCloseButton && (
				<DialogPrimitive.Close render={<Button variant="ghost" />}>
					Close
				</DialogPrimitive.Close>
			)}
		</div>
	);
}

function DialogTitle({ className, ...props }: DialogPrimitive.Title.Props) {
	return (
		<DialogPrimitive.Title
			data-slot="dialog-title"
			className={cn(
				"font-semibold text-[16px] text-ink-1 leading-[1.2]",
				className,
			)}
			{...props}
		/>
	);
}

function DialogDescription({
	className,
	...props
}: DialogPrimitive.Description.Props) {
	return (
		<DialogPrimitive.Description
			data-slot="dialog-description"
			className={cn("text-pretty text-ink-3 text-sm leading-[1.5]", className)}
			{...props}
		/>
	);
}

export {
	Dialog,
	DialogBody,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
	useOpenedByKeyboard,
};
