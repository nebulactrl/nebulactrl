import { afterEach, describe, expect, test } from "bun:test";
import { act, renderHook } from "@testing-library/react";
import { coolifyImportActions, useCoolifyImportState } from "./store";

afterEach(() => coolifyImportActions.close());

describe("coolify import store", () => {
	test("starts closed, opens and closes, and notifies every subscriber", () => {
		const first = renderHook(() => useCoolifyImportState());
		const second = renderHook(() => useCoolifyImportState());
		expect(first.result.current.open).toBe(false);

		act(() => coolifyImportActions.open());
		expect(first.result.current.open).toBe(true);
		expect(second.result.current.open).toBe(true);

		act(() => coolifyImportActions.close());
		expect(first.result.current.open).toBe(false);
		expect(second.result.current.open).toBe(false);
	});

	test("an unmounted subscriber is no longer notified", () => {
		let renders = 0;
		const hook = renderHook(() => {
			renders += 1;
			return useCoolifyImportState();
		});
		hook.unmount();
		const before = renders;
		act(() => coolifyImportActions.open());
		expect(renders).toBe(before);
	});

	test("a hook mounted after opening sees it open", () => {
		act(() => coolifyImportActions.open());
		const late = renderHook(() => useCoolifyImportState());
		expect(late.result.current.open).toBe(true);
	});
});
