/**
 * Clear boxes: keep a field off the text that sits on it. A box is measured
 * from the DOM (an element, padded) in CSS pixels relative to the host's
 * top-left; the field clears a rounded box with a feathered edge, so the
 * clearing is right at every width and DPR. Several boxes multiply.
 *
 * `useClearBox` measures. `clearBoxUniforms` packs boxes into the uniforms the
 * `GLSL_CLEAR` helper (`backgrounds/shared`) reads. Engine uniforms cannot
 * start with `u_` or be arrays, so the uniforms are plain names: four
 * `clearRect<i>` vec4s (x, y, w, h), two `clearShape` vec4s (radius, feather
 * for boxes 0-1 and 2-3) and `clearN`, the box count.
 */
import { type RefObject, useEffect, useState } from "react";
import type { GridLayout } from "./layout";
import type { UniformRecord } from "./uniforms";

/** Boxes a field clears at once; extra boxes are ignored. */
export const MAX_CLEAR_BOXES = 4;

/** A rounded, feathered clearing. CSS px, relative to the host's top-left. */
export interface ClearBox {
	x: number;
	y: number;
	w: number;
	h: number;
	/** Corner radius of the box. */
	radius: number;
	/** Width of the soft edge, outside the box. 0 is a hard edge. */
	feather: number;
}

export interface ClearBoxOptions {
	/** Space added round each measured element, px. Default 16. */
	pad?: number | undefined;
	/** Corner radius of each box, px. Default 12. */
	radius?: number | undefined;
	/** Feather outside each box, px. Default 24. */
	feather?: number | undefined;
}

const DEFAULTS = { pad: 16, radius: 12, feather: 24 } as const;

/** The edges of a rectangle; what `getBoundingClientRect` returns. */
export interface RectLike {
	left: number;
	top: number;
	right: number;
	bottom: number;
}

/**
 * The box that clears `rect` inside `host`: padded, and rounded outward to
 * whole pixels so it never covers less than the element.
 */
export function clearBoxFromRect(
	rect: RectLike,
	host: RectLike,
	{ pad, radius, feather }: { pad: number; radius: number; feather: number },
): ClearBox {
	const left = Math.floor(rect.left - host.left - pad);
	const top = Math.floor(rect.top - host.top - pad);
	const right = Math.ceil(rect.right - host.left + pad);
	const bottom = Math.ceil(rect.bottom - host.top + pad);
	return {
		x: left,
		y: top,
		w: right - left,
		h: bottom - top,
		radius: Math.max(0, radius),
		feather: Math.max(0, feather),
	};
}

export function sameClearBoxes(
	a: readonly ClearBox[],
	b: readonly ClearBox[],
): boolean {
	if (a.length !== b.length) return false;
	for (let i = 0; i < a.length; i++) {
		const p = a[i];
		const q = b[i];
		if (
			!p ||
			!q ||
			p.x !== q.x ||
			p.y !== q.y ||
			p.w !== q.w ||
			p.h !== q.h ||
			p.radius !== q.radius ||
			p.feather !== q.feather
		)
			return false;
	}
	return true;
}

/**
 * Boxes as uniforms, in units of `layout.cellCss` CSS px with y from the top
 * (a cell layout gives cells; `{ cellCss: 1 }` gives CSS px). At most
 * `MAX_CLEAR_BOXES` are packed; the record always has the same keys, so a
 * change of box count never recompiles the shader.
 */
export function clearBoxUniforms(
	boxes: readonly ClearBox[],
	layout: Pick<GridLayout, "cellCss">,
): UniformRecord {
	const unit = layout.cellCss > 0 ? layout.cellCss : 1;
	const n = Math.min(boxes.length, MAX_CLEAR_BOXES);
	const rect = (i: number): [number, number, number, number] => {
		const box = boxes[i];
		return i < n && box
			? [
					box.x / unit,
					box.y / unit,
					Math.max(0, box.w) / unit,
					Math.max(0, box.h) / unit,
				]
			: [0, 0, 0, 0];
	};
	const shape = (i: number): [number, number] => {
		const box = boxes[i];
		return i < n && box ? [box.radius / unit, box.feather / unit] : [0, 0];
	};
	const [r0, f0] = shape(0);
	const [r1, f1] = shape(1);
	const [r2, f2] = shape(2);
	const [r3, f3] = shape(3);
	return {
		clearRect0: rect(0),
		clearRect1: rect(1),
		clearRect2: rect(2),
		clearRect3: rect(3),
		clearShape01: [r0, f0, r1, f1],
		clearShape23: [r2, f2, r3, f3],
		clearN: n,
	};
}

const EMPTY: readonly ClearBox[] = [];
const SEPARATOR = "\u0000";

/**
 * Measure `selectors` (each matched with `host.querySelector`) into clear
 * boxes relative to the host's top-left, so the host should be the field's
 * box or fill the same area. Re-measures on resize of the host or of a target,
 * on window resize, when fonts load and when an animation or transition in
 * the host ends (a transform moves a box without resizing it). The array keeps
 * its identity while no box changes; a selector that matches nothing is
 * skipped; at most `MAX_CLEAR_BOXES` are returned.
 */
export function useClearBox(
	host: RefObject<HTMLElement | null>,
	selectors: readonly string[],
	options: ClearBoxOptions = {},
): readonly ClearBox[] {
	const pad = options.pad ?? DEFAULTS.pad;
	const radius = options.radius ?? DEFAULTS.radius;
	const feather = options.feather ?? DEFAULTS.feather;
	const key = selectors.join(SEPARATOR);
	const [boxes, setBoxes] = useState<readonly ClearBox[]>(EMPTY);

	useEffect(() => {
		const root = host.current;
		if (!root) return;
		const list = (key ? key.split(SEPARATOR) : []).slice(0, MAX_CLEAR_BOXES);
		const shape = { pad, radius, feather };
		const observer =
			typeof ResizeObserver === "undefined"
				? null
				: new ResizeObserver(() => measure());
		observer?.observe(root);
		let frame = 0;
		let live = true;

		function measure() {
			if (!root) return;
			const hostRect = root.getBoundingClientRect();
			const next: ClearBox[] = [];
			for (const selector of list) {
				const el = root.querySelector(selector);
				if (!el) continue;
				observer?.observe(el);
				const rect = el.getBoundingClientRect();
				if (rect.width === 0 && rect.height === 0) continue;
				next.push(clearBoxFromRect(rect, hostRect, shape));
			}
			const result = next.length === 0 ? EMPTY : next;
			setBoxes((prev) => (sameClearBoxes(prev, result) ? prev : result));
		}
		const schedule = () => {
			if (frame) return;
			frame = requestAnimationFrame(() => {
				frame = 0;
				measure();
			});
		};

		measure();
		window.addEventListener("resize", schedule);
		root.addEventListener("animationend", schedule);
		root.addEventListener("transitionend", schedule);
		document.fonts?.ready.then(() => {
			if (live) measure();
		});
		return () => {
			live = false;
			observer?.disconnect();
			window.removeEventListener("resize", schedule);
			root.removeEventListener("animationend", schedule);
			root.removeEventListener("transitionend", schedule);
			if (frame) cancelAnimationFrame(frame);
		};
	}, [host, key, pad, radius, feather]);

	return boxes;
}
