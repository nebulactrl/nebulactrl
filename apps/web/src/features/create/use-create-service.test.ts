import { afterEach, describe, expect, test } from "bun:test";
import { act, renderHook } from "@testing-library/react";
import { createActions, useCreateState } from "./store";
import { useCreateService } from "./use-create-service";

afterEach(() => {
	act(() => createActions.closeDialog());
});

describe("useCreateService", () => {
	test("open(kind, options) opens that dialog from anywhere", () => {
		const state = renderHook(() => useCreateState());
		const { result } = renderHook(() => useCreateService());
		act(() => result.current.open("database", { engine: "mysql" }));
		expect(state.result.current.request).toEqual({
			kind: "database",
			engine: "mysql",
		});
	});

	test("open() with no Create button on screen starts the Docker image dialog", () => {
		const state = renderHook(() => useCreateState());
		const { result } = renderHook(() => useCreateService());
		act(() => result.current.open());
		expect(state.result.current.request).toEqual({ kind: "image" });
	});

	test("the API object is stable across renders, so it is safe in effect dependencies", () => {
		const { result, rerender } = renderHook(() => useCreateService());
		const first = result.current;
		rerender();
		expect(result.current).toBe(first);
	});
});
