import { afterEach, describe, expect, test } from "bun:test";
import { act, renderHook } from "@testing-library/react";
import { closeAdoption, openAdoption, useAdoptionState } from "./store";

afterEach(() => act(() => closeAdoption()));

describe("adoption store", () => {
	test("starts closed, opens for a server and closes, and notifies every subscriber", () => {
		const first = renderHook(() => useAdoptionState());
		const second = renderHook(() => useAdoptionState());
		expect(first.result.current.open).toBe(false);

		act(() => openAdoption("srv-1"));
		expect(first.result.current).toEqual({ open: true, serverId: "srv-1" });
		expect(second.result.current).toEqual({ open: true, serverId: "srv-1" });

		act(() => closeAdoption());
		expect(first.result.current.open).toBe(false);
		expect(second.result.current.open).toBe(false);
	});

	test("closing keeps the server, so the dialog does not change while it fades out", () => {
		const hook = renderHook(() => useAdoptionState());
		act(() => openAdoption("srv-1"));
		act(() => closeAdoption());
		expect(hook.result.current.serverId).toBe("srv-1");
	});

	test("opening for another server replaces the first", () => {
		const hook = renderHook(() => useAdoptionState());
		act(() => openAdoption("srv-1"));
		act(() => openAdoption("srv-2"));
		expect(hook.result.current).toEqual({ open: true, serverId: "srv-2" });
	});

	test("an unmounted subscriber is no longer notified", () => {
		let renders = 0;
		const hook = renderHook(() => {
			renders += 1;
			return useAdoptionState();
		});
		hook.unmount();
		const before = renders;
		act(() => openAdoption("srv-1"));
		expect(renders).toBe(before);
	});
});
