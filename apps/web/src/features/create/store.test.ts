import { afterEach, describe, expect, test } from "bun:test";
import { act, renderHook } from "@testing-library/react";
import { createActions, useCreateState } from "./store";

// Buttons a test mounted, unmounted afterwards so the count starts at 0 again.
let unregisters: Array<() => void> = [];
const registerButton = () => {
	const unregisterOnce = createActions.registerButton();
	let done = false;
	const unregister = () => {
		if (done) return;
		done = true;
		unregisterOnce();
	};
	unregisters.push(unregister);
	return unregister;
};

afterEach(() => {
	act(() => {
		for (const unregister of unregisters) unregister();
		unregisters = [];
		createActions.closeDialog();
		createActions.setMenuOpen(false);
	});
});

describe("create store", () => {
	test("starts with no dialog, no menu and no Create button", () => {
		const { result } = renderHook(() => useCreateState());
		expect(result.current).toEqual({ request: null, menu: null, buttons: 0 });
	});

	describe("open", () => {
		test("with a kind opens that dialog and carries the options along", () => {
			const { result } = renderHook(() => useCreateState());
			act(() =>
				createActions.open("database", {
					position: { x: 10, y: 20 },
					engine: "postgres",
				}),
			);
			expect(result.current.request).toEqual({
				kind: "database",
				position: { x: 10, y: 20 },
				engine: "postgres",
			});
			expect(result.current.menu).toBeNull();
		});

		test("with a kind closes a menu that is open", () => {
			const { result } = renderHook(() => useCreateState());
			act(() => {
				registerButton();
			});
			act(() => createActions.open());
			expect(result.current.menu).not.toBeNull();
			act(() => createActions.open("git"));
			expect(result.current.menu).toBeNull();
			expect(result.current.request).toEqual({ kind: "git" });
		});

		test("without a kind opens the menu when a Create button is on screen, keeping the options", () => {
			const { result } = renderHook(() => useCreateState());
			act(() => {
				registerButton();
			});
			act(() => createActions.open(undefined, { position: { x: 1, y: 2 } }));
			expect(result.current.menu).toEqual({
				options: { position: { x: 1, y: 2 } },
			});
			expect(result.current.request).toBeNull();
		});

		test("without a kind falls back to the Docker image dialog when no Create button is mounted", () => {
			const { result } = renderHook(() => useCreateState());
			act(() => createActions.open(undefined, { position: { x: 3, y: 4 } }));
			expect(result.current.request).toEqual({
				kind: "image",
				position: { x: 3, y: 4 },
			});
			expect(result.current.menu).toBeNull();
		});
	});

	describe("registerButton", () => {
		test("counts mounted buttons and the returned function removes only its own", () => {
			const { result } = renderHook(() => useCreateState());
			let first = () => {};
			let second = () => {};
			act(() => {
				first = registerButton();
				second = registerButton();
			});
			expect(result.current.buttons).toBe(2);
			act(() => first());
			expect(result.current.buttons).toBe(1);
			act(() => second());
			expect(result.current.buttons).toBe(0);
		});

		test("with the last button gone, open() goes straight to the image dialog again", () => {
			const { result } = renderHook(() => useCreateState());
			let unregister = () => {};
			act(() => {
				unregister = registerButton();
			});
			act(() => unregister());
			act(() => createActions.open());
			expect(result.current.request).toEqual({ kind: "image" });
			expect(result.current.menu).toBeNull();
		});
	});

	describe("setMenuOpen", () => {
		test("opening shows the menu with empty options", () => {
			const { result } = renderHook(() => useCreateState());
			act(() => createActions.setMenuOpen(true));
			expect(result.current.menu).toEqual({ options: {} });
		});

		test("opening again keeps the options the menu was opened with", () => {
			const { result } = renderHook(() => useCreateState());
			act(() => {
				registerButton();
			});
			act(() => createActions.open(undefined, { position: { x: 7, y: 8 } }));
			act(() => createActions.setMenuOpen(true));
			expect(result.current.menu).toEqual({
				options: { position: { x: 7, y: 8 } },
			});
		});

		test("closing hides it", () => {
			const { result } = renderHook(() => useCreateState());
			act(() => createActions.setMenuOpen(true));
			act(() => createActions.setMenuOpen(false));
			expect(result.current.menu).toBeNull();
		});
	});

	describe("choose", () => {
		test("opens the dialog with the menu's options and closes the menu", () => {
			const { result } = renderHook(() => useCreateState());
			act(() => {
				registerButton();
			});
			act(() => createActions.open(undefined, { position: { x: 5, y: 6 } }));
			act(() => createActions.choose("image"));
			expect(result.current.request).toEqual({
				kind: "image",
				position: { x: 5, y: 6 },
			});
			expect(result.current.menu).toBeNull();
		});

		test("an engine preselects it in the database dialog", () => {
			const { result } = renderHook(() => useCreateState());
			act(() => createActions.setMenuOpen(true));
			act(() => createActions.choose("database", "valkey"));
			expect(result.current.request).toEqual({
				kind: "database",
				engine: "valkey",
			});
		});

		test("without a menu open it still opens the dialog, with no options", () => {
			const { result } = renderHook(() => useCreateState());
			act(() => createActions.choose("git"));
			expect(result.current.request).toEqual({ kind: "git" });
		});
	});

	test("closeDialog clears the request but leaves the menu state alone", () => {
		const { result } = renderHook(() => useCreateState());
		act(() => createActions.open("git"));
		act(() => createActions.closeDialog());
		expect(result.current.request).toBeNull();
	});

	test("every subscribed component sees a change; an unmounted one stops listening", () => {
		const first = renderHook(() => useCreateState());
		const second = renderHook(() => useCreateState());
		act(() => createActions.open("git"));
		expect(first.result.current.request).toEqual({ kind: "git" });
		expect(second.result.current.request).toEqual({ kind: "git" });
		first.unmount();
		act(() => createActions.closeDialog());
		expect(second.result.current.request).toBeNull();
	});
});
