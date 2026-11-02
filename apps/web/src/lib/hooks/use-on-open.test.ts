import { describe, expect, test } from "bun:test";
import { renderHook } from "@testing-library/react";
import { useOnOpen } from "./use-on-open";

function setup(initialOpen: boolean) {
	const starts: number[] = [];
	const start = () => starts.push(starts.length + 1);
	const hook = renderHook(({ open }) => useOnOpen(open, start), {
		initialProps: { open: initialOpen },
	});
	return { starts, ...hook };
}

describe("useOnOpen", () => {
	test("does not start a session for a list that mounts open or closed", () => {
		expect(setup(true).starts).toEqual([]);
		expect(setup(false).starts).toEqual([]);
	});

	test("starts one session each time the list opens", () => {
		const { starts, rerender } = setup(false);
		rerender({ open: true });
		expect(starts).toEqual([1]);
		rerender({ open: true });
		expect(starts).toEqual([1]);
		rerender({ open: false });
		expect(starts).toEqual([1]);
		rerender({ open: true });
		expect(starts).toEqual([1, 2]);
	});
});
