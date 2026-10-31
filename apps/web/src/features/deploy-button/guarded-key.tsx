import { cn } from "cn";
import { LockIcon } from "lucide-react";
import {
	type ReactNode,
	useCallback,
	useEffect,
	useId,
	useReducer,
	useRef,
} from "react";
import { RocketIcon } from "#/components/icons/animated/rocket-icon";
import { StatusIcon } from "#/components/ui/status-icon";
import { lastInputWasKeyboard } from "#/lib/input-modality";
import { prefersReducedMotion } from "#/lib/transitions";
import { canPressKey, GUARD_CLOSED, guardReducer } from "./guard-state";

/** How long a lifted guard stays up before it drops back on its own. */
export const GUARD_ARMED_MS = 4000;

export interface Guard {
	/** The cover is lifted: the key beneath can be pressed. */
	armed: boolean;
	/** The key was pressed and its request has not resolved. */
	sent: boolean;
	lift(): void;
	/** The cover is lowered by hand, outside press, focus loss or Escape. */
	lower(reason?: "lower" | "outside" | "escape"): void;
	/** The key was pressed: the cover closes at once. */
	press(): void;
	/** The request the press started has resolved. */
	resolved(): void;
}

/**
 * The state of one guard (see `guardReducer`). A lifted cover drops by
 * itself after `armedMs`, so the key is never one click from production for
 * longer than a moment.
 */
export function useGuard(armedMs: number = GUARD_ARMED_MS): Guard {
	const [state, dispatch] = useReducer(guardReducer, GUARD_CLOSED);
	useEffect(() => {
		if (!state.armed) return;
		const timer = window.setTimeout(() => dispatch("timeout"), armedMs);
		return () => window.clearTimeout(timer);
	}, [state.armed, armedMs]);
	return {
		armed: state.armed,
		sent: state.sent,
		lift: useCallback(() => dispatch("lift"), []),
		lower: useCallback(
			(reason: "lower" | "outside" | "escape" = "lower") => dispatch(reason),
			[],
		),
		press: useCallback(() => dispatch("press"), []),
		resolved: useCallback(() => dispatch("resolved"), []),
	};
}

export interface GuardedKeyProps {
	guard: Guard;
	/** The key's label: "Deploy · 2". */
	label: ReactNode;
	/**
	 * The release is on its way, beyond the request itself (a deployment of
	 * the environment is still running). The closed guard reports it.
	 */
	sent: boolean;
	/** Nothing to deploy; the guard stays shut. */
	disabled?: boolean;
	disabledReason?: string;
	/**
	 * Plays the rocket in the status column once: the deploy was just sent.
	 * The cover snaps shut over the key on the press, so the cue sits beside
	 * it, where the in-flight state shows.
	 */
	launching?: boolean | undefined;
	onPress(): void;
	/** Lays out the whole key; on a phone give it the width of its row. */
	className?: string;
}

/**
 * The production Deploy key: a key under a hinged hatched cover that always
 * reads PRODUCTION. Click, or press Enter/Space on the cover, to lift it;
 * the key then behaves like a primary button. The cover drops again when the
 * key is pressed, on a pointer press or focus move outside the key and
 * cover, on Escape, or after 4s. While a release is in flight the closed
 * cover has a spinning status icon beside it reading "Deploying". Reduced motion,
 * and a lift or lowering made from the keyboard, skip the hinge.
 */
export function GuardedKey({
	guard,
	label,
	sent,
	disabled = false,
	disabledReason,
	launching = false,
	onPress,
	className,
}: GuardedKeyProps) {
	const keyId = useId();
	const housingRef = useRef<HTMLDivElement>(null);
	const keyRef = useRef<HTMLButtonElement>(null);
	const coverRef = useRef<HTMLButtonElement>(null);
	const raised = guard.armed;
	const sending = guard.sent || sent;
	const { lower } = guard;

	// A pointer press anywhere but the key and its cover lowers the guard.
	// Attached only while the cover is lifted.
	useEffect(() => {
		if (!raised) return;
		const onPointerDown = (event: PointerEvent) => {
			if (
				event.target instanceof Node &&
				housingRef.current?.contains(event.target)
			) {
				return;
			}
			lower("outside");
		};
		document.addEventListener("pointerdown", onPointerDown, true);
		return () =>
			document.removeEventListener("pointerdown", onPointerDown, true);
	}, [raised, lower]);

	// Focus leaving the key and its cover lowers the guard.
	useEffect(() => {
		const housing = housingRef.current;
		if (!raised || !housing) return;
		const onFocusOut = (event: FocusEvent) => {
			if (!housing.contains(event.relatedTarget as Node | null)) {
				lower("outside");
			}
		};
		housing.addEventListener("focusout", onFocusOut);
		return () => housing.removeEventListener("focusout", onFocusOut);
	}, [raised, lower]);

	// When the guard drops under focus (a press, Escape, the timeout) the
	// focus returns to the cover rather than falling to the page.
	const wasRaised = useRef(raised);
	useEffect(() => {
		if (wasRaised.current && !raised) {
			const active = document.activeElement;
			if (
				active === null ||
				active === document.body ||
				keyRef.current?.contains(active)
			) {
				coverRef.current?.focus();
			}
		}
		wasRaised.current = raised;
	}, [raised]);

	const instant = prefersReducedMotion() || lastInputWasKeyboard();
	const coverLocked = disabled || sending;

	return (
		<fieldset
			aria-label="Production deploy"
			className={cn(
				"m-0 flex min-w-0 items-center gap-2.5 border-0 p-0",
				className,
			)}
			onKeyDown={(event) => {
				if (event.key !== "Escape" || !guard.armed) return;
				// Lowering the guard is all Escape does; a sheet around the key
				// must not also close.
				event.stopPropagation();
				guard.lower("escape");
			}}
		>
			<div
				role="status"
				className={cn(
					"flex w-[76px] flex-col items-center gap-1 font-medium font-mono text-[10px] text-ink-3 leading-none transition-opacity duration-quick",
					sending ? "opacity-100" : "opacity-0",
				)}
			>
				<RocketIcon animate={launching} className="size-3.5" />
				<span aria-hidden className="flex items-center gap-1">
					<StatusIcon kind="deploying" label="" />
					Deploying
				</span>
				<span className="sr-only">{sending ? "Deploying" : ""}</span>
			</div>
			<div
				ref={housingRef}
				className={cn(
					"relative h-10 rounded-xl border border-line-control bg-surface-well p-[3px] shadow-[inset_0_2px_5px_rgb(2_4_10/.85),inset_0_0_0_1px_rgb(2_4_10/.4),0_1px_0_rgb(255_255_255/.07)]",
					"w-[148px] max-[759px]:w-auto max-[759px]:min-w-[148px] max-[759px]:flex-1",
				)}
			>
				<button
					ref={keyRef}
					id={keyId}
					type="button"
					// Behind a closed guard the key can be neither clicked nor tabbed to.
					inert={!raised}
					disabled={disabled}
					data-busy={sending || undefined}
					onClick={() => {
						// A scripted click reaches an inert key too: the guard decides.
						if (!canPressKey({ armed: raised, sent: sending }, disabled))
							return;
						guard.press();
						onPress();
					}}
					className="btn-primary pressable flex size-full cursor-pointer select-none items-center justify-center whitespace-nowrap rounded-[9px] font-medium text-[13px] leading-none outline-none focus-visible:outline-2 focus-visible:outline-accent-ink focus-visible:outline-offset-2 disabled:cursor-default"
				>
					{label}
				</button>
				<button
					ref={coverRef}
					type="button"
					aria-expanded={raised}
					aria-controls={keyId}
					aria-label={
						raised ? "Lower production guard" : "Lift production guard"
					}
					aria-disabled={coverLocked || undefined}
					title={disabled ? (disabledReason ?? "Nothing to deploy") : undefined}
					onClick={(event) => {
						if (raised) {
							guard.lower("lower");
							return;
						}
						if (coverLocked) return;
						guard.lift();
						// A lift made from the keyboard lands on the key, ready to press.
						if (event.detail === 0) {
							requestAnimationFrame(() => keyRef.current?.focus());
						}
					}}
					style={{
						transformOrigin: "50% 0",
						transform: raised
							? "perspective(420px) translateY(-7px) rotateX(-62deg)"
							: "none",
						transitionDuration: instant ? "0ms" : "180ms",
					}}
					className={cn(
						"absolute -inset-px flex cursor-pointer items-center justify-center gap-2 rounded-xl border border-[oklch(0.52_0.13_22)] bg-[repeating-linear-gradient(135deg,oklch(0.36_0.075_22)_0_7px,oklch(0.3_0.06_22)_7px_14px)] font-mono font-semibold text-[10.5px] text-[oklch(0.9_0.07_22)] uppercase leading-none tracking-[0.16em] shadow-[inset_0_1px_0_rgb(255_255_255/.22),inset_0_-2px_0_rgb(2_4_10/.4),0_6px_14px_-4px_rgb(2_4_10/.7)] outline-none [text-shadow:0_1px_0_rgb(2_4_10/.5)] [transition-property:transform,scale,opacity] ease-smooth-out focus-visible:outline-2 focus-visible:outline-accent-ink focus-visible:outline-offset-2",
						// A lifted cover is rotated over the key's top edge: a pointer
						// passes through to the key; the keyboard can still lower it.
						raised
							? "pointer-events-none opacity-95"
							: !coverLocked && "active:scale-(--press-scale)",
						coverLocked && !raised && "cursor-default",
						disabled && "cursor-not-allowed brightness-[.8] saturate-[.55]",
					)}
				>
					<LockIcon aria-hidden className="size-3" />
					Production
					<i
						aria-hidden
						className="absolute bottom-[-6px] left-1/2 h-2 w-[34px] -translate-x-1/2 rounded-b-md bg-[oklch(0.52_0.13_22)] shadow-[inset_0_1px_0_rgb(255_255_255/.3)]"
					/>
				</button>
			</div>
		</fieldset>
	);
}
