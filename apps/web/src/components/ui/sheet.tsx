import { Dialog as SheetPrimitive } from "@base-ui/react/dialog";
import { cn } from "cn";
import { X } from "lucide-react";
import * as React from "react";
import { Button } from "#/components/ui/button";
import {
	OPEN_OTHER_LAYER_SELECTOR,
	shouldDismissOnOutsidePress,
} from "#/components/ui/sheet-dismiss";
import { lastInputWasKeyboard } from "#/lib/input-modality";

type SheetProps = SheetPrimitive.Root.Props & {
	/**
	 * Selects the elements that switch this sheet to another subject when
	 * pressed (a table row, a card). A press on one never dismisses the
	 * sheet: the element's own click handler retargets it, and closing first
	 * would flicker it shut and open again.
	 */
	retarget?: string;
};

/**
 * Whether the sheet's last open/close came from the keyboard. Read by
 * `SheetContent` so a drawer skips its slide for a reader moving by key.
 */
const SheetInstantContext = React.createContext(false);

/**
 * Whether a request to close the sheet should go through. A non-modal
 * sheet leaves the page usable behind it, so what counts as a request
 * needs more care than Base UI's subtree test:
 *
 * - a press inside a dialog, menu, select or toast — including ones the
 *   page renders as siblings of the sheet — belongs to that layer, so a
 *   nested dialog closes alone and the sheet stays;
 * - a press on a `retarget` element hands the sheet to that element;
 * - Escape closes the topmost layer only;
 * - focus leaving the sheet (tabbing out, a dialog taking focus) is not a
 *   request to close it: only Escape, the close button and a press on the
 *   page are.
 */
function allowsClose(
	details: SheetPrimitive.Root.ChangeEventDetails,
	retarget: string | undefined,
): boolean {
	if (details.reason === "focus-out") return false;
	if (details.reason === "escape-key") {
		return document.querySelector(OPEN_OTHER_LAYER_SELECTOR) === null;
	}
	if (details.reason !== "outside-press") return true;

	const event = details.event;
	const target = event.target instanceof Element ? event.target : null;
	const touches = "touches" in event ? event.touches : undefined;
	const lifted =
		"changedTouches" in event && event.type === "touchend"
			? event.changedTouches.length
			: 0;
	return shouldDismissOnOutsidePress(
		{
			target,
			button: "button" in event ? event.button : undefined,
			touchCount: touches ? touches.length + lifted : undefined,
		},
		retarget,
	);
}

function Sheet({ retarget, onOpenChange, open, ...props }: SheetProps) {
	const [instant, setInstant] = React.useState(false);
	const [wasOpen, setWasOpen] = React.useState(open);
	if (open !== undefined && open !== wasOpen) {
		setWasOpen(open);
		setInstant(lastInputWasKeyboard());
	}

	return (
		<SheetInstantContext value={instant}>
			<SheetPrimitive.Root
				data-slot="sheet"
				{...(open === undefined ? {} : { open })}
				onOpenChange={(next, details) => {
					if (!next && !allowsClose(details, retarget)) {
						details.cancel();
						return;
					}
					onOpenChange?.(next, details);
				}}
				{...props}
			/>
		</SheetInstantContext>
	);
}

function SheetPortal({ ...props }: SheetPrimitive.Portal.Props) {
	return <SheetPrimitive.Portal data-slot="sheet-portal" {...props} />;
}

function SheetOverlay({ className, ...props }: SheetPrimitive.Backdrop.Props) {
	return (
		<SheetPrimitive.Backdrop
			data-slot="sheet-overlay"
			className={cn("t-scrim fixed inset-0 z-50 bg-scrim", className)}
			{...props}
		/>
	);
}

const edgeSheetClassName =
	"rm-fade surface-float fixed z-50 flex flex-col rounded-none text-ink-1 text-sm transition-[translate,opacity] duration-fast ease-smooth-out data-ending-style:opacity-0 data-ending-style:duration-quick data-starting-style:opacity-0 data-[side=bottom]:inset-x-0 data-[side=bottom]:bottom-0 data-[side=bottom]:h-auto data-[side=bottom]:data-ending-style:translate-y-[2.5rem] data-[side=bottom]:data-starting-style:translate-y-[2.5rem] data-[side=left]:inset-y-0 data-[side=left]:left-0 data-[side=left]:h-full data-[side=left]:w-3/4 data-[side=left]:data-ending-style:translate-x-[-2.5rem] data-[side=left]:data-starting-style:translate-x-[-2.5rem] data-[side=right]:inset-y-0 data-[side=right]:right-0 data-[side=right]:h-full data-[side=right]:w-3/4 data-[side=right]:data-ending-style:translate-x-[2.5rem] data-[side=right]:data-starting-style:translate-x-[2.5rem] data-[side=top]:inset-x-0 data-[side=top]:top-0 data-[side=top]:h-auto data-[side=top]:data-ending-style:translate-y-[-2.5rem] data-[side=top]:data-starting-style:translate-y-[-2.5rem] data-[side=left]:sm:max-w-sm data-[side=right]:sm:max-w-sm";

/*
 * The floating sheet is the console's detail panel: an inset card that
 * slides in from the right over the canvas. Its motion lives entirely in
 * `.panel-shell` (the service panel's slide and receding scale) or
 * `.panel-shell-simple` (the project-variable panels' 320ms settle) in
 * styles.css, both of which move the panel with `transform`.
 * No Tailwind `translate-*` belongs here: it drives the separate CSS
 * `translate` property, which those transitions don't cover, so the panel
 * would jump off-screen on close instead of sliding.
 *
 * With `data-stacked` set, a card has been stacked over it: it recedes
 * from its top edge and dims, so its title row stays readable above the
 * card while everything else reads as behind.
 *
 * `overflow-clip`, not `overflow-hidden`: a hidden box is still a scroll
 * container, so `scrollIntoView` on a deep-linked section scrolled the
 * shell itself and pushed the header and tabs out of sight.
 */
const floatingSheetClassName =
	"surface-float fixed top-[100px] right-2.5 bottom-2.5 z-40 flex w-floating-panel origin-left flex-col overflow-clip rounded-panel text-ink-1 text-sm outline-none data-stacked:-translate-x-3.5 data-stacked:scale-[0.965] data-stacked:brightness-[0.55] motion-reduce:data-stacked:translate-x-0 motion-reduce:data-stacked:scale-100";

/*
 * The docked sheet is the same panel pinned flush to the right edge: full
 * height, square, one hairline on its inner edge, no gap. It takes the same
 * `motion` classes as the floating one (all of them slide it in along `transform`),
 * or `motion="external"` for none: the caller animates the element itself.
 */
const dockedSheetClassName =
	"surface-float fixed inset-y-0 right-0 z-40 flex w-[480px] max-w-[90vw] flex-col overflow-clip rounded-none text-ink-1 text-sm outline-none";

/*
 * A sheet over the canvas keeps a strip of it clear at its left: a card at
 * 100% (248px) and 24px of air on each side, plus the sheet's own 12px
 * inset, is 308px. The service sheet is up to 1040px wide, which on a
 * laptop leaves under 300px, and below ~1050px nothing at all, so no
 * amount of panning could bring a clicked card clear of it; this cap is
 * what lets the camera do so at every width. From 760px down the sheet is
 * full-screen and the canvas is not meant to show.
 */
const clearOfCanvasClassName = "min-[760px]:max-w-[calc(100%-308px)]";

/** The narrowest a resizable sheet can be dragged, in px. */
const SHEET_MIN_WIDTH = 360;
/** The widest a resizable sheet can be dragged, as a fraction of the viewport. */
const SHEET_MAX_VIEWPORT_FRACTION = 0.9;
/** How far an arrow key moves the resize handle, in px. */
const SHEET_RESIZE_KEY_STEP = 16;

function maxSheetWidth(): number {
	return Math.max(
		SHEET_MIN_WIDTH,
		Math.floor(window.innerWidth * SHEET_MAX_VIEWPORT_FRACTION),
	);
}

function clampSheetWidth(px: number): number {
	return Math.round(Math.min(Math.max(px, SHEET_MIN_WIDTH), maxSheetWidth()));
}

type SheetPopupStyle = SheetPrimitive.Popup.Props["style"];

/** `style` with an inline `width` merged in, whichever form the caller used. */
function withWidth(style: SheetPopupStyle, width: string): SheetPopupStyle {
	if (typeof style === "function") {
		return (state) => ({ ...style(state), width });
	}
	return { ...style, width };
}

/**
 * A 6px drag handle on the sheet's inner edge (the left edge of a right-hand
 * sheet): a vertical separator the pointer drags and ←/→ move by 16px. It
 * reports the new width in px, clamped to [360px, 90vw]; the sheet applies it.
 */
function SheetResizeHandle({
	popupRef,
	edge,
	onResize,
}: {
	popupRef: React.RefObject<HTMLElement | null>;
	/** Which edge of the sheet the handle sits on. */
	edge: "left" | "right";
	onResize: (px: number) => void;
}) {
	const [dragging, setDragging] = React.useState(false);
	const [measured, setMeasured] = React.useState<number | undefined>(undefined);
	const drag = React.useRef<{
		pointerId: number;
		startX: number;
		startWidth: number;
	} | null>(null);
	// Dragging the left edge to the right narrows the sheet; the right edge widens it.
	const sign = edge === "left" ? -1 : 1;

	const resizeTo = (px: number) => {
		const next = clampSheetWidth(px);
		setMeasured(next);
		onResize(next);
	};
	const endDrag = (event: React.PointerEvent<HTMLDivElement>) => {
		if (drag.current?.pointerId !== event.pointerId) return;
		drag.current = null;
		setDragging(false);
		if (event.currentTarget.hasPointerCapture(event.pointerId)) {
			event.currentTarget.releasePointerCapture(event.pointerId);
		}
	};

	return (
		<div
			role="separator"
			aria-orientation="vertical"
			aria-label="Resize panel"
			aria-valuemin={SHEET_MIN_WIDTH}
			aria-valuemax={maxSheetWidth()}
			aria-valuenow={measured}
			tabIndex={0}
			data-slot="sheet-resize-handle"
			data-dragging={dragging || undefined}
			onFocus={() => {
				const width = popupRef.current?.offsetWidth;
				if (width !== undefined) setMeasured(Math.round(width));
			}}
			onPointerDown={(event) => {
				const popup = popupRef.current;
				if (event.button !== 0 || !popup) return;
				event.preventDefault();
				event.currentTarget.setPointerCapture(event.pointerId);
				drag.current = {
					pointerId: event.pointerId,
					startX: event.clientX,
					startWidth: popup.offsetWidth,
				};
				setDragging(true);
			}}
			onPointerMove={(event) => {
				const active = drag.current;
				if (active?.pointerId !== event.pointerId) return;
				resizeTo(active.startWidth + sign * (event.clientX - active.startX));
			}}
			onPointerUp={endDrag}
			onPointerCancel={endDrag}
			onKeyDown={(event) => {
				const popup = popupRef.current;
				if (!popup) return;
				// The arrow names where the edge moves; the width follows from the side.
				const direction =
					event.key === "ArrowLeft" ? -1 : event.key === "ArrowRight" ? 1 : 0;
				if (direction !== 0) {
					event.preventDefault();
					resizeTo(
						popup.offsetWidth + sign * direction * SHEET_RESIZE_KEY_STEP,
					);
				} else if (event.key === "Home") {
					event.preventDefault();
					resizeTo(SHEET_MIN_WIDTH);
				} else if (event.key === "End") {
					event.preventDefault();
					resizeTo(maxSheetWidth());
				}
			}}
			className={cn(
				"absolute inset-y-0 z-20 w-1.5 cursor-col-resize touch-none outline-none",
				edge === "left" ? "left-0" : "right-0",
				"after:absolute after:inset-y-3 after:w-0.5 after:rounded-full after:bg-accent-ink after:opacity-0 after:transition-opacity after:duration-quick after:ease-out after:content-['']",
				edge === "left" ? "after:left-px" : "after:right-px",
				"hover:after:opacity-100 focus-visible:after:opacity-100 data-dragging:after:opacity-100",
			)}
		/>
	);
}

function SheetContent({
	className,
	children,
	side = "right",
	variant = "edge",
	motion = "plain",
	look = "service",
	showOverlay = variant === "edge",
	showCloseButton = true,
	clearOfCanvas = look === "service",
	ref,
	width,
	resizable = false,
	onWidthChange,
	style,
	...props
}: SheetPrimitive.Popup.Props & {
	side?: "top" | "right" | "bottom" | "left";
	/**
	 * `floating` is the inset right-hand detail panel; `docked` the same panel
	 * pinned flush to the right edge (full height, square); `side` is ignored
	 * by both.
	 */
	variant?: "edge" | "floating" | "docked";
	/**
	 * Floating and docked panel motion. `slide` is the service panel and
	 * `plain` the shared 320ms settle. `external` puts no motion on the
	 * element: the caller animates it (the service inspector does, with
	 * `usePanelSlide`), and the dialog waits for that animation before it
	 * unmounts the panel.
	 */
	motion?: "plain" | "slide" | "external";
	/**
	 * `service` is the service panel, `panel` the shared panel (project
	 * variables, cluster). Both wear the same float surface; the look only
	 * decides whether `clearOfCanvas` is on by default.
	 */
	look?: "service" | "panel";
	/**
	 * Cap a floating sheet's width so a strip of the canvas stays clear at
	 * its left for the camera to centre a clicked node in. On for the
	 * service sheet, and for any other sheet that opens over the canvas.
	 */
	clearOfCanvas?: boolean;
	/** The popup element, for a caller that drives its motion. */
	ref?: React.Ref<HTMLDivElement>;
	showOverlay?: boolean;
	showCloseButton?: boolean;
	/**
	 * The panel's width: a number is px, a string any CSS length. Unset keeps
	 * the variant's own width. Applied inline; below 760px a slide-motion
	 * floating panel is full-screen regardless. Ignored by top/bottom sheets.
	 */
	width?: number | string;
	/**
	 * Renders a 6px drag handle on the panel's inner edge (pointer drag, or
	 * ←/→ for 16px steps), clamped to [360px, 90vw]. Reports through
	 * `onWidthChange`; pass the number back as `width` to control it, or leave
	 * `width` unset and the panel keeps the dragged width itself. Not for
	 * top/bottom sheets.
	 */
	resizable?: boolean;
	/** The width in px the reader dragged or keyed the panel to. */
	onWidthChange?: (px: number) => void;
}) {
	const instant = React.useContext(SheetInstantContext);
	const popupRef = React.useRef<HTMLElement | null>(null);
	const mergedRef = React.useCallback(
		(element: HTMLDivElement | null) => {
			popupRef.current = element;
			if (typeof ref === "function") ref(element);
			else if (ref) ref.current = element;
		},
		[ref],
	);

	// The width a drag or key press set, until the caller hands one back.
	const [liveWidth, setLiveWidth] = React.useState<number | undefined>(
		undefined,
	);
	const [seenWidth, setSeenWidth] = React.useState(width);
	if (width !== seenWidth) {
		setSeenWidth(width);
		setLiveWidth(undefined);
	}
	const horizontal =
		variant === "floating" ||
		variant === "docked" ||
		side === "left" ||
		side === "right";
	const appliedWidth = !horizontal
		? undefined
		: liveWidth !== undefined
			? `${liveWidth}px`
			: typeof width === "number"
				? `${width}px`
				: width;
	// A right-hand or floating/docked sheet is resized from its left edge.
	const handleEdge = variant !== "edge" || side === "right" ? "left" : "right";

	return (
		<SheetPortal>
			{showOverlay ? <SheetOverlay /> : null}
			<SheetPrimitive.Popup
				ref={mergedRef}
				data-slot="sheet-content"
				data-side={side}
				data-variant={variant}
				data-motion={variant === "edge" ? undefined : motion}
				data-instant={instant || undefined}
				className={cn(
					variant === "floating"
						? floatingSheetClassName
						: variant === "docked"
							? dockedSheetClassName
							: edgeSheetClassName,
					variant === "floating" && clearOfCanvas && clearOfCanvasClassName,
					variant !== "edge" &&
						motion !== "external" &&
						(motion === "slide" ? "panel-shell" : "panel-shell-simple"),
					// An unset width keeps the variant's own; a set one lifts the edge
					// sheet's `sm:max-w-sm` cap and yields to the full-screen sheet.
					appliedWidth !== undefined &&
						variant === "edge" &&
						"data-[side=left]:sm:max-w-none data-[side=right]:sm:max-w-none",
					appliedWidth !== undefined &&
						variant !== "edge" &&
						motion === "slide" &&
						"max-[759px]:w-auto!",
					className,
				)}
				style={
					appliedWidth === undefined ? style : withWidth(style, appliedWidth)
				}
				{...props}
			>
				{children}
				{showCloseButton && (
					<SheetPrimitive.Close
						data-slot="sheet-close"
						render={
							<Button
								variant="ghost"
								className={cn(
									"absolute right-4",
									variant === "edge" ? "top-4" : "top-[18px]",
								)}
								size="icon"
							/>
						}
					>
						<X />
						<span className="sr-only">Close</span>
					</SheetPrimitive.Close>
				)}
				{resizable && horizontal ? (
					<SheetResizeHandle
						popupRef={popupRef}
						edge={handleEdge}
						onResize={(px) => {
							setLiveWidth(px);
							onWidthChange?.(px);
						}}
					/>
				) : null}
			</SheetPrimitive.Popup>
		</SheetPortal>
	);
}

function SheetHeader({ className, ...props }: React.ComponentProps<"div">) {
	return (
		<div
			data-slot="sheet-header"
			className={cn(
				"flex shrink-0 flex-col gap-3.5 border-line border-b px-5 pt-[18px]",
				className,
			)}
			{...props}
		/>
	);
}

/** The scrolling middle of a floating sheet. */
function SheetBody({ className, ...props }: React.ComponentProps<"div">) {
	return (
		<div
			data-slot="sheet-body"
			className={cn(
				"min-h-0 flex-1 overflow-y-auto overflow-x-hidden overscroll-contain p-5",
				className,
			)}
			{...props}
		/>
	);
}

function SheetFooter({ className, ...props }: React.ComponentProps<"div">) {
	return (
		<div
			data-slot="sheet-footer"
			className={cn(
				"mt-auto flex shrink-0 items-center gap-3 border-line border-t px-5 py-3",
				className,
			)}
			{...props}
		/>
	);
}

function SheetTitle({ className, ...props }: SheetPrimitive.Title.Props) {
	return (
		<SheetPrimitive.Title
			data-slot="sheet-title"
			className={cn(
				"font-display font-semibold text-[19px] text-ink-1 leading-[1.1] tracking-[-0.02em]",
				className,
			)}
			{...props}
		/>
	);
}

function SheetDescription({
	className,
	...props
}: SheetPrimitive.Description.Props) {
	return (
		<SheetPrimitive.Description
			data-slot="sheet-description"
			className={cn("font-mono text-ink-3 text-xs", className)}
			{...props}
		/>
	);
}

export {
	Sheet,
	SheetBody,
	SheetContent,
	SheetDescription,
	SheetFooter,
	SheetHeader,
	SheetTitle,
};
