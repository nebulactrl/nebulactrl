import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { act, fireEvent, renderHook } from "@testing-library/react";
import { stubMedia } from "./shell-test-kit";
import type * as SidebarModule from "./use-sidebar";

const NARROW = "(max-width: 759px)";
const KEY = "nebulactrl:sidebar";

let fresh = 0;
/** A new copy of the module: the sidebar's store lives in module state, read from storage at load. */
async function load(): Promise<typeof SidebarModule> {
	return (await import(
		`./use-sidebar.ts?case=${fresh++}`
	)) as typeof SidebarModule;
}

let media: ReturnType<typeof stubMedia>;
beforeEach(() => {
	window.localStorage.clear();
	media = stubMedia();
});
afterEach(() => {
	media.restore();
	window.localStorage.clear();
});

describe("useSidebar on a wide screen", () => {
	test("starts expanded with nothing stored", async () => {
		const { useSidebar } = await load();
		const { result } = renderHook(() => useSidebar());
		expect(result.current).toMatchObject({
			collapsed: false,
			narrow: false,
			drawerOpen: false,
			animate: false,
		});
	});

	test("starts collapsed when the stored choice says so", async () => {
		window.localStorage.setItem(KEY, "collapsed");
		const { useSidebar } = await load();
		const { result } = renderHook(() => useSidebar());
		expect(result.current.collapsed).toBe(true);
	});

	test("toggling collapses, animates, and remembers the choice", async () => {
		const { useSidebar } = await load();
		const { result } = renderHook(() => useSidebar());
		act(() => result.current.toggle());
		expect(result.current.collapsed).toBe(true);
		expect(result.current.animate).toBe(true);
		expect(window.localStorage.getItem(KEY)).toBe("collapsed");
		act(() => result.current.toggle());
		expect(result.current.collapsed).toBe(false);
		expect(window.localStorage.getItem(KEY)).toBe("expanded");
	});

	test("a keyboard toggle is instant", async () => {
		const { useSidebar, sidebarAnimates } = await load();
		const { result } = renderHook(() => useSidebar());
		act(() => result.current.toggle(true));
		expect(result.current.collapsed).toBe(true);
		expect(result.current.animate).toBe(false);
		expect(sidebarAnimates()).toBe(false);
		act(() => result.current.toggle());
		expect(sidebarAnimates()).toBe(true);
	});

	test("a new page load reads the remembered choice back", async () => {
		const first = await load();
		const view = renderHook(() => first.useSidebar());
		act(() => view.result.current.toggle());
		view.unmount();
		const second = await load();
		const { result } = renderHook(() => second.useSidebar());
		expect(result.current.collapsed).toBe(true);
	});

	test("two components share the one state", async () => {
		const { useSidebar } = await load();
		const a = renderHook(() => useSidebar());
		const b = renderHook(() => useSidebar());
		act(() => a.result.current.toggle());
		expect(b.result.current.collapsed).toBe(true);
	});

	test("a storage that throws leaves the choice for this visit only", async () => {
		const set = Storage.prototype.setItem;
		Storage.prototype.setItem = () => {
			throw new Error("quota");
		};
		try {
			const { useSidebar } = await load();
			const { result } = renderHook(() => useSidebar());
			act(() => result.current.toggle());
			expect(result.current.collapsed).toBe(true);
		} finally {
			Storage.prototype.setItem = set;
		}
	});

	test("unreadable storage means expanded", async () => {
		const get = Storage.prototype.getItem;
		Storage.prototype.getItem = () => {
			throw new Error("denied");
		};
		try {
			const { useSidebar } = await load();
			const { result } = renderHook(() => useSidebar());
			expect(result.current.collapsed).toBe(false);
		} finally {
			Storage.prototype.getItem = get;
		}
	});
});

describe("useSidebar on a narrow screen", () => {
	test("is a drawer: never collapsed, shut at first", async () => {
		window.localStorage.setItem(KEY, "collapsed");
		media.set(NARROW, true);
		const { useSidebar } = await load();
		const { result } = renderHook(() => useSidebar());
		expect(result.current).toMatchObject({
			narrow: true,
			collapsed: false,
			drawerOpen: false,
		});
	});

	test("toggle opens and shuts the drawer and leaves the stored rail choice alone", async () => {
		media.set(NARROW, true);
		const { useSidebar } = await load();
		const { result } = renderHook(() => useSidebar());
		act(() => result.current.toggle());
		expect(result.current.drawerOpen).toBe(true);
		expect(window.localStorage.getItem(KEY)).toBeNull();
		act(() => result.current.toggle());
		expect(result.current.drawerOpen).toBe(false);
	});

	test("openDrawer and closeDrawer are explicit", async () => {
		media.set(NARROW, true);
		const { useSidebar } = await load();
		const { result } = renderHook(() => useSidebar());
		act(() => result.current.openDrawer());
		expect(result.current.drawerOpen).toBe(true);
		act(() => result.current.openDrawer());
		expect(result.current.drawerOpen).toBe(true);
		act(() => result.current.closeDrawer());
		expect(result.current.drawerOpen).toBe(false);
	});

	test("the drawer opened by pointer glides, by key does not", async () => {
		media.set(NARROW, true);
		const { useSidebar } = await load();
		const { result } = renderHook(() => useSidebar());
		fireEvent.pointerDown(document.body);
		act(() => result.current.openDrawer());
		expect(result.current.animate).toBe(true);
		act(() => result.current.closeDrawer());
		fireEvent.keyDown(document.body, { key: "Enter" });
		act(() => result.current.openDrawer());
		expect(result.current.animate).toBe(false);
		fireEvent.pointerDown(document.body);
	});

	test("widening the window reports not narrow, and an open drawer no longer counts", async () => {
		media.set(NARROW, true);
		const { useSidebar } = await load();
		const { result } = renderHook(() => useSidebar());
		act(() => result.current.openDrawer());
		expect(result.current.drawerOpen).toBe(true);
		media.set(NARROW, false);
		expect(result.current.narrow).toBe(false);
		expect(result.current.drawerOpen).toBe(false);
	});
});

describe("useSidebarDrawer", () => {
	test("reports narrow and the drawer without the rail state", async () => {
		media.set(NARROW, true);
		const { useSidebarDrawer } = await load();
		const { result } = renderHook(() => useSidebarDrawer());
		expect(result.current.narrow).toBe(true);
		act(() => result.current.openDrawer());
		expect(result.current.drawerOpen).toBe(true);
	});

	test("a wide screen never has an open drawer", async () => {
		const { useSidebarDrawer } = await load();
		const { result } = renderHook(() => useSidebarDrawer());
		act(() => result.current.openDrawer());
		expect(result.current.narrow).toBe(false);
		expect(result.current.drawerOpen).toBe(false);
	});
});

describe("useSidebarShortcut", () => {
	async function mount() {
		const mod = await load();
		const view = renderHook(() => {
			mod.useSidebarShortcut();
			return mod.useSidebar();
		});
		return view;
	}

	test("Ctrl+B and Cmd+B collapse and expand the rail, instantly", async () => {
		const { result } = await mount();
		fireEvent.keyDown(window, { key: "b", ctrlKey: true });
		expect(result.current.collapsed).toBe(true);
		expect(result.current.animate).toBe(false);
		fireEvent.keyDown(window, { key: "B", metaKey: true });
		expect(result.current.collapsed).toBe(false);
		expect(window.localStorage.getItem(KEY)).toBe("expanded");
	});

	test("the chord is cancelled so the browser does not act on it", async () => {
		await mount();
		expect(fireEvent.keyDown(window, { key: "b", ctrlKey: true })).toBe(false);
	});

	test("other keys and modifier mixes do nothing", async () => {
		const { result } = await mount();
		fireEvent.keyDown(window, { key: "b" });
		fireEvent.keyDown(window, { key: "k", ctrlKey: true });
		fireEvent.keyDown(window, { key: "b", ctrlKey: true, altKey: true });
		fireEvent.keyDown(window, { key: "b", ctrlKey: true, shiftKey: true });
		expect(result.current.collapsed).toBe(false);
	});

	test("typing in a field is left alone", async () => {
		const { result } = await mount();
		for (const el of [
			document.createElement("input"),
			document.createElement("textarea"),
			document.createElement("select"),
		]) {
			document.body.append(el);
			expect(fireEvent.keyDown(el, { key: "b", ctrlKey: true })).toBe(true);
		}
		const editable = document.createElement("div");
		Object.defineProperty(editable, "isContentEditable", { value: true });
		document.body.append(editable);
		fireEvent.keyDown(editable, { key: "b", ctrlKey: true });
		expect(result.current.collapsed).toBe(false);
	});

	test("narrow: it opens and shuts the drawer instead", async () => {
		media.set(NARROW, true);
		const { result } = await mount();
		fireEvent.keyDown(window, { key: "b", ctrlKey: true });
		expect(result.current.drawerOpen).toBe(true);
		fireEvent.keyDown(window, { key: "b", ctrlKey: true });
		expect(result.current.drawerOpen).toBe(false);
		expect(window.localStorage.getItem(KEY)).toBeNull();
	});

	test("stops listening when unmounted", async () => {
		const { result, unmount } = await mount();
		unmount();
		expect(fireEvent.keyDown(window, { key: "b", ctrlKey: true })).toBe(true);
		expect(result.current.collapsed).toBe(false);
	});
});
