import { describe, expect, jest, test } from "bun:test";
import { renderHook } from "@testing-library/react";
import {
	type PaletteCommand,
	useCommandRegistry,
	useRegisterCommands,
} from "./palette-registry";

function cmd(id: string, label = id): PaletteCommand {
	return { id, label, run: () => {} };
}

function setup(sources: Record<string, readonly PaletteCommand[]>) {
	return renderHook(
		({ current }: { current: Record<string, readonly PaletteCommand[]> }) => {
			for (const [id, commands] of Object.entries(current)) {
				// biome-ignore lint/correctness/useHookAtTopLevel: the set of sources is fixed per test.
				useRegisterCommands(id, commands);
			}
			return useCommandRegistry();
		},
		{ initialProps: { current: sources } },
	);
}

describe("palette registry", () => {
	test("a mounted source's commands appear, and go away when it unmounts", () => {
		const viewer = renderHook(() => useCommandRegistry());
		expect(viewer.result.current).toEqual([]);
		const source = renderHook(() =>
			useRegisterCommands("a", [cmd("a1"), cmd("a2")]),
		);
		expect(viewer.result.current.map((c) => c.id)).toEqual(["a1", "a2"]);
		source.unmount();
		expect(viewer.result.current).toEqual([]);
	});

	test("the registry is the union of every source, in registration order", () => {
		const a = [cmd("a1")];
		const b = [cmd("b1"), cmd("b2")];
		const { result } = setup({ a, b });
		expect(result.current.map((c) => c.id)).toEqual(["a1", "b1", "b2"]);
	});

	test("re-registering a source replaces its commands rather than appending", () => {
		const first = [cmd("x1")];
		const second = [cmd("x2")];
		const { result, rerender } = setup({ x: first });
		rerender({ current: { x: second } });
		expect(result.current.map((c) => c.id)).toEqual(["x2"]);
	});

	test("a stale unregister does not remove a newer registration of the same source", () => {
		const viewer = renderHook(() => useCommandRegistry());
		const one = renderHook(() => useRegisterCommands("shared", [cmd("one")]));
		const two = renderHook(() => useRegisterCommands("shared", [cmd("two")]));
		expect(viewer.result.current.map((c) => c.id)).toEqual(["two"]);
		one.unmount();
		expect(viewer.result.current.map((c) => c.id)).toEqual(["two"]);
		two.unmount();
		expect(viewer.result.current).toEqual([]);
	});

	test("a command's run is the caller's function", () => {
		const run = jest.fn();
		const { result } = setup({ r: [{ id: "r1", label: "Run", run }] });
		result.current[0]?.run();
		expect(run).toHaveBeenCalledTimes(1);
	});

	test("a viewer that mounts later sees what is already registered", () => {
		const source = renderHook(() => useRegisterCommands("late", [cmd("l1")]));
		const viewer = renderHook(() => useCommandRegistry());
		expect(viewer.result.current.map((c) => c.id)).toEqual(["l1"]);
		source.unmount();
	});
});
