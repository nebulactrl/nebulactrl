import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { act, renderHook } from "@testing-library/react";
import { usePersisted } from "./use-persisted";

type Unit = "mb" | "pct";
const isUnit = (value: unknown): value is Unit =>
	value === "mb" || value === "pct";

/** Swaps window.localStorage for `fake` (private mode, a blocked or full store) until the returned function runs. */
function replaceStorage(fake: Partial<Storage>): () => void {
	const original = Object.getOwnPropertyDescriptor(window, "localStorage");
	Object.defineProperty(window, "localStorage", {
		configurable: true,
		value: fake,
	});
	return () => {
		if (original) Object.defineProperty(window, "localStorage", original);
	};
}

beforeEach(() => window.localStorage.clear());
afterEach(() => window.localStorage.clear());

describe("usePersisted", () => {
	test("starts at the fallback when nothing is stored", () => {
		const { result } = renderHook(() =>
			usePersisted<Unit>("unit", "mb", isUnit),
		);
		expect(result.current[0]).toBe("mb");
	});

	test("starts at a stored value the validator accepts", () => {
		window.localStorage.setItem("nebulactrl:unit", "pct");
		const { result } = renderHook(() =>
			usePersisted<Unit>("unit", "mb", isUnit),
		);
		expect(result.current[0]).toBe("pct");
	});

	test("a stale or hand-edited value falls back to the default", () => {
		window.localStorage.setItem("nebulactrl:unit", "bananas");
		const { result } = renderHook(() =>
			usePersisted<Unit>("unit", "mb", isUnit),
		);
		expect(result.current[0]).toBe("mb");
	});

	test("setting updates the value and stores it under the namespaced key", () => {
		const { result } = renderHook(() =>
			usePersisted<Unit>("unit", "mb", isUnit),
		);
		act(() => result.current[1]("pct"));
		expect(result.current[0]).toBe("pct");
		expect(window.localStorage.getItem("nebulactrl:unit")).toBe("pct");
	});

	test("a later mount reads what an earlier one stored", () => {
		const first = renderHook(() => usePersisted<Unit>("unit", "mb", isUnit));
		act(() => first.result.current[1]("pct"));
		first.unmount();
		const second = renderHook(() => usePersisted<Unit>("unit", "mb", isUnit));
		expect(second.result.current[0]).toBe("pct");
	});

	test("different keys do not share a value", () => {
		window.localStorage.setItem("nebulactrl:other", "pct");
		const { result } = renderHook(() =>
			usePersisted<Unit>("unit", "mb", isUnit),
		);
		expect(result.current[0]).toBe("mb");
	});

	test("unreadable storage falls back to the default", () => {
		window.localStorage.setItem("nebulactrl:unit", "pct");
		const restore = replaceStorage({
			getItem: () => {
				throw new Error("denied");
			},
		});
		try {
			const { result } = renderHook(() =>
				usePersisted<Unit>("unit", "mb", isUnit),
			);
			expect(result.current[0]).toBe("mb");
		} finally {
			restore();
		}
	});

	test("unwritable storage still keeps the choice for this visit", () => {
		let attempts = 0;
		const restore = replaceStorage({
			getItem: () => null,
			setItem: () => {
				attempts += 1;
				throw new Error("quota");
			},
		});
		try {
			const { result } = renderHook(() =>
				usePersisted<Unit>("unit", "mb", isUnit),
			);
			act(() => result.current[1]("pct"));
			expect(attempts).toBe(1);
			expect(result.current[0]).toBe("pct");
		} finally {
			restore();
		}
	});
});
