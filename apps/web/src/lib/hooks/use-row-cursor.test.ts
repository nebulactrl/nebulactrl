import { describe, expect, test } from "bun:test";
import { act, renderHook } from "@testing-library/react";
import { useRowCursor } from "./use-row-cursor";

const rows = (...ids: string[]) => ids.map((id) => ({ id }));

function setup(initial: { rows: { id: string }[]; query: string }) {
	return renderHook((props) => useRowCursor(props.rows, props.query), {
		initialProps: initial,
	});
}

describe("useRowCursor", () => {
	test("highlights the first row until the cursor moves", () => {
		const { result } = setup({ rows: rows("a", "b", "c"), query: "" });
		expect(result.current.activeIndex).toBe(0);
	});

	test("moveActive steps down and up and stops at both ends", () => {
		const { result } = setup({ rows: rows("a", "b", "c"), query: "" });
		act(() => result.current.moveActive(1));
		expect(result.current.activeIndex).toBe(1);
		act(() => result.current.moveActive(1));
		act(() => result.current.moveActive(1));
		expect(result.current.activeIndex).toBe(2);
		act(() => result.current.moveActive(-1));
		expect(result.current.activeIndex).toBe(1);
		act(() => result.current.moveActive(-1));
		act(() => result.current.moveActive(-1));
		expect(result.current.activeIndex).toBe(0);
	});

	test("two keys landing in one batch move two rows", () => {
		const { result } = setup({ rows: rows("a", "b", "c", "d"), query: "" });
		act(() => {
			result.current.moveActive(1);
			result.current.moveActive(1);
		});
		expect(result.current.activeIndex).toBe(2);
	});

	test("moveActive on an empty list does nothing", () => {
		const { result } = setup({ rows: [], query: "" });
		act(() => result.current.moveActive(1));
		expect(result.current.activeIndex).toBe(0);
	});

	test("moveTo highlights a row by id, and null resets to the first row", () => {
		const { result } = setup({ rows: rows("a", "b", "c"), query: "" });
		act(() => result.current.moveTo("c"));
		expect(result.current.activeIndex).toBe(2);
		act(() => result.current.moveTo(null));
		expect(result.current.activeIndex).toBe(0);
	});

	test("the cursor follows its row when the list reorders", () => {
		const { result, rerender } = setup({
			rows: rows("a", "b", "c"),
			query: "",
		});
		act(() => result.current.moveTo("b"));
		rerender({ rows: rows("c", "a", "b"), query: "" });
		expect(result.current.activeIndex).toBe(2);
	});

	test("a list swapped for different rows of the same length returns to the first row", () => {
		const { result, rerender } = setup({
			rows: rows("a", "b", "c"),
			query: "",
		});
		act(() => result.current.moveTo("b"));
		rerender({ rows: rows("x", "y", "z"), query: "" });
		expect(result.current.activeIndex).toBe(0);
	});

	test("rows that come back do not revive a cursor whose row left", () => {
		const { result, rerender } = setup({
			rows: rows("a", "b", "c"),
			query: "",
		});
		act(() => result.current.moveTo("c"));
		rerender({ rows: rows("a", "b"), query: "" });
		expect(result.current.activeIndex).toBe(0);
		rerender({ rows: rows("a", "b", "c"), query: "" });
		expect(result.current.activeIndex).toBe(0);
	});

	test("a changed query drops the cursor even when the row is still listed", () => {
		const { result, rerender } = setup({
			rows: rows("a", "b", "c"),
			query: "",
		});
		act(() => result.current.moveTo("c"));
		rerender({ rows: rows("a", "b", "c"), query: "x" });
		expect(result.current.activeIndex).toBe(0);
		rerender({ rows: rows("a", "b", "c"), query: "" });
		expect(result.current.activeIndex).toBe(0);
	});

	test("moveActive steps from the highlighted row under the current query", () => {
		const { result, rerender } = setup({
			rows: rows("a", "b", "c"),
			query: "",
		});
		act(() => result.current.moveTo("c"));
		rerender({ rows: rows("p", "q", "r"), query: "new" });
		act(() => result.current.moveActive(1));
		expect(result.current.activeIndex).toBe(1);
	});
});
