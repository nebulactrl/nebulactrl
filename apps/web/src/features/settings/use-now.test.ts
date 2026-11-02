import { afterEach, describe, expect, jest, test } from "bun:test";
import { act, renderHook } from "@testing-library/react";
import { useNow } from "./use-now";

afterEach(() => jest.useRealTimers());

describe("useNow", () => {
	test("returns the wall clock at mount and does not tick while inactive", () => {
		const { result } = renderHook(() => useNow(false));
		const first = result.current;
		expect(Math.abs(first - Date.now())).toBeLessThan(1_000);
		jest.useFakeTimers();
		act(() => {
			jest.advanceTimersByTime(5_000);
		});
		expect(result.current).toBe(first);
	});

	test("ticks once a second while active and stops when deactivated", () => {
		const { result, rerender } = renderHook(({ active }) => useNow(active), {
			initialProps: { active: false },
		});
		jest.useFakeTimers();
		jest.setSystemTime(new Date("2026-01-01T00:00:00Z"));
		rerender({ active: true });
		const start = result.current;
		expect(start).toBe(new Date("2026-01-01T00:00:00Z").getTime());

		act(() => {
			jest.advanceTimersByTime(3_000);
		});
		expect(result.current).toBe(start + 3_000);

		rerender({ active: false });
		act(() => {
			jest.advanceTimersByTime(5_000);
		});
		expect(result.current).toBe(start + 3_000);
	});

	test("clears its timer on unmount", () => {
		const { rerender, unmount } = renderHook(({ active }) => useNow(active), {
			initialProps: { active: false },
		});
		jest.useFakeTimers();
		rerender({ active: true });
		expect(jest.getTimerCount()).toBe(1);
		unmount();
		expect(jest.getTimerCount()).toBe(0);
	});
});
