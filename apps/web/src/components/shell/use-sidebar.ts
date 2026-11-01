import { useCallback, useEffect, useSyncExternalStore } from "react";
import { lastInputWasKeyboard } from "#/lib/input-modality";

const STORAGE_KEY = "nebulactrl:sidebar";

/** Below this width the sidebar is an off-canvas drawer behind a menu button. */
const NARROW_QUERY = "(max-width: 759px)";

interface SidebarState {
	/** The desktop rail: persisted, ignored while the viewport is narrow. */
	collapsed: boolean;
	/**
	 * Whether the last change should glide. A toggle from the keyboard is
	 * instant: the reader is moving by key and the motion only makes them wait.
	 */
	animate: boolean;
	/** The narrow drawer is open. Never persisted: it always starts shut. */
	drawerOpen: boolean;
}

function readStored(): boolean {
	try {
		return window.localStorage.getItem(STORAGE_KEY) === "collapsed";
	} catch {
		return false;
	}
}

// One store for the whole shell, so the sidebar, the ⌘B listener, the top
// bar's menu button and any future caller agree without a provider.
let state: SidebarState = {
	collapsed: typeof window === "undefined" ? false : readStored(),
	animate: false,
	drawerOpen: false,
};
const listeners = new Set<() => void>();

function emit(next: SidebarState) {
	state = next;
	for (const listener of listeners) listener();
}

function setCollapsed(collapsed: boolean, animate: boolean) {
	if (state.collapsed === collapsed && state.animate === animate) return;
	try {
		window.localStorage.setItem(
			STORAGE_KEY,
			collapsed ? "collapsed" : "expanded",
		);
	} catch {
		// Private mode or a full quota: the choice lasts for this visit.
	}
	emit({ ...state, collapsed, animate });
}

function setDrawer(drawerOpen: boolean) {
	if (state.drawerOpen === drawerOpen) return;
	// Opened or closed by key: no slide. A pointer or touch gets the glide.
	emit({ ...state, drawerOpen, animate: !lastInputWasKeyboard() });
}

function subscribe(listener: () => void): () => void {
	listeners.add(listener);
	return () => listeners.delete(listener);
}

function subscribeNarrow(listener: () => void): () => void {
	const query = window.matchMedia(NARROW_QUERY);
	query.addEventListener("change", listener);
	return () => query.removeEventListener("change", listener);
}

/** Whether the viewport is under 760px: the sidebar is a drawer there. */
function useNarrow(): boolean {
	return useSyncExternalStore(
		subscribeNarrow,
		() => window.matchMedia(NARROW_QUERY).matches,
		() => false,
	);
}

function isEditable(target: EventTarget | null): boolean {
	return (
		target instanceof HTMLElement &&
		(target.isContentEditable ||
			target instanceof HTMLInputElement ||
			target instanceof HTMLTextAreaElement ||
			target instanceof HTMLSelectElement)
	);
}

export interface Sidebar {
	/**
	 * The icon rail is showing. Always false while narrow: the drawer holds
	 * the full sidebar and the stored rail choice is left alone.
	 */
	collapsed: boolean;
	/** Width and drawer changes glide; false right after a keyboard action. */
	animate: boolean;
	/** The viewport is under 760px: the sidebar is an off-canvas drawer. */
	narrow: boolean;
	drawerOpen: boolean;
	openDrawer(): void;
	closeDrawer(): void;
	/** `viaKeyboard` makes the change instant. Narrow: opens or closes the drawer. */
	toggle(viaKeyboard?: boolean): void;
}

/**
 * One slice of the store. The snapshot is a primitive, so a component
 * re-renders only when its own slice changes: the top bar does not render
 * when the rail collapses, and the shell frame does not render at all.
 */
function useSlice<T extends boolean>(select: (current: SidebarState) => T): T {
	return useSyncExternalStore(
		subscribe,
		() => select(state),
		() => select(state),
	);
}

/** Whether the last change should glide. Read at commit time by the rail's motion. */
export function sidebarAnimates(): boolean {
	return state.animate;
}

/** The narrow drawer: the top bar's menu button and the frame that goes inert behind it. */
export function useSidebarDrawer() {
	const narrow = useNarrow();
	const open = useSlice((current) => current.drawerOpen);
	const openDrawer = useCallback(() => setDrawer(true), []);
	const closeDrawer = useCallback(() => setDrawer(false), []);
	return { narrow, drawerOpen: narrow && open, openDrawer, closeDrawer };
}

/** The sidebar's collapsed state (desktop) and drawer state (narrow). */
export function useSidebar(): Sidebar {
	const collapsed = useSlice((current) => current.collapsed);
	const animate = useSlice((current) => current.animate);
	const narrow = useNarrow();
	const { drawerOpen, openDrawer, closeDrawer } = useSidebarDrawer();
	const toggle = useCallback(
		(viaKeyboard = false) => {
			if (narrow) setDrawer(!state.drawerOpen);
			else setCollapsed(!state.collapsed, !viaKeyboard);
		},
		[narrow],
	);
	return {
		collapsed: narrow ? false : collapsed,
		animate,
		narrow,
		drawerOpen,
		openDrawer,
		closeDrawer,
		toggle,
	};
}

/** ⌘B / Ctrl+B toggles the sidebar (the drawer, when narrow) from anywhere but a text field. */
export function useSidebarShortcut() {
	useEffect(() => {
		const onKeyDown = (event: KeyboardEvent) => {
			if (event.key.toLowerCase() !== "b") return;
			if (!(event.metaKey || event.ctrlKey) || event.altKey || event.shiftKey)
				return;
			if (isEditable(event.target)) return;
			event.preventDefault();
			if (window.matchMedia(NARROW_QUERY).matches) setDrawer(!state.drawerOpen);
			else setCollapsed(!state.collapsed, false);
		};
		window.addEventListener("keydown", onKeyDown);
		return () => window.removeEventListener("keydown", onKeyDown);
	}, []);
}
