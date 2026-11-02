import { describe, expect, test } from "bun:test";
import { act, renderHook, waitFor } from "@testing-library/react";
import { useDebounced } from "./use-debounced";

describe("useDebounced", () => {
	test("starts with the initial value", () => {
		const { result } = renderHook(() => useDebounced("a", 10));
		expect(result.current).toBe("a");
	});

	test("holds the old value until the input stops changing for the delay", async () => {
		const { result, rerender } = renderHook(
			({ value }) => useDebounced(value, 40),
			{ initialProps: { value: "a" } },
		);
		rerender({ value: "ab" });
		expect(result.current).toBe("a");
		rerender({ value: "abc" });
		expect(result.current).toBe("a");
		await waitFor(() => expect(result.current).toBe("abc"));
	});

	test("a change that is replaced before the delay never surfaces", async () => {
		const seen: string[] = [];
		const { rerender, result } = renderHook(
			({ value }) => {
				const v = useDebounced(value, 30);
				seen.push(v);
				return v;
			},
			{ initialProps: { value: "a" } },
		);
		rerender({ value: "b" });
		await act(async () => {
			await new Promise((r) => setTimeout(r, 10));
		});
		rerender({ value: "c" });
		await waitFor(() => expect(result.current).toBe("c"));
		expect(seen).not.toContain("b");
	});
});
