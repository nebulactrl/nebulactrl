import { afterEach, beforeEach, expect, jest, test } from "bun:test";
import { act, renderHook } from "@testing-library/react";
import { useTicker } from "./use-ticker";

beforeEach(() => {
	jest.useFakeTimers();
	jest.setSystemTime(new Date("2026-01-01T00:00:00Z"));
});
afterEach(() => {
	jest.useRealTimers();
});

test("an active ticker moves to the new time every interval", () => {
	const { result } = renderHook(() => useTicker(true, 1000));
	const first = result.current;
	expect(first).toBe(Date.parse("2026-01-01T00:00:00Z"));
	act(() => {
		jest.advanceTimersByTime(3000);
	});
	expect(result.current).toBe(first + 3000);
});

test("the interval argument sets the refresh rate", () => {
	const { result } = renderHook(() => useTicker(true, 5000));
	const first = result.current;
	act(() => {
		jest.advanceTimersByTime(4000);
	});
	expect(result.current).toBe(first);
	act(() => {
		jest.advanceTimersByTime(1000);
	});
	expect(result.current).toBe(first + 5000);
});

test("an inactive ticker starts no timer and reports the time at render", () => {
	const { result, rerender } = renderHook(() => useTicker(false, 1000));
	const first = result.current;
	act(() => {
		jest.advanceTimersByTime(5000);
	});
	// No timer fired, so nothing re-rendered; a render reads the clock afresh.
	expect(jest.getTimerCount()).toBe(0);
	rerender();
	expect(result.current).toBe(first + 5000);
});

test("going inactive stops the timer, unmounting clears it", () => {
	const { rerender, unmount } = renderHook(
		({ active }) => useTicker(active, 1000),
		{ initialProps: { active: true } },
	);
	expect(jest.getTimerCount()).toBe(1);
	rerender({ active: false });
	expect(jest.getTimerCount()).toBe(0);
	rerender({ active: true });
	expect(jest.getTimerCount()).toBe(1);
	unmount();
	expect(jest.getTimerCount()).toBe(0);
});
