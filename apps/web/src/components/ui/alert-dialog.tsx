import { AlertDialog as AlertDialogPrimitive } from "@base-ui/react/alert-dialog";
import { cn } from "cn";
import type * as React from "react";
import { Button } from "#/components/ui/button";
import { trapTabKey } from "#/components/ui/focus-trap";

function AlertDialog({ ...props }: AlertDialogPrimitive.Root.Props) {
	return <AlertDialogPrimitive.Root data-slot="alert-dialog" {...props} />;
}

function AlertDialogPortal({ ...props }: AlertDialogPrimitive.Portal.Props) {
	return (
		<AlertDialogPrimitive.Portal data-slot="alert-dialog-portal" {...props} />
	);
}

function AlertDialogOverlay({
	className,
	instant = false,
	...props
}: AlertDialogPrimitive.Backdrop.Props & { instant?: boolean }) {
	return (
		<AlertDialogPrimitive.Backdrop
			data-slot="alert-dialog-overlay"
			className={cn(
				!instant && "t-scrim",
				"fixed inset-0 isolate z-50 bg-scrim",
				className,
			)}
			{...props}
		/>
	);
}

// A confirmation is always one decision, so it only comes in the small tier,
// and sits a little lower than a form dialog so it reads as an interruption.
// Scales in from its own centre (the modal transition: scale 0.96 and opacity, 250ms in, 150ms out),
// unless `instant`: a confirmation opened by the keyboard appears at once.
function AlertDialogContent({
	className,
	onKeyDown,
	instant = false,
	...props
}: AlertDialogPrimitive.Popup.Props & { instant?: boolean }) {
	return (
		<AlertDialogPortal>
			<AlertDialogOverlay instant={instant} />
			<div className="pointer-events-none fixed inset-x-0 top-[16vh] z-50 flex justify-center px-4">
				<AlertDialogPrimitive.Popup
					data-slot="alert-dialog-content"
					className={cn(
						"surface-modal pointer-events-auto flex w-full max-w-[92vw] flex-col overflow-clip rounded-dialog text-ink-1 outline-none sm:w-[460px]",
						!instant && "t-modal",
						className,
					)}
					{...props}
					onKeyDown={(event) => {
						onKeyDown?.(event);
						trapTabKey(event);
					}}
				/>
			</div>
		</AlertDialogPortal>
	);
}

function AlertDialogBody({ className, ...props }: React.ComponentProps<"div">) {
	return (
		<div
			data-slot="alert-dialog-body"
			className={cn("flex flex-col gap-4 px-5 py-5", className)}
			{...props}
		/>
	);
}

function AlertDialogFooter({
	className,
	...props
}: React.ComponentProps<"div">) {
	return (
		<div
			data-slot="alert-dialog-footer"
			className={cn(
				"flex flex-col-reverse gap-2 border-line border-t px-5 py-3.5 sm:flex-row sm:items-center sm:justify-end",
				className,
			)}
			{...props}
		/>
	);
}

function AlertDialogTitle({
	className,
	...props
}: React.ComponentProps<typeof AlertDialogPrimitive.Title>) {
	return (
		<AlertDialogPrimitive.Title
			data-slot="alert-dialog-title"
			className={cn(
				"font-semibold text-[16px] text-ink-1 leading-[1.3]",
				className,
			)}
			{...props}
		/>
	);
}

function AlertDialogDescription({
	className,
	...props
}: React.ComponentProps<typeof AlertDialogPrimitive.Description>) {
	return (
		<AlertDialogPrimitive.Description
			data-slot="alert-dialog-description"
			className={cn("type-body text-ink-3", className)}
			{...props}
		/>
	);
}

function AlertDialogCancel({
	className,
	variant = "ghost",
	size = "default",
	...props
}: AlertDialogPrimitive.Close.Props &
	Pick<React.ComponentProps<typeof Button>, "variant" | "size">) {
	return (
		<AlertDialogPrimitive.Close
			data-slot="alert-dialog-cancel"
			className={cn(className)}
			render={<Button variant={variant} size={size} />}
			{...props}
		/>
	);
}

export {
	AlertDialog,
	AlertDialogBody,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogTitle,
};
