import type { MapBounds } from "@nebulactrl/dither/backgrounds/geo";
import { cubicBezier } from "motion/react";
import { type RefObject, useEffect, useMemo, useRef } from "react";
import { EASE_OUT } from "#/lib/ease";
import { capturePointer, releasePointer } from "#/lib/touch";
import { type Projection, transformCss } from "../geo/projection";
import {
	clamp,
	clampView,
	isPinchWheel,
	KEY_PAN_SHARE,
	keyAction,
	momentumStep,
	type PointerSample,
	REST_VIEW,
	releaseVelocity,
	resistView,
	STOP_SPEED,
	type Vec,
	type View,
	viewBounds,
	wheelFactor,
	wheelPixels,
	zoomAround,
	zoomRange,
} from "../geo/view";

/** A press moves less than this many pixels and it is still a click. */
const DRAG_THRESHOLD = 4;
/** The map redraws, and the labels are placed again, this long after the last movement. */
const SETTLE_DEBOUNCE_MS = 100;
/** A pull past the edge settles back over this long, ease-out. */
const REBOUND_MS = 200;
/** A frame that took longer than this (a background tab) counts as this. */
const MAX_FRAME_MS = 50;
/** An offset this close to its clamped one (pixels) is rounding, not a pull past the edge. */
const EDGE_EPSILON = 0.01;
const pastEdge = (held: number, raw: number) =>
	Math.abs(held - raw) > EDGE_EPSILON;

const rebound = cubicBezier(...EASE_OUT);

export interface MapViewHost {
	/** The element that takes the wheel, pointer and key events. */
	boxRef: RefObject<HTMLElement | null>;
	/** What the view moves: the shown layer and the hotspot overlay. */
	targets: () => readonly HTMLElement[];
	/** The projection the view is relative to; undefined until the box has a size. */
	projection: Projection | undefined;
	/** Whether gestures are taken at all. */
	enabled: boolean;
	/** Reduced motion: no glide, no rubber band and no rebound. */
	reduced: boolean;
	/** One finger pans on touch only when the map is zoomed in; otherwise the page scrolls. */
	touchPan: boolean;
	/**
	 * A hand took the map. If it was gliding to a new region, stop that glide
	 * at the frame on screen and return the view it was showing.
	 */
	grab: () => View | null;
	/** The view came to rest somewhere: draw the world there. `fade` cross-fades the redraw. */
	commit: (bounds: MapBounds, fade: boolean) => void;
	/** `0`: back to the fitted view. */
	fit: () => void;
	/** The wheel turned over the map without the zoom modifier: the page scrolls, and the hint may show. */
	scrolled: () => void;
}

export interface MapView {
	/** The view on screen now (at rest when nothing is moving). */
	shown: () => View;
	/** Zoom about the box's centre by `factor` (above 1 zooms in): what + and - do. */
	zoomBy: (factor: number) => void;
	/** Back to the fitted view: what 0 does. */
	fit: () => void;
	/**
	 * Stop everything that moves the view and return it to rest, handing back
	 * the view that was on screen. Does not touch the elements.
	 */
	release: () => View;
}

const REST_COPY = (): View => ({ ...REST_VIEW });

/**
 * Wheel, pointer, touch and key control of the map's view. While a hand is
 * on the map the layer and the overlay are moved straight from the input
 * (no transition, no React render per event); `host.commit` is called once
 * the movement has stopped for `SETTLE_DEBOUNCE_MS`. Everything is read from
 * `host` at the moment of the event, so a render never restarts a gesture.
 */
export function useMapView(host: MapViewHost): MapView {
	const latest = useRef(host);
	latest.current = host;

	const state = useRef({
		/** The view as pulled: offsets past the edge are not yet rubber-banded. */
		raw: REST_COPY(),
		/** The view as drawn. */
		shown: REST_COPY(),
		raf: 0,
		timer: 0,
		keyboard: false,
		dragging: false,
		pointers: new Map<
			number,
			{ x: number; y: number; sx: number; sy: number; ignored: boolean }
		>(),
		samples: [] as PointerSample[],
		pinch: null as { distance: number; mid: Vec } | null,
		swallowClick: false,
	});

	const { api, listeners } = useMemo(() => {
		const s = state.current;

		const stopLoop = () => {
			if (s.raf) cancelAnimationFrame(s.raf);
			s.raf = 0;
		};
		const stopTimer = () => {
			if (s.timer) window.clearTimeout(s.timer);
			s.timer = 0;
		};
		const flag = (name: "gesture" | "dragging", on: boolean) => {
			const el = latest.current.boxRef.current;
			if (!el) return;
			if (on) el.dataset[name] = "";
			else delete el.dataset[name];
		};
		const paint = (v: View) => {
			s.shown = v;
			const css = transformCss({
				x: v.x,
				y: v.y,
				scaleX: v.k,
				scaleY: v.k,
			});
			for (const el of latest.current.targets()) el.style.transform = css;
		};
		/** The view at rest in `host.projection`, or null when there is no projection. */
		const clampedRaw = () => {
			const p = latest.current.projection;
			return p ? clampView(s.raw, p) : s.raw;
		};
		const draw = () => {
			const p = latest.current.projection;
			if (!p) return;
			paint(latest.current.reduced ? clampedRaw() : resistView(s.raw, p));
		};

		const commit = () => {
			s.timer = 0;
			// Hands still down: the release schedules it again.
			if (s.pointers.size > 0 && s.dragging) return;
			stopLoop();
			const p = latest.current.projection;
			const v = s.shown;
			s.raw = REST_COPY();
			s.shown = REST_COPY();
			flag("gesture", false);
			flag("dragging", false);
			if (!p || (v.x === 0 && v.y === 0 && v.k === 1)) {
				for (const el of latest.current.targets()) el.style.transform = "";
				return;
			}
			latest.current.commit(
				viewBounds(p, v),
				!latest.current.reduced && !s.keyboard,
			);
		};
		const schedule = () => {
			stopTimer();
			s.timer = window.setTimeout(commit, SETTLE_DEBOUNCE_MS);
		};

		/** A hand takes the map: from here the view is moved by input. */
		const take = () => {
			stopLoop();
			stopTimer();
			if (s.shown.k === 1 && s.shown.x === 0 && s.shown.y === 0) {
				const was = latest.current.grab();
				if (was) {
					s.raw = { ...was };
					s.shown = { ...was };
				}
			}
			flag("gesture", true);
		};

		const local = (clientX: number, clientY: number): Vec | null => {
			const el = latest.current.boxRef.current;
			const p = latest.current.projection;
			if (!el || !p) return null;
			const rect = el.getBoundingClientRect();
			return { x: clientX - rect.left - p.x, y: clientY - rect.top - p.y };
		};

		// ---- momentum and rebound ------------------------------------------

		const glide = (velocity: Vec) => {
			let v = velocity;
			let last = performance.now();
			const tick = (now: number) => {
				const p = latest.current.projection;
				if (!p) return;
				const step = momentumStep(v, Math.min(now - last, MAX_FRAME_MS));
				last = now;
				s.raw = {
					...s.raw,
					x: s.raw.x + step.delta.x,
					y: s.raw.y + step.delta.y,
				};
				const held = clampView(s.raw, p);
				// An edge ends the glide on that axis: it does not bounce.
				v = {
					x: pastEdge(held.x, s.raw.x) ? 0 : step.velocity.x,
					y: pastEdge(held.y, s.raw.y) ? 0 : step.velocity.y,
				};
				s.raw = held;
				paint(held);
				schedule();
				if (Math.hypot(v.x, v.y) < STOP_SPEED) {
					s.raf = 0;
					return;
				}
				s.raf = requestAnimationFrame(tick);
			};
			s.raf = requestAnimationFrame(tick);
		};

		const settleBack = () => {
			const p = latest.current.projection;
			if (!p) return;
			const from = s.shown;
			const to = clampView(s.raw, p);
			const start = performance.now();
			const tick = (now: number) => {
				const t = clamp((now - start) / REBOUND_MS, 0, 1);
				const e = rebound(t);
				paint({
					x: from.x + (to.x - from.x) * e,
					y: from.y + (to.y - from.y) * e,
					k: to.k,
				});
				schedule();
				if (t >= 1) {
					s.raw = to;
					s.raf = 0;
					return;
				}
				s.raf = requestAnimationFrame(tick);
			};
			s.raf = requestAnimationFrame(tick);
		};

		/** A hand lifted: carry on, rebound or rest. */
		const lift = (now: number, cancelled: boolean) => {
			s.dragging = false;
			flag("dragging", false);
			const p = latest.current.projection;
			if (!p) return;
			const held = clampView(s.raw, p);
			const past = pastEdge(held.x, s.raw.x) || pastEdge(held.y, s.raw.y);
			if (latest.current.reduced) {
				s.raw = held;
				schedule();
				return;
			}
			if (past) {
				settleBack();
				return;
			}
			const v = cancelled ? { x: 0, y: 0 } : releaseVelocity(s.samples, now);
			s.samples = [];
			if (Math.hypot(v.x, v.y) >= STOP_SPEED) glide(v);
			else schedule();
		};

		// ---- the events ----------------------------------------------------

		const onWheel = (e: WheelEvent) => {
			const h = latest.current;
			if (!h.enabled || !h.projection) return;
			if (!(e.metaKey || e.ctrlKey)) {
				// No modifier: the page scrolls, untouched.
				h.scrolled();
				return;
			}
			e.preventDefault();
			const el = h.boxRef.current;
			const p = h.projection;
			const at = local(e.clientX, e.clientY);
			if (!el || !at) return;
			s.keyboard = false;
			take();
			s.raw = clampedRaw();
			const factor = wheelFactor(
				wheelPixels(e, el.clientHeight),
				isPinchWheel(e),
			);
			const next = zoomAround(s.raw, factor, at, zoomRange(p.bounds));
			s.raw = clampView(next, p);
			paint(s.raw);
			schedule();
		};

		const onPointerDown = (e: PointerEvent) => {
			const h = latest.current;
			if (!h.enabled || !h.projection) return;
			if (e.pointerType === "mouse" && e.button !== 0) return;
			if (!e.isPrimary && e.pointerType !== "touch") return;
			s.keyboard = false;
			s.pointers.set(e.pointerId, {
				x: e.clientX,
				y: e.clientY,
				sx: e.clientX,
				sy: e.clientY,
				ignored: false,
			});
			const touches = [...s.pointers.values()];
			if (e.pointerType === "touch" && touches.length === 2) {
				// A second finger: pinch. The first finger's drag is over.
				take();
				s.raw = clampedRaw();
				s.dragging = true;
				// The first finger may have been left to the page's scroll.
				for (const q of touches) q.ignored = false;
				const [a, b] = touches;
				if (a && b) {
					s.pinch = {
						distance: Math.hypot(a.x - b.x, a.y - b.y),
						mid: { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 },
					};
				}
				for (const id of s.pointers.keys()) {
					if (h.boxRef.current) capturePointer(h.boxRef.current, id);
				}
				flag("dragging", true);
				return;
			}
			if (e.pointerType === "touch" && touches.length > 2) {
				const own = s.pointers.get(e.pointerId);
				if (own) own.ignored = true;
				return;
			}
			if (e.pointerType === "touch" && !h.touchPan) {
				// Not zoomed in: the page scrolls under this finger.
				const own = s.pointers.get(e.pointerId);
				if (own) own.ignored = true;
				return;
			}
			s.samples = [];
			// Pressing on a map that is still moving stops it where it is, and
			// whatever follows (a drag, or nothing) settles from there.
			if (s.raf) {
				stopLoop();
				stopTimer();
				s.raw = { ...s.shown };
				schedule();
			} else if (s.shown.k === 1 && s.shown.x === 0 && s.shown.y === 0) {
				const was = h.grab();
				if (was) {
					s.raw = { ...was };
					paint(was);
					schedule();
				}
			}
		};

		const onPointerMove = (e: PointerEvent) => {
			const h = latest.current;
			const el = h.boxRef.current;
			const p = h.projection;
			const own = s.pointers.get(e.pointerId);
			if (!own || own.ignored || !el || !p) return;

			if (s.pinch) {
				own.x = e.clientX;
				own.y = e.clientY;
				const pair = [...s.pointers.values()].filter((q) => !q.ignored);
				const [a, b] = pair;
				if (!a || !b) return;
				const distance = Math.hypot(a.x - b.x, a.y - b.y);
				const mid = { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 };
				const at = local(mid.x, mid.y);
				if (!at || s.pinch.distance === 0) return;
				const zoomed = zoomAround(
					s.raw,
					distance / s.pinch.distance,
					at,
					zoomRange(p.bounds),
				);
				s.raw = clampView(
					{
						x: zoomed.x + (mid.x - s.pinch.mid.x),
						y: zoomed.y + (mid.y - s.pinch.mid.y),
						k: zoomed.k,
					},
					p,
				);
				s.pinch = { distance, mid };
				paint(s.raw);
				schedule();
				return;
			}

			const dx = e.clientX - own.x;
			const dy = e.clientY - own.y;
			if (!s.dragging) {
				// A press that has not moved far enough is still a click.
				if (
					Math.hypot(e.clientX - own.sx, e.clientY - own.sy) < DRAG_THRESHOLD
				) {
					return;
				}
				s.dragging = true;
				take();
				s.raw = clampedRaw();
				capturePointer(el, e.pointerId);
				flag("dragging", true);
				// The move that crossed the threshold counts in full.
				own.x = own.sx;
				own.y = own.sy;
				s.samples = [{ x: own.sx, y: own.sy, t: performance.now() }];
				return onPointerMove(e);
			}
			own.x = e.clientX;
			own.y = e.clientY;
			s.raw = { ...s.raw, x: s.raw.x + dx, y: s.raw.y + dy };
			s.samples.push({ x: e.clientX, y: e.clientY, t: performance.now() });
			if (s.samples.length > 12) s.samples.shift();
			draw();
			schedule();
		};

		const onPointerEnd = (e: PointerEvent) => {
			const el = latest.current.boxRef.current;
			const own = s.pointers.get(e.pointerId);
			if (!own) return;
			s.pointers.delete(e.pointerId);
			if (el) releasePointer(el, e.pointerId);
			if (!s.dragging) return;
			// A drag's last lift must not click whatever is under it.
			s.swallowClick = true;
			window.setTimeout(() => {
				s.swallowClick = false;
			}, 0);
			if (s.pinch) {
				if (s.pointers.size >= 2) return;
				s.pinch = null;
				// The finger left on the glass must lift before it pans again.
				for (const q of s.pointers.values()) q.ignored = true;
				s.dragging = false;
				flag("dragging", false);
				schedule();
				return;
			}
			lift(performance.now(), e.type === "pointercancel");
		};

		const onClick = (e: MouseEvent) => {
			if (!s.swallowClick) return;
			e.stopPropagation();
			e.preventDefault();
		};

		/** Back to the fitted view: whatever the hand did is dropped. */
		const fit = () => {
			stopLoop();
			stopTimer();
			s.raw = REST_COPY();
			s.shown = REST_COPY();
			for (const el of latest.current.targets()) el.style.transform = "";
			flag("gesture", false);
			latest.current.fit();
		};

		/** Zoom about the box's centre by `factor` (above 1 zooms in), as a key press or a button does. */
		const zoomBy = (factor: number) => {
			const p = latest.current.projection;
			if (!p) return;
			s.keyboard = true;
			take();
			s.raw = clampedRaw();
			s.raw = clampView(
				zoomAround(
					s.raw,
					factor,
					{ x: p.width / 2, y: p.height / 2 },
					zoomRange(p.bounds),
				),
				p,
			);
			paint(s.raw);
			schedule();
		};

		const onKeyDown = (e: KeyboardEvent) => {
			const h = latest.current;
			const p = h.projection;
			if (!h.enabled || !p || e.metaKey || e.ctrlKey || e.altKey) return;
			const action = keyAction(e.key);
			if (!action) return;
			e.preventDefault();
			if (action.type === "fit") {
				fit();
				return;
			}
			if (action.type === "zoom") {
				zoomBy(action.factor);
				return;
			}
			s.keyboard = true;
			take();
			const at = clampedRaw();
			s.raw = clampView(
				{
					x: at.x + action.x * p.width * KEY_PAN_SHARE,
					y: at.y + action.y * p.height * KEY_PAN_SHARE,
					k: at.k,
				},
				p,
			);
			paint(s.raw);
			schedule();
		};

		const listeners: Listeners = {
			onWheel,
			onPointerDown,
			onPointerMove,
			onPointerEnd,
			onClick,
			onKeyDown,
		};
		const api: MapView = {
			shown: () => s.shown,
			zoomBy,
			fit,
			release: () => {
				stopLoop();
				stopTimer();
				const v = s.shown;
				s.raw = REST_COPY();
				s.shown = REST_COPY();
				s.dragging = false;
				s.pointers.clear();
				s.pinch = null;
				flag("gesture", false);
				flag("dragging", false);
				return v;
			},
		};
		return { api, listeners };
	}, []);

	const { enabled } = host;
	useEffect(() => {
		const el = host.boxRef.current;
		if (!el || !enabled) return;
		const l = listeners;
		// Not passive: a zoom must stop the page from zooming or scrolling.
		el.addEventListener("wheel", l.onWheel, { passive: false });
		el.addEventListener("pointerdown", l.onPointerDown);
		el.addEventListener("pointermove", l.onPointerMove);
		el.addEventListener("pointerup", l.onPointerEnd);
		el.addEventListener("pointercancel", l.onPointerEnd);
		el.addEventListener("click", l.onClick, true);
		el.addEventListener("keydown", l.onKeyDown);
		return () => {
			el.removeEventListener("wheel", l.onWheel);
			el.removeEventListener("pointerdown", l.onPointerDown);
			el.removeEventListener("pointermove", l.onPointerMove);
			el.removeEventListener("pointerup", l.onPointerEnd);
			el.removeEventListener("pointercancel", l.onPointerEnd);
			el.removeEventListener("click", l.onClick, true);
			el.removeEventListener("keydown", l.onKeyDown);
			api.release();
		};
	}, [api, listeners, enabled, host.boxRef]);

	return api;
}

interface Listeners {
	onWheel: (e: WheelEvent) => void;
	onPointerDown: (e: PointerEvent) => void;
	onPointerMove: (e: PointerEvent) => void;
	onPointerEnd: (e: PointerEvent) => void;
	onClick: (e: MouseEvent) => void;
	onKeyDown: (e: KeyboardEvent) => void;
}
