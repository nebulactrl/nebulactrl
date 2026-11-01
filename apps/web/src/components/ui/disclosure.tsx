import { durations } from "@nebulactrl/tokens";
import { cn } from "cn";
import { ChevronDownIcon } from "lucide-react";
import { type ReactNode, useEffect, useState } from "react";

/**
 * The accordion transition (transitions.dev 21, installed in transitions.css)
 * for a disclosure whose header we already have: a button with
 * `aria-expanded`, and a body that grows and shrinks in height. Height is a
 * grid track (`0fr` to `1fr`), so content of any size animates without
 * measuring. The header is the caller's; `DisclosureChevron` is the "v" that
 * flips to a "^".
 *
 * The motion is never the only cue: the chevron and `aria-expanded` carry the
 * state, and a reader who prefers reduced motion gets the body with a plain
 * cross-fade (no height tween, no blur).
 */

/** The collapse clock, read from the CSS so the unmount follows the tween. */
function collapseMs(): number {
	if (typeof document === "undefined") return durations.fast;
	const raw = getComputedStyle(document.documentElement)
		.getPropertyValue("--acc-collapse")
		.trim();
	const ms = Number.parseFloat(raw);
	if (Number.isNaN(ms)) return durations.fast;
	return raw.endsWith("ms") ? ms : ms * 1000;
}

export interface DisclosurePanelProps {
	open: boolean;
	/** The id the header's `aria-controls` points at. */
	id?: string;
	/** The id of the header button, which names the region. */
	labelledBy?: string;
	/**
	 * Keep the body mounted after it closes. By default it unmounts once the
	 * collapse has played, so a closed row stops its queries and log streams.
	 */
	keepMounted?: boolean;
	className?: string;
	/** Classes for the clipping inner element (padding goes here, never on the panel). */
	innerClassName?: string;
	children: ReactNode;
}

/**
 * The collapsible body. Mounts on open, plays `0fr -> 1fr` with a cross-blur,
 * and unmounts after `0fr` has been reached. A body that is open on first
 * render does not animate in. Closed, it is `inert` and hidden from assistive
 * tech, so neither focus nor a screen reader lands inside what is not shown.
 */
export function DisclosurePanel({
	open,
	id,
	labelledBy,
	keepMounted = false,
	className,
	innerClassName,
	children,
}: DisclosurePanelProps) {
	const [mounted, setMounted] = useState(open);
	// `shown` is the `data-open` the CSS tweens to: false for one frame after
	// mounting, so the first paint is the collapsed track.
	const [shown, setShown] = useState(open);

	useEffect(() => {
		if (open) {
			setMounted(true);
			let second = 0;
			const first = requestAnimationFrame(() => {
				second = requestAnimationFrame(() => setShown(true));
			});
			return () => {
				cancelAnimationFrame(first);
				cancelAnimationFrame(second);
			};
		}
		setShown(false);
		if (keepMounted) return;
		const timer = window.setTimeout(() => setMounted(false), collapseMs() + 40);
		return () => window.clearTimeout(timer);
	}, [open, keepMounted]);

	return (
		<div
			id={id}
			{...(labelledBy ? { role: "region", "aria-labelledby": labelledBy } : {})}
			aria-hidden={open ? undefined : true}
			inert={!open}
			data-open={shown}
			className={cn("t-acc-panel", className)}
		>
			<div className={cn("t-acc-panel-inner", innerClassName)}>
				{mounted || keepMounted ? children : null}
			</div>
		</div>
	);
}

/**
 * The header chevron: a "v" that flips to a "^" through a flat line. The
 * glyph is symmetric about its box centre, which is what makes the flip land.
 * `className` sizes it (`size-3.5`) and colours it (`text-ink-3`).
 */
export function DisclosureChevron({
	open,
	className,
}: {
	open: boolean;
	className?: string;
}) {
	return (
		<span
			aria-hidden
			data-open={open}
			className={cn("t-acc-chevron shrink-0", className)}
		>
			<ChevronDownIcon className="size-full" />
		</span>
	);
}
