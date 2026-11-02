import { describe, expect, test } from "bun:test";
import { renderHook } from "@testing-library/react";
import { rangeMs } from "./ranges";
import { useChartDomain } from "./use-chart-domain";

describe("useChartDomain", () => {
	test("ends at the time the data arrived and reaches the range back", () => {
		const { result } = renderHook(() =>
			useChartDomain("1h", 1_700_000_000_000, 10),
		);
		expect(result.current).toEqual({
			from: 1_700_000_000_000 - rangeMs("1h"),
			to: 1_700_000_000_000,
			stepSeconds: 10,
		});
	});

	test("uses the current time before any data arrived", () => {
		const before = Date.now();
		const { result } = renderHook(() => useChartDomain("5m", 0, 10));
		const after = Date.now();
		expect(result.current.to).toBeGreaterThanOrEqual(before);
		expect(result.current.to).toBeLessThanOrEqual(after);
		expect(result.current.to - result.current.from).toBe(300_000);
	});

	test("re-renders with the same inputs keep the very same domain", () => {
		const { result, rerender } = renderHook(() =>
			useChartDomain("1h", 1_700_000_000_000, 10),
		);
		const first = result.current;
		rerender();
		expect(result.current).toBe(first);
	});

	test("a new arrival slides the window, a new range resizes it", () => {
		const { result, rerender } = renderHook(
			({ range, at }) => useChartDomain(range, at, 60),
			{ initialProps: { range: "1h", at: 10_000_000 } },
		);
		rerender({ range: "1h", at: 10_005_000 });
		expect(result.current.to).toBe(10_005_000);
		expect(result.current.from).toBe(10_005_000 - 3_600_000);
		rerender({ range: "24h", at: 10_005_000 });
		expect(result.current.from).toBe(10_005_000 - 86_400_000);
	});
});
