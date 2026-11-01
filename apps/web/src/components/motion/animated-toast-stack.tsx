"use client";

// beui.dev/components/motion/animated-toast-stack
//
// Forked to match the console's toast stack pixel-for-pixel and
// motion-for-motion: stacked 10px apart with scale steps, hover expands to
// 8px gaps (real measured heights, not guesses), a front-toast drain bar
// that pauses on hover, loading→ok glyphs, and up to two actions. The
// adapter in `ui/toast.tsx` is its only caller, so its shape follows the
// design rather than the upstream beUI API.

import { blurs, exact, scales } from "@nebulactrl/tokens";
import { cn } from "cn";
import {
	AnimatePresence,
	motion,
	type Transition,
	useReducedMotion,
} from "motion/react";
import {
	type ReactNode,
	type PointerEvent as ReactPointerEvent,
	useCallback,
	useEffect,
	useLayoutEffect,
	useRef,
	useState,
} from "react";
import { createPortal } from "react-dom";
import { type CalloutTone, ToneIcon } from "#/components/common/tone-icon";
import { CircleCheckIcon } from "#/components/icons/animated/circle-check-icon";
import { useHoverCapable } from "#/lib/hooks/use-hover-capable";
import { SPRING, SPRING_EASE, SPRING_MOTION } from "#/lib/transitions";
import { toastStackLayout } from "./toast-stack-layout";

export type ToastKind = "info" | "success" | "warning" | "error" | "loading";

export interface ToastActionConfig {
	label: ReactNode;
	onClick: () => void;
}

interface AnimatedToast {
	id: string;
	title: ReactNode;
	description?: ReactNode | undefined;
	kind: ToastKind;
	/**
	 * `check` shows a circle-check that draws its tick in place of the plain
	 * success icon: "your deploy was sent". Other kinds ignore it.
	 */
	glyph?: "check" | undefined;
	/** The outline action: "View logs". */
	action?: ToastActionConfig | undefined;
	/** The solid, primary action: "Undo". */
	primaryAction?: ToastActionConfig | undefined;
	/** Milliseconds before auto-dismiss; 0 never dismisses on its own. */
	duration: number;
	dismissible: boolean;
	createdAt: number;
}

// Every field explicitly nullable (not merely optional) — see `ToastPatch`
// below for why `exactOptionalPropertyTypes` requires this.
type ToastInput = {
	[K in keyof Omit<AnimatedToast, "createdAt">]?: AnimatedToast[K] | undefined;
} & Pick<AnimatedToast, "title">;

type ToastPatch = {
	[K in keyof Omit<AnimatedToast, "id" | "createdAt">]?:
		| AnimatedToast[K]
		| undefined;
};

type ToastDirection = "left" | "right" | "down";

export interface AnimatedToastStore {
	subscribe(listener: () => void): () => void;
	getSnapshot(): AnimatedToast[];
	add(input: ToastInput): string;
	update(id: string, patch: ToastPatch): void;
	dismiss(id: string): void;
	clear(): void;
	pause(): void;
	resume(): void;
}

let idSeed = 0;

/**
 * A toast store lives outside React: `toast.add(...)` in `ui/toast.tsx` is
 * called from mutations and services with no component on the stack, so the
 * state it mutates cannot be a hook's `useState`.
 */
export function createAnimatedToastStore(
	defaultDuration = 4200,
	limit = 4,
): AnimatedToastStore {
	let toasts: AnimatedToast[] = [];
	let paused = false;
	const listeners = new Set<() => void>();
	const timers = new Map<
		string,
		{ timer: number | undefined; dueAt: number; remaining: number }
	>();

	const notify = () => {
		for (const listener of listeners) listener();
	};

	function clearTimer(id: string) {
		const entry = timers.get(id);
		if (entry?.timer !== undefined) window.clearTimeout(entry.timer);
		timers.delete(id);
	}

	function setTimer(id: string, remaining: number) {
		const dueAt = Date.now() + remaining;
		const timer = window.setTimeout(() => {
			timers.delete(id);
			dismiss(id);
		}, remaining);
		timers.set(id, { timer, dueAt, remaining });
	}

	function schedule(toast: AnimatedToast) {
		clearTimer(toast.id);
		if (toast.duration <= 0) return;
		if (paused) {
			timers.set(toast.id, {
				timer: undefined,
				dueAt: 0,
				remaining: toast.duration,
			});
			return;
		}
		setTimer(toast.id, toast.duration);
	}

	function add(input: ToastInput): string {
		const id = input.id ?? `toast-${Date.now()}-${idSeed++}`;
		const created: AnimatedToast = {
			...input,
			kind: input.kind ?? "info",
			dismissible: input.dismissible ?? true,
			duration: input.duration ?? defaultDuration,
			id,
			createdAt: Date.now(),
		};
		toasts = [created, ...toasts.filter((t) => t.id !== id)].slice(0, limit);
		schedule(created);
		notify();
		return id;
	}

	function update(id: string, patch: ToastPatch) {
		let updated: AnimatedToast | undefined;
		toasts = toasts.map((t) => {
			if (t.id !== id) return t;
			const next: AnimatedToast = { ...t };
			if (patch.title !== undefined) next.title = patch.title;
			if (patch.description !== undefined) next.description = patch.description;
			if (patch.kind !== undefined) next.kind = patch.kind;
			if (patch.glyph !== undefined) next.glyph = patch.glyph;
			if (patch.action !== undefined) next.action = patch.action;
			if (patch.primaryAction !== undefined)
				next.primaryAction = patch.primaryAction;
			if (patch.dismissible !== undefined) next.dismissible = patch.dismissible;
			if (patch.duration !== undefined) {
				next.duration = patch.duration;
				next.createdAt = Date.now();
			}
			updated = next;
			return next;
		});
		if (updated && patch.duration !== undefined) schedule(updated);
		notify();
	}

	function dismiss(id: string) {
		clearTimer(id);
		toasts = toasts.filter((t) => t.id !== id);
		// A stack that empties while hovered is removed from the page, and the
		// browser fires no mouseleave on a removed node, so nothing would ever
		// resume the timers of the toasts that come next.
		if (toasts.length === 0) paused = false;
		notify();
	}

	function clear() {
		for (const id of timers.keys()) clearTimer(id);
		toasts = [];
		// Nothing is left under the pointer to resume the timers; see dismiss.
		paused = false;
		notify();
	}

	function pause() {
		if (paused) return;
		paused = true;
		for (const [id, entry] of timers) {
			if (entry.timer !== undefined) window.clearTimeout(entry.timer);
			timers.set(id, {
				timer: undefined,
				dueAt: 0,
				remaining: Math.max(0, entry.dueAt - Date.now()),
			});
		}
	}

	function resume() {
		if (!paused) return;
		paused = false;
		for (const [id, entry] of timers) {
			setTimer(id, entry.remaining);
		}
	}

	return {
		subscribe: (listener) => {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
		getSnapshot: () => toasts,
		add,
		update,
		dismiss,
		clear,
		pause,
		resume,
	};
}

const SWIPE_THRESHOLD = 90;
// The design's plain `ease` (CSS), as the tuple motion/react's `ease` takes.
const CSS_EASE: [number, number, number, number] = [0.25, 0.1, 0.25, 1];
// A toast leaves on the exit clock (better-ui: 150ms), in seconds for motion/react.
const exitS = exact.exit.duration / 1000;

// An error, warning, info or success toast carries the alert marker: a tone
// icon at the start of the first line (a `ToneIcon`, with a visually-hidden
// label so the kind is never carried by colour alone) on a subtle tone wash
// (`tone-callout`, `data-tone`). A loading toast shows a spinner and no wash.
const KIND_TONE: Record<
	Exclude<ToastKind, "loading">,
	{ tone: CalloutTone; label: string }
> = {
	info: { tone: "info", label: "Info" },
	success: { tone: "success", label: "Success" },
	warning: { tone: "warning", label: "Warning" },
	error: { tone: "danger", label: "Error" },
};

function ToastGlyph({
	kind,
	glyph,
}: {
	kind: ToastKind;
	glyph: AnimatedToast["glyph"];
}) {
	if (kind === "success" && glyph === "check") {
		return (
			<>
				<CircleCheckIcon animate className="size-4 text-status-live" />
				<span className="sr-only">Success</span>
			</>
		);
	}
	if (kind !== "loading") {
		const { tone, label } = KIND_TONE[kind];
		return (
			<>
				<ToneIcon tone={tone} />
				<span className="sr-only">{label}</span>
			</>
		);
	}
	return (
		<>
			<span
				aria-hidden
				className="size-3.5 animate-nc-spin rounded-full border-[1.6px] border-line-strong border-t-accent-ink"
			/>
			<span className="sr-only">Working</span>
		</>
	);
}

function ToastMiniButton({
	action,
	primary,
	onClick,
}: {
	action: ToastActionConfig;
	primary: boolean;
	onClick: () => void;
}) {
	return (
		<button
			type="button"
			onClick={onClick}
			className={cn(
				"pressable h-7 shrink-0 whitespace-nowrap rounded-ctl px-2.5 text-xs leading-none",
				primary ? "btn-primary font-semibold" : "btn-secondary font-medium",
			)}
		>
			{action.label}
		</button>
	);
}

interface ToastCardProps {
	toast: AnimatedToast;
	index: number;
	front: boolean;
	hovered: boolean;
	y: number;
	scale: number;
	opacity: number;
	/** See ToastSlot.forcedHeight. */
	forcedHeight: number | undefined;
	reduce: boolean;
	canDrag: boolean;
	onDismiss: (id: string, direction?: ToastDirection) => void;
	onHeight: (id: string, height: number) => void;
}

function ToastCard({
	toast,
	index,
	front,
	hovered,
	y,
	scale,
	opacity,
	forcedHeight,
	reduce,
	canDrag,
	onDismiss,
	onHeight,
}: ToastCardProps) {
	const cardRef = useRef<HTMLDivElement>(null);
	const dragRef = useRef<HTMLDivElement>(null);
	const dragCleanupRef = useRef<(() => void) | null>(null);
	const dismissTimerRef = useRef<number | undefined>(undefined);
	const [swipeExit, setSwipeExit] = useState<1 | -1 | null>(null);
	const show = hovered || front;
	// The card rises 8px into place and fades in with a CSS transition, so a
	// toast raised or dismissed mid-flight retargets instead of restarting.
	const [entered, setEntered] = useState(reduce);

	// A forced height is the front toast's, not this card's: recording it
	// would lay the expanded stack out with the wrong heights.
	const measured = forcedHeight === undefined;
	useEffect(() => {
		const el = cardRef.current;
		if (!measured || !el || typeof ResizeObserver === "undefined") return;
		const observer = new ResizeObserver(() =>
			onHeight(toast.id, el.offsetHeight),
		);
		observer.observe(el);
		onHeight(toast.id, el.offsetHeight);
		return () => observer.disconnect();
	}, [toast.id, onHeight, measured]);

	useLayoutEffect(() => {
		const el = cardRef.current;
		if (!el || entered) return;
		// Commit the resting-off style first, so the change below transitions.
		void el.getBoundingClientRect();
		setEntered(true);
	}, [entered]);

	useEffect(
		() => () => {
			dragCleanupRef.current?.();
			if (dismissTimerRef.current !== undefined)
				window.clearTimeout(dismissTimerRef.current);
		},
		[],
	);

	const startSwipe = (event: ReactPointerEvent<HTMLDivElement>) => {
		const target = event.target;
		if (
			!canDrag ||
			!toast.dismissible ||
			reduce ||
			!(target instanceof Element) ||
			target.closest("button")
		)
			return;

		const el = dragRef.current;
		if (!el) return;
		dragCleanupRef.current?.();

		const pointerId = event.pointerId;
		const startX = event.clientX;
		const originalTransition = el.style.transition;
		let distance = 0;
		el.style.transition = "opacity .2s";

		const move = (moveEvent: PointerEvent) => {
			if (moveEvent.pointerId !== pointerId) return;
			distance = moveEvent.clientX - startX;
			el.style.translate = `${distance}px 0`;
			el.style.opacity = String(Math.max(0.25, 1 - Math.abs(distance) / 280));
		};
		const cleanup = () => {
			window.removeEventListener("pointermove", move);
			window.removeEventListener("pointerup", finish);
			window.removeEventListener("pointercancel", cancel);
			dragCleanupRef.current = null;
		};
		const settle = (commit: boolean) => {
			cleanup();
			el.style.transition = originalTransition;
			if (commit && Math.abs(distance) > SWIPE_THRESHOLD) {
				const direction = distance > 0 ? 1 : -1;
				setSwipeExit(direction);
				el.style.translate = `${direction * 480}px 0`;
				el.style.opacity = "0";
				dismissTimerRef.current = window.setTimeout(
					() => onDismiss(toast.id, direction > 0 ? "right" : "left"),
					340,
				);
				return;
			}
			el.style.translate = "0 0";
			el.style.opacity = "1";
		};
		function finish(upEvent: PointerEvent) {
			if (upEvent.pointerId !== pointerId) return;
			settle(true);
		}
		function cancel(cancelEvent: PointerEvent) {
			if (cancelEvent.pointerId !== pointerId) return;
			settle(false);
		}

		dragCleanupRef.current = cleanup;
		window.addEventListener("pointermove", move);
		window.addEventListener("pointerup", finish);
		window.addEventListener("pointercancel", cancel);
	};

	const tone = toast.kind === "loading" ? null : KIND_TONE[toast.kind];

	const transition: Transition = reduce
		? { duration: 0 }
		: {
				y: SPRING_MOTION,
				scale: SPRING_MOTION,
				opacity: { duration: 0.28, ease: CSS_EASE },
			};

	return (
		<motion.div
			initial={false}
			animate={{ y, scale, opacity, filter: "blur(0px)" }}
			exit={
				reduce
					? { opacity: 0 }
					: swipeExit
						? { opacity: 0, transition: { duration: 0 } }
						: {
								y: 16,
								scale: scales.medium,
								opacity: 0,
								filter: `blur(${blurs.small}px)`,
								// Leaves faster than it arrived: the exit clock, no travel past 16px.
								transition: {
									y: { duration: exitS, ease: SPRING_EASE },
									scale: { duration: exitS, ease: SPRING_EASE },
									opacity: { duration: exitS, ease: CSS_EASE },
									filter: { duration: exitS, ease: CSS_EASE },
								},
							}
			}
			transition={transition}
			style={{ zIndex: 20 - index, transformOrigin: "50% 100%" }}
			className="absolute inset-x-0 bottom-0"
		>
			<div
				ref={dragRef}
				onPointerDown={startSwipe}
				className="pointer-events-auto touch-pan-y select-none cursor-grab"
				style={{
					transition: reduce
						? undefined
						: `translate .45s ${SPRING}, opacity .2s`,
				}}
			>
				<div
					ref={cardRef}
					data-toast={toast.id}
					data-tone={tone?.tone}
					className={cn(
						"t-toast surface-pop relative flex items-center gap-3 overflow-hidden rounded-menu py-3 pr-2.5 pl-3.5",
						entered && "is-open",
						tone && "tone-callout",
					)}
					style={{ height: forcedHeight }}
				>
					<span
						className={cn(
							"flex size-4 flex-none items-center justify-center",
							tone && "mt-px self-start",
						)}
					>
						<AnimatePresence mode="wait" initial={false}>
							<motion.span
								key={toast.kind}
								initial={reduce ? false : { opacity: 0 }}
								animate={{ opacity: 1 }}
								className="flex items-center justify-center"
							>
								<ToastGlyph kind={toast.kind} glyph={toast.glyph} />
							</motion.span>
						</AnimatePresence>
					</span>
					<span
						className="flex min-w-0 flex-1 flex-col gap-[5px] transition-opacity duration-quick"
						style={{ opacity: show ? 1 : 0 }}
					>
						{/* Title and description wrap rather than truncate: an error's
						    description is the whole explanation of what went wrong. */}
						<span className="break-words font-medium text-[13px] text-ink-1 leading-[1.3]">
							{toast.title}
						</span>
						{toast.description ? (
							<span className="break-words font-mono text-2xs text-ink-3 leading-[1.3]">
								{toast.description}
							</span>
						) : null}
					</span>
					{show && toast.action ? (
						<ToastMiniButton
							action={toast.action}
							primary={false}
							onClick={() => {
								toast.action?.onClick();
								onDismiss(toast.id);
							}}
						/>
					) : null}
					{show && toast.primaryAction ? (
						<ToastMiniButton
							action={toast.primaryAction}
							primary
							onClick={() => toast.primaryAction?.onClick()}
						/>
					) : null}
					<button
						type="button"
						onClick={() => onDismiss(toast.id)}
						title="Dismiss"
						aria-label="Dismiss"
						className="flex size-6 flex-none items-center justify-center rounded-sm px-1.5 py-px text-[16px] text-ink-4 leading-none transition-[opacity,color] duration-quick hover:text-ink-1"
						style={{ opacity: show ? 1 : 0 }}
					>
						×
					</button>
					{front && toast.kind !== "loading" && toast.duration > 0 ? (
						<span
							key={`${toast.id}-${toast.createdAt}`}
							aria-hidden
							className="absolute inset-x-0 bottom-0 h-0.5 origin-left bg-accent-ink shadow-toast-drain"
							style={{
								animationName: reduce ? undefined : "nc-drain",
								animationDuration: `${toast.duration}ms`,
								animationTimingFunction: "linear",
								animationFillMode: "forwards",
								animationPlayState: hovered ? "paused" : "running",
							}}
						/>
					) : null}
				</div>
			</div>
		</motion.div>
	);
}

export interface AnimatedToastStackProps {
	toasts: AnimatedToast[];
	onDismiss: (id: string, direction?: ToastDirection) => void;
	onPause?: (() => void) | undefined;
	onResume?: (() => void) | undefined;
	/** Distance from the viewport's bottom edge, in px — the canvas minimap
	 * and the approvals Approve bar both raise it. */
	bottom: number;
	portalRoot?: Element | null;
}

/** A fixed, self-sizing column of toasts that expands on hover. */
export function AnimatedToastStack({
	toasts,
	onDismiss,
	onPause,
	onResume,
	bottom,
	portalRoot,
}: AnimatedToastStackProps) {
	const reduce = useReducedMotion();
	const canHover = useHoverCapable();
	const [hovered, setHovered] = useState(false);
	const [heights, setHeights] = useState<Record<string, number>>({});
	const [mounted, setMounted] = useState(false);

	useEffect(() => setMounted(true), []);

	// The stack unmounts under the pointer when its last toast goes, and a
	// removed node gets no mouseleave: without this the store stays paused and
	// hovered, and every later toast would never time out.
	useEffect(() => {
		if (toasts.length > 0) return;
		setHovered(false);
		onResume?.();
	}, [toasts.length, onResume]);

	const setHeight = useCallback((id: string, height: number) => {
		setHeights((prev) =>
			prev[id] === height ? prev : { ...prev, [id]: height },
		);
	}, []);

	if (!mounted || toasts.length === 0) return null;

	const expanded = hovered && canHover;
	const { slots, totalHeight } = toastStackLayout(toasts, heights, expanded);

	const stack = (
		<section
			aria-label="Notifications"
			onMouseEnter={() => {
				if (!canHover) return;
				setHovered(true);
				onPause?.();
			}}
			onMouseLeave={() => {
				setHovered(false);
				onResume?.();
			}}
			className="pointer-events-none fixed right-5 z-[120] w-[min(420px,calc(100vw-40px))]"
			style={{
				bottom,
				height: totalHeight,
				transition: reduce ? undefined : `height 0.5s ${SPRING}`,
			}}
		>
			<AnimatePresence initial={false}>
				{slots.map(
					({ item: toast, y, scale, opacity, forcedHeight }, index) => (
						<ToastCard
							key={toast.id}
							toast={toast}
							index={index}
							front={index === 0}
							hovered={expanded}
							y={y}
							scale={scale}
							opacity={opacity}
							forcedHeight={forcedHeight}
							reduce={!!reduce}
							canDrag={index === 0 || expanded}
							onDismiss={onDismiss}
							onHeight={setHeight}
						/>
					),
				)}
			</AnimatePresence>
		</section>
	);

	return createPortal(stack, portalRoot ?? document.body);
}
