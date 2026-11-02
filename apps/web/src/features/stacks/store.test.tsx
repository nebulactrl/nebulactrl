import { afterEach, describe, expect, test } from "bun:test";
import { act, renderHook } from "@testing-library/react";
import { createStackActions, useCreateStackState } from "./store";

afterEach(() => {
	act(() => createStackActions.close());
});

describe("create stack store", () => {
	test("starts closed with no drop position", () => {
		const { result } = renderHook(() => useCreateStackState());
		expect(result.current).toEqual({ open: false, position: undefined });
	});

	test("open() remembers where the node was dropped", () => {
		const { result } = renderHook(() => useCreateStackState());
		act(() => createStackActions.open({ x: 120, y: 40 }));
		expect(result.current).toEqual({ open: true, position: { x: 120, y: 40 } });
	});

	test("open() without a position lets the canvas place the node", () => {
		const { result } = renderHook(() => useCreateStackState());
		act(() => createStackActions.open());
		expect(result.current.open).toBe(true);
		expect(result.current.position).toBeUndefined();
	});

	test("close() keeps the position so the closing dialog does not jump", () => {
		const { result } = renderHook(() => useCreateStackState());
		act(() => createStackActions.open({ x: 5, y: 6 }));
		act(() => createStackActions.close());
		expect(result.current).toEqual({ open: false, position: { x: 5, y: 6 } });
	});
});
