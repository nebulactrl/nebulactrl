"use client";

// beui.dev/components/motion/context-menu

import { cn } from "cn";
import {
	Children,
	cloneElement,
	createContext,
	isValidElement,
	type ReactElement,
	type KeyboardEvent as ReactKeyboardEvent,
	type MouseEvent as ReactMouseEvent,
	type ReactNode,
	type PointerEvent as ReactPointerEvent,
	type Ref,
	useCallback,
	useContext,
	useEffect,
	useId,
	useLayoutEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { createPortal } from "react-dom";
import { Kbd } from "#/components/ui/kbd";
import { holdSelection, TOUCH_GESTURE_CONTENT_CLASS } from "#/lib/touch";

type OpenModality = "pointer" | "keyboard" | "touch";
type MenuPoint = { x: number; y: number };

const VIEWPORT_PADDING = 8;
const LONG_PRESS_DELAY = 520;
const LONG_PRESS_TOLERANCE = 10;

type TriggerElementProps = React.HTMLAttributes<HTMLElement> & {
	ref?: Ref<HTMLElement>;
};

interface ContextMenuContextValue {
	open: boolean;
	setOpen: (open: boolean) => void;
	openAt: (point: MenuPoint, modality: OpenModality) => void;
	point: MenuPoint;
	modality: OpenModality;
	menuId: string;
	triggerRef: React.MutableRefObject<HTMLElement | null>;
	/** What had focus when the menu opened, so Escape can hand it back. */
	restoreFocusRef: React.MutableRefObject<HTMLElement | null>;
	contentRef: React.MutableRefObject<HTMLDivElement | null>;
	activeId: string | null;
	setActiveId: (id: string | null) => void;
}

const ContextMenuContext = createContext<ContextMenuContextValue | null>(null);

function useContextMenuContext(component: string) {
	const context = useContext(ContextMenuContext);
	if (!context) {
		throw new Error(`${component} must be used within <ContextMenu>`);
	}
	return context;
}

function assignRef<T>(ref: Ref<T> | undefined, value: T | null) {
	if (typeof ref === "function") {
		ref(value);
	} else if (ref) {
		ref.current = value;
	}
}

function getEnabledItems(container: HTMLElement | null) {
	if (!container) return [];
	return Array.from(
		container.querySelectorAll<HTMLElement>(
			'[data-context-menu-item="true"]:not([data-disabled="true"])',
		),
	);
}

export interface ContextMenuProps {
	children: ReactNode;
	open?: boolean;
	defaultOpen?: boolean;
	onOpenChange?: (open: boolean) => void;
	className?: string;
}

export function ContextMenu({
	children,
	open: controlledOpen,
	defaultOpen = false,
	onOpenChange,
	className,
}: ContextMenuProps) {
	const [internalOpen, setInternalOpen] = useState(defaultOpen);
	const [point, setPoint] = useState<MenuPoint>({ x: 0, y: 0 });
	const [modality, setModality] = useState<OpenModality>("pointer");
	const [activeId, setActiveId] = useState<string | null>(null);
	const controlled = controlledOpen !== undefined;
	const open = controlled ? controlledOpen : internalOpen;
	const triggerRef = useRef<HTMLElement | null>(null);
	const restoreFocusRef = useRef<HTMLElement | null>(null);
	const contentRef = useRef<HTMLDivElement | null>(null);
	const menuId = useId();

	const setOpen = useCallback(
		(next: boolean) => {
			if (!controlled) setInternalOpen(next);
			onOpenChange?.(next);
			if (!next) setActiveId(null);
		},
		[controlled, onOpenChange],
	);

	const openAt = useCallback(
		(nextPoint: MenuPoint, nextModality: OpenModality) => {
			// A second right-click while open only moves the menu; focus is in
			// it by then, and the element to return to is the first one.
			if (!open) {
				const active = document.activeElement;
				restoreFocusRef.current =
					active instanceof HTMLElement && active !== document.body
						? active
						: null;
			}
			setPoint(nextPoint);
			setModality(nextModality);
			setActiveId(null);
			setOpen(true);
		},
		[open, setOpen],
	);

	useEffect(() => {
		if (!open) return;

		const onPointerDown = (event: PointerEvent) => {
			if (!contentRef.current?.contains(event.target as Node)) setOpen(false);
		};
		const onWindowChange = () => setOpen(false);

		window.addEventListener("pointerdown", onPointerDown);
		window.addEventListener("resize", onWindowChange);
		window.addEventListener("scroll", onWindowChange);
		window.addEventListener("blur", onWindowChange);
		return () => {
			window.removeEventListener("pointerdown", onPointerDown);
			window.removeEventListener("resize", onWindowChange);
			window.removeEventListener("scroll", onWindowChange);
			window.removeEventListener("blur", onWindowChange);
		};
	}, [open, setOpen]);

	const value = useMemo<ContextMenuContextValue>(
		() => ({
			open,
			setOpen,
			openAt,
			point,
			modality,
			menuId,
			triggerRef,
			restoreFocusRef,
			contentRef,
			activeId,
			setActiveId,
		}),
		[open, setOpen, openAt, point, modality, menuId, activeId],
	);

	return (
		<ContextMenuContext.Provider value={value}>
			<div className={cn("contents", className)}>{children}</div>
		</ContextMenuContext.Provider>
	);
}

export type ContextMenuOpener = (
	point: { x: number; y: number },
	modality: "pointer" | "keyboard" | "touch",
) => void;

/**
 * Opens the surrounding menu at a screen point without a pointer or key
 * event on its trigger: for a surface that decides what the menu is for
 * after a key press that did not land on the trigger (focus on the page,
 * a selection made earlier).
 */
export function useContextMenuOpener(): ContextMenuOpener {
	return useContextMenuContext("useContextMenuOpener").openAt;
}

export interface ContextMenuTriggerProps {
	children: ReactElement<TriggerElementProps>;
	disabled?: boolean;
	className?: string;
	/**
	 * Fires with the element under the pointer (or the focused element, for
	 * a keyboard invocation) the moment the menu is about to open — before
	 * any state settles, so the caller can decide what the menu is for
	 * (a canvas node vs. empty canvas) from the same `closest()` lookup for
	 * every modality: right-click, long-press and Shift+F10/ContextMenu.
	 */
	onTrigger?: (target: HTMLElement | null) => void;
	/**
	 * Leaves the event alone when it returns true, so the platform's own menu
	 * (a text field's paste and spellcheck) or a portalled layer's own handling
	 * keeps it. Checked before anything else for right-click and the keyboard.
	 */
	ignore?: (target: HTMLElement) => boolean;
	/**
	 * Whether a touch or pen press held in place opens the menu, and the
	 * trigger drops its touch selection opt-out. A surface that wraps the
	 * whole console turns this off: it must not take text selection from
	 * every screen, and the platform still sends a `contextmenu` for a long
	 * press on most touch browsers.
	 * @default true
	 */
	longPress?: boolean;
}

export function ContextMenuTrigger({
	children,
	disabled = false,
	className,
	onTrigger,
	ignore,
	longPress = true,
}: ContextMenuTriggerProps) {
	const context = useContextMenuContext("ContextMenuTrigger");
	const longPressTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
	const touchOrigin = useRef<MenuPoint | null>(null);
	const touchTarget = useRef<HTMLElement | null>(null);
	const releaseSelection = useRef<(() => void) | null>(null);

	const cancelLongPress = useCallback(() => {
		if (longPressTimer.current) {
			clearTimeout(longPressTimer.current);
			longPressTimer.current = null;
		}
		touchOrigin.current = null;
	}, []);

	// Held for the whole press, not just the timer: a gesture that turned into a
	// drag must not paint a selection under the finger either.
	const endPress = useCallback(() => {
		cancelLongPress();
		releaseSelection.current?.();
		releaseSelection.current = null;
	}, [cancelLongPress]);

	useEffect(() => endPress, [endPress]);

	if (!isValidElement(children)) {
		throw new Error("<ContextMenuTrigger> requires a single React element");
	}

	const childProps = children.props;
	const childRef = children.props.ref;

	const onPointerDown = (event: ReactPointerEvent<HTMLElement>) => {
		childProps.onPointerDown?.(event);
		// A pen presses the same way a finger does and gets no `contextmenu` out
		// of the platform for it, so it holds to open too. A mouse has the right
		// button and is left to `onContextMenu`.
		const pressToOpen =
			event.pointerType === "touch" || event.pointerType === "pen";
		if (event.defaultPrevented || disabled || !longPress || !pressToOpen)
			return;

		// `pointer-coarse:select-none` misses this press on a laptop whose mouse
		// is the primary pointer and whose touchscreen is not, and the platform's
		// own long-press selection then claims the gesture and cancels ours. The
		// press is the only thing that knows which input is on the glass, so it
		// takes selection away itself — for its own duration, and no longer.
		releaseSelection.current?.();
		releaseSelection.current = holdSelection(event.currentTarget);

		const origin = { x: event.clientX, y: event.clientY };
		touchOrigin.current = origin;
		touchTarget.current = event.target as HTMLElement;
		longPressTimer.current = setTimeout(() => {
			onTrigger?.(touchTarget.current);
			context.openAt(origin, "touch");
			longPressTimer.current = null;
			touchOrigin.current = null;
		}, LONG_PRESS_DELAY);
	};

	const onPointerMove = (event: ReactPointerEvent<HTMLElement>) => {
		childProps.onPointerMove?.(event);
		const origin = touchOrigin.current;
		if (
			origin &&
			Math.hypot(event.clientX - origin.x, event.clientY - origin.y) >
				LONG_PRESS_TOLERANCE
		) {
			cancelLongPress();
		}
	};

	const onKeyDown = (event: ReactKeyboardEvent<HTMLElement>) => {
		childProps.onKeyDown?.(event);
		if (event.defaultPrevented || disabled) return;
		if (ignore?.(event.target as HTMLElement)) return;
		if (event.key !== "ContextMenu" && !(event.shiftKey && event.key === "F10"))
			return;

		event.preventDefault();
		// The focused element, not the trigger's own (much larger) bounding
		// box: a canvas node invoking this by keyboard opens the menu beside
		// itself, not at the centre of the whole canvas.
		const target = event.target as HTMLElement;
		const rect = target.getBoundingClientRect();
		onTrigger?.(target);
		context.openAt(
			{
				x: rect.left + Math.min(24, rect.width / 2),
				y: rect.top + rect.height / 2,
			},
			"keyboard",
		);
	};

	return cloneElement(children, {
		ref: (node: HTMLElement | null) => {
			context.triggerRef.current = node;
			assignRef(childRef, node);
		},
		"aria-controls": context.open ? context.menuId : undefined,
		"aria-haspopup": "menu",
		"aria-expanded": context.open,
		// The long press is ours: without this iOS runs its own on the same
		// gesture and drops the selection callout and its handles on top of the
		// menu we just opened. Only the press gesture is ours though — the child
		// is the consumer's content, so a mouse can still select the text in it
		// and right-click the selection. `touch-none` stays off too: the page
		// still has to scroll from the trigger.
		className: cn(
			longPress && TOUCH_GESTURE_CONTENT_CLASS,
			childProps.className,
			className,
		),
		onContextMenu: (event: ReactMouseEvent<HTMLElement>) => {
			childProps.onContextMenu?.(event);
			if (event.defaultPrevented || disabled) return;
			if (ignore?.(event.target as HTMLElement)) return;
			event.preventDefault();
			endPress();
			onTrigger?.(event.target as HTMLElement);
			context.openAt({ x: event.clientX, y: event.clientY }, "pointer");
		},
		onKeyDown,
		onPointerDown,
		onPointerMove,
		onPointerUp: (event: ReactPointerEvent<HTMLElement>) => {
			childProps.onPointerUp?.(event);
			endPress();
		},
		onPointerCancel: (event: ReactPointerEvent<HTMLElement>) => {
			childProps.onPointerCancel?.(event);
			endPress();
		},
	});
}

export interface ContextMenuContentProps {
	children: ReactNode;
	className?: string;
	ariaLabel?: string;
	/** Shown once above the items, e.g. "checkout-api · production". */
	title?: string;
}

export function ContextMenuContent({
	children,
	className,
	ariaLabel,
	title,
}: ContextMenuContentProps) {
	const context = useContextMenuContext("ContextMenuContent");
	const [mounted, setMounted] = useState(false);
	const [position, setPosition] = useState<MenuPoint>(context.point);
	const typeahead = useRef("");
	const typeaheadTimer = useRef<number | null>(null);
	// The menu scales in from the point it was invoked at (origin-aware) and
	// opens with no animation at all when the keyboard invoked it.

	useEffect(() => setMounted(true), []);

	// Clamps the menu into the viewport on both axes. `useLayoutEffect` runs
	// after the DOM commit but before paint, so the reader never sees the
	// unclamped frame at `context.point` even though that's what the content
	// first renders at (its own size is unknown until it exists in the DOM to
	// measure). A ResizeObserver re-clamps when the rows change height after
	// mount, and on window resize. Layout size (offsetWidth/Height) is used,
	// not getBoundingClientRect, so the unfold scale never skews the measure.
	// The menu itself is capped to the viewport and scrolls, so a destructive
	// last row stays reachable even when the menu is taller than the window.
	useLayoutEffect(() => {
		const content = context.contentRef.current;
		if (!mounted || !content || !context.open) return;
		const clamp = () => {
			const width = content.offsetWidth;
			const height = content.offsetHeight;
			const maxLeft = window.innerWidth - width - VIEWPORT_PADDING;
			const left = Math.max(
				VIEWPORT_PADDING,
				Math.min(Math.max(context.point.x, VIEWPORT_PADDING), maxLeft),
			);
			const maxTop = window.innerHeight - height - VIEWPORT_PADDING;
			let top = Math.max(context.point.y, VIEWPORT_PADDING);
			if (top > maxTop) {
				// Prefer opening upward from the pointer when it fits there,
				// otherwise slide up until the bottom edge is inside.
				const flipped = context.point.y - height;
				top = flipped >= VIEWPORT_PADDING ? flipped : maxTop;
			}
			top = Math.max(VIEWPORT_PADDING, top);
			setPosition((previous) =>
				previous.x === left && previous.y === top
					? previous
					: { x: left, y: top },
			);
		};
		clamp();
		const observer = new ResizeObserver(clamp);
		observer.observe(content);
		window.addEventListener("resize", clamp);
		return () => {
			observer.disconnect();
			window.removeEventListener("resize", clamp);
		};
	}, [context.point, context.open, mounted, context.contentRef]);

	// On every opening, not only the first mount: this component stays
	// mounted and renders nothing while closed. A keyboard opening lands on
	// the first item, as a native menu does; a pointer opening only takes
	// focus, so the arrow keys work without lighting an item away from the
	// pointer.
	useEffect(() => {
		if (!context.open) return;
		const frame = requestAnimationFrame(() => {
			const content = context.contentRef.current;
			if (context.modality === "keyboard") {
				getEnabledItems(content)[0]?.focus({ preventScroll: true });
			} else {
				content?.focus({ preventScroll: true });
			}
		});
		return () => cancelAnimationFrame(frame);
	}, [context.open, context.modality, context.contentRef]);

	useEffect(() => () => clearTimeout(typeaheadTimer.current ?? undefined), []);

	const moveFocus = (direction: 1 | -1) => {
		const items = getEnabledItems(context.contentRef.current);
		if (items.length === 0) return;
		const current = items.indexOf(document.activeElement as HTMLElement);
		const next =
			current < 0 ? 0 : (current + direction + items.length) % items.length;
		items[next]?.focus();
	};

	const onKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
		if (event.key === "Escape") {
			event.preventDefault();
			context.setOpen(false);
			(context.restoreFocusRef.current ?? context.triggerRef.current)?.focus();
			return;
		}
		if (event.key === "Tab") {
			context.setOpen(false);
			return;
		}
		if (event.key === "ArrowDown" || event.key === "ArrowUp") {
			event.preventDefault();
			moveFocus(event.key === "ArrowDown" ? 1 : -1);
			return;
		}
		if (event.key === "Home" || event.key === "End") {
			event.preventDefault();
			const items = getEnabledItems(context.contentRef.current);
			items[event.key === "Home" ? 0 : items.length - 1]?.focus();
			return;
		}
		if (
			event.key.length === 1 &&
			!event.ctrlKey &&
			!event.metaKey &&
			!event.altKey
		) {
			typeahead.current += event.key.toLocaleLowerCase();
			clearTimeout(typeaheadTimer.current ?? undefined);
			typeaheadTimer.current = window.setTimeout(() => {
				typeahead.current = "";
			}, 500);
			const match = getEnabledItems(context.contentRef.current).find((item) =>
				(item.dataset.label ?? item.textContent ?? "")
					.trim()
					.toLocaleLowerCase()
					.startsWith(typeahead.current),
			);
			match?.focus();
		}
	};

	// Mounted only while open, so the entrance animation replays on every
	// genuinely new invocation.
	if (!mounted || !context.open) return null;

	return createPortal(
		<div
			ref={context.contentRef}
			id={context.menuId}
			role="menu"
			aria-label={ariaLabel ?? title ?? "Context menu"}
			tabIndex={-1}
			style={{
				left: position.x,
				top: position.y,
				maxHeight: `calc(100dvh - ${VIEWPORT_PADDING * 2}px)`,
				transformOrigin: `${Math.max(0, context.point.x - position.x)}px ${Math.max(0, context.point.y - position.y)}px`,
			}}
			onKeyDown={onKeyDown}
			onContextMenu={(event) => event.preventDefault()}
			onMouseLeave={() => context.setActiveId(null)}
			className={cn(
				"surface-pop fixed z-[100] w-[236px] overflow-y-auto overflow-x-hidden overscroll-contain rounded-menu p-1 text-ink-1 outline-none",
				context.modality !== "keyboard" &&
					"animate-nc-unfold motion-reduce:animate-none",
				className,
			)}
		>
			{title ? (
				<div className="truncate px-2.5 pt-2 pb-1.5 font-medium font-mono text-2xs text-ink-3 leading-[1.2]">
					{title}
				</div>
			) : null}
			{children}
		</div>,
		document.body,
	);
}

type ContextMenuItemTone = "default" | "destructive";

export interface ContextMenuItemProps {
	children: ReactNode;
	onSelect?: () => void;
	disabled?: boolean;
	closeOnSelect?: boolean;
	tone?: ContextMenuItemTone;
	inset?: boolean;
	className?: string;
	textValue?: string;
}

/**
 * A row's label is its own span inside the button, so the text owner has no
 * padding, radius or gap. Each run of plain text is wrapped; icons and
 * shortcuts stay direct children.
 */
function wrapTextRuns(children: ReactNode): ReactNode {
	const nodes = Children.toArray(children);
	const out: ReactNode[] = [];
	let run: Array<string | number> = [];
	const flush = () => {
		if (run.length === 0) return;
		out.push(
			<span key={`text-${out.length}`} className="flex-1 text-left">
				{run}
			</span>,
		);
		run = [];
	};
	for (const node of nodes) {
		if (typeof node === "string" || typeof node === "number") run.push(node);
		else {
			flush();
			out.push(node);
		}
	}
	flush();
	return out;
}

export function ContextMenuItem({
	children,
	onSelect,
	disabled = false,
	closeOnSelect = true,
	tone = "default",
	inset = false,
	className,
	textValue,
}: ContextMenuItemProps) {
	const context = useContextMenuContext("ContextMenuItem");
	const id = useId();
	const active = context.activeId === id;

	return (
		<button
			type="button"
			id={id}
			role="menuitem"
			disabled={disabled}
			data-context-menu-item="true"
			data-disabled={disabled ? "true" : undefined}
			data-label={textValue}
			tabIndex={-1}
			onFocus={() => context.setActiveId(id)}
			onPointerMove={(event) => {
				if (!disabled && event.pointerType !== "touch")
					event.currentTarget.focus();
			}}
			onClick={() => {
				if (disabled) return;
				onSelect?.();
				if (closeOnSelect) context.setOpen(false);
			}}
			className={cn(
				"relative flex min-h-[30px] w-full select-none items-center gap-4 rounded-md px-2.5 text-left font-normal text-[13px] leading-none outline-none",
				"disabled:pointer-events-none disabled:opacity-40",
				"[&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-3.5",
				inset && "pl-8",
				// The highlight is a plain fill: it follows focus with no animation.
				tone === "destructive"
					? cn("text-destructive-text", active && "bg-destructive-hover")
					: active
						? "bg-white/7 text-ink-1"
						: "text-ink-2",
				className,
			)}
		>
			{wrapTextRuns(children)}
		</button>
	);
}

export interface ContextMenuLabelProps {
	children: ReactNode;
	inset?: boolean;
	className?: string;
}

export function ContextMenuLabel({
	children,
	inset = false,
	className,
}: ContextMenuLabelProps) {
	return (
		<div
			className={cn(
				"type-label px-2.5 pt-2 pb-1.5",
				inset && "pl-8",
				className,
			)}
		>
			{children}
		</div>
	);
}

export interface ContextMenuSeparatorProps {
	className?: string;
}

export function ContextMenuSeparator({ className }: ContextMenuSeparatorProps) {
	return <hr className={cn("-mx-1 my-1 h-px border-0 bg-line", className)} />;
}

export interface ContextMenuShortcutProps {
	children: ReactNode;
	className?: string;
}

export function ContextMenuShortcut({
	children,
	className,
}: ContextMenuShortcutProps) {
	return (
		<Kbd aria-hidden="true" className={cn("ml-auto", className)}>
			{children}
		</Kbd>
	);
}
