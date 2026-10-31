import { useSyncExternalStore } from "react";
import type { InspectorTab } from "./tab-ids";

/** The narrowest the panel can be dragged, in px. Matches `Sheet`'s own floor. */
export const INSPECTOR_MIN = 360;
/** The docked width of Overview, Deployments and Variables, as a CSS length.
 * Wide enough for all eight tab labels (databases add Backups) at 13px without
 * the strip scrolling, and for the two-column Overview on large screens. */
export const INSPECTOR_DOCKED_CSS = "clamp(640px, 48vw, 920px)";
const DOCKED_FLOOR = 640;
const DOCKED_FRACTION = 0.48;
const DOCKED_CEIL = 920;
const WIDE_FLOOR = 640;
const WIDE_FRACTION = 0.66;
const WIDE_CEIL = 1120;
/** The wide width is `min(66vw, vw − 420px)`, held to 640..1120px (see `wideWidthPx`). */
/**
 * The canvas the wide panel always leaves free, in px: room for the toolbar
 * collapsed to icons, Create, and the zoom stack.
 */
export const MIN_CANVAS_FREE = 420;
/** `Sheet` never grows past this share of the viewport. */
const VIEWPORT_CAP = 0.9;
/**
 * Below this viewport width the panel is a full-screen sheet:
 * the canvas is not meant to show, so it neither resizes nor reserves room.
 */
export const FULL_SCREEN_BELOW = 760;

/** Whether the panel covers the whole screen at this viewport width. */
export function isFullScreen(viewport: number): boolean {
	return viewport < FULL_SCREEN_BELOW;
}

/** A panel width is kept per tab class: the docked tabs share one, the wide tabs another. */
export type WidthClass = "docked" | "wide";

const WIDE_TABS: Partial<Record<InspectorTab, true>> = {
	metrics: true,
	analytics: true,
	logs: true,
	settings: true,
	backups: true,
};

export function widthClassOf(tab: InspectorTab): WidthClass {
	return WIDE_TABS[tab] === true ? "wide" : "docked";
}

/** What `INSPECTOR_DOCKED_CSS` resolves to at this viewport width, in px. */
export function dockedWidthPx(viewport: number): number {
	return Math.min(
		Math.max(DOCKED_FLOOR, Math.min(viewport * DOCKED_FRACTION, DOCKED_CEIL)),
		viewport * VIEWPORT_CAP,
	);
}

/**
 * The wide width at this viewport width, in px: 66vw, but never so wide that
 * less than `MIN_CANVAS_FREE` of canvas is left. `canvasLeft` is where the
 * canvas starts (the sidebar's width), so the room is counted from there.
 */
export function wideWidthPx(viewport: number, canvasLeft = 0): number {
	const room = viewport - canvasLeft - MIN_CANVAS_FREE;
	return Math.min(
		Math.max(WIDE_FLOOR, Math.min(viewport * WIDE_FRACTION, WIDE_CEIL, room)),
		viewport * VIEWPORT_CAP,
	);
}

/** A dragged width held to the range the panel can take at this viewport. */
export function clampInspectorWidth(px: number, viewport: number): number {
	const max = Math.max(INSPECTOR_MIN, viewport * VIEWPORT_CAP);
	return Math.round(Math.min(Math.max(px, INSPECTOR_MIN), max));
}

/**
 * The panel's width in px for a tab class: the width the reader dragged it to,
 * or the class's default.
 */
export function resolveInspectorWidth(
	widthClass: WidthClass,
	saved: number | undefined,
	viewport: number,
	canvasLeft = 0,
): number {
	if (saved !== undefined) return clampInspectorWidth(saved, viewport);
	return Math.round(
		widthClass === "wide"
			? wideWidthPx(viewport, canvasLeft)
			: dockedWidthPx(viewport),
	);
}

// The key is versioned so a change of the default width can retire widths saved
// under an older key.
const STORAGE_PREFIX = "nebulactrl.inspector.width.v2.";

/** The part of `Storage` the width store uses (so tests need no DOM). */
export interface WidthStorage {
	getItem(key: string): string | null;
	setItem(key: string, value: string): void;
}

/** The width the reader last dragged this tab class to, or undefined. */
export function loadSavedWidth(
	storage: WidthStorage | undefined,
	widthClass: WidthClass,
): number | undefined {
	if (!storage) return undefined;
	try {
		const raw = storage.getItem(STORAGE_PREFIX + widthClass);
		if (raw === null) return undefined;
		const value = Number(raw);
		return Number.isFinite(value) && value >= INSPECTOR_MIN
			? Math.round(value)
			: undefined;
	} catch {
		return undefined;
	}
}

export function saveWidth(
	storage: WidthStorage | undefined,
	widthClass: WidthClass,
	px: number,
): void {
	try {
		storage?.setItem(STORAGE_PREFIX + widthClass, String(Math.round(px)));
	} catch {
		// Private mode or a full quota: the width simply is not remembered.
	}
}

// The width the open panel is heading to, in px (0 while closed). The canvas
// reads it to keep the selected card and its dock in view; it is the target,
// not the animated value, so one change is one reading.
let published = 0;
const listeners = new Set<() => void>();

/** Called by the inspector as it opens, resizes, switches class or closes. */
export function publishInspectorWidth(px: number): void {
	if (px === published) return;
	published = px;
	for (const listener of listeners) listener();
}

function subscribe(listener: () => void): () => void {
	listeners.add(listener);
	return () => {
		listeners.delete(listener);
	};
}

/** The inspector's current width in px, or 0 while it is closed. */
export function useInspectorWidthPx(): number {
	return useSyncExternalStore(
		subscribe,
		() => published,
		() => 0,
	);
}
