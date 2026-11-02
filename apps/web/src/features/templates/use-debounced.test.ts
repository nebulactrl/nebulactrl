import { describe, expect, test } from "bun:test";
import { act, renderHook, waitFor } from "@testing-library/react";
import { useDebounced } from "./use-debounced";

describe("useDebounced", () => {
	test("starts with the first value", () => {
		const { result } = renderHook(() => useDebounced("a", 10_000));
		expect(result.current).toBe("a");
	});

	test("holds the old value until the new one has stopped changing", async () => {
		const { result, rerender } = renderHook(
			({ value }) => useDebounced(value, 10_000),
			{ initialProps: { value: "a" } },
		);
		rerender({ value: "b" });
		expect(result.current).toBe("a");
	});

	test("settles on the latest value after the delay", async () => {
		const { result, rerender } = renderHook(
			({ value }) => useDebounced(value, 15),
			{ initialProps: { value: "a" } },
		);
		await act(async () => rerender({ value: "b" }));
		await act(async () => rerender({ value: "c" }));
		await waitFor(() => expect(result.current).toBe("c"));
	});
});
