import { createAscii, readPalette } from "@nebulactrl/ascii";
import { cn } from "cn";
import { type RefObject, useEffect, useRef } from "react";

/**
 * The pixel-dithered "Eclipse" (a ringed planet) behind the auth card.
 * Decorative: `aria-hidden`, no pointer events, fixed behind everything.
 * Mounts the framework-free engine in an effect, so nothing re-renders per
 * frame.
 *
 * - `cardRef` is the panel: the field is dimmed and feathered around it and
 *   the planet takes the free side of the screen.
 * - `quietRef` is text drawn straight over the field (the wordmark): cleared
 *   to the page background.
 * - Without WebGL2 (or after an unrecoverable context loss) `active` stays
 *   false, the host stays transparent and the parent keeps its halftone floor.
 */
export function AsciiBackdrop({
	cardRef,
	quietRef,
	active,
	onActiveChange,
	className,
}: {
	cardRef: RefObject<HTMLElement | null>;
	quietRef?: RefObject<HTMLElement | null> | undefined;
	active: boolean;
	onActiveChange: (active: boolean) => void;
	className?: string;
}) {
	const hostRef = useRef<HTMLDivElement>(null);

	useEffect(() => {
		const host = hostRef.current;
		if (!host) return;
		// A fresh canvas per effect run: destroy() releases the GL context, which
		// cannot be revived on the same element (StrictMode mounts twice).
		const canvas = document.createElement("canvas");
		canvas.className = "block size-full";
		host.prepend(canvas);
		const engine = createAscii(canvas, {
			palette: readPalette(host),
			maskRect: () => cardRef.current?.getBoundingClientRect() ?? null,
			quietRects: () => {
				const el = quietRef?.current;
				return el ? [el.getBoundingClientRect()] : [];
			},
			onStatus: (status) => onActiveChange(status === "ready"),
		});
		if (!engine) {
			canvas.remove();
			return;
		}
		engine.start();
		// The card grows with errors and field notes: keep the mask on it.
		const watch =
			typeof ResizeObserver === "function"
				? new ResizeObserver(() => engine.resize())
				: null;
		if (cardRef.current) watch?.observe(cardRef.current);
		if (quietRef?.current) watch?.observe(quietRef.current);
		return () => {
			watch?.disconnect();
			engine.destroy();
			canvas.remove();
			onActiveChange(false);
		};
	}, [cardRef, quietRef, onActiveChange]);

	return (
		<div
			ref={hostRef}
			aria-hidden="true"
			data-slot="ascii-backdrop"
			data-active={active ? "" : undefined}
			className={cn(
				"pointer-events-none fixed inset-0 z-[-1] opacity-0 data-active:opacity-100",
				"motion-safe:transition-opacity motion-safe:duration-very-slow motion-safe:ease-smooth-out",
				className,
			)}
		>
			{/* Edge vignette over the field. */}
			<div className="absolute inset-0 bg-[radial-gradient(ellipse_80%_75%_at_50%_48%,transparent_38%,rgb(10_11_14/0.62)_100%)]" />
		</div>
	);
}
