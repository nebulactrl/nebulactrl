import { describe, expect, test } from "bun:test";
import { GRID, LAYER_GAP, type LayerVertex, layeredLayout } from "./layered";

const card = (id: string, extra: Partial<LayerVertex> = {}): LayerVertex => ({
	id,
	w: 288,
	h: 176,
	...extra,
});

describe("layeredLayout", () => {
	test("nothing to place gives nothing", () => {
		const result = layeredLayout([], []);
		expect(result.boxes.size).toBe(0);
		expect(result.crossings).toBe(0);
	});

	test("consumers sit left of what they use, one column apart", () => {
		const result = layeredLayout(
			[card("web"), card("api"), card("db")],
			[
				["web", "api"],
				["api", "db"],
			],
		);
		expect([...result.columns.values()]).toEqual([0, 1, 2]);
		expect(result.boxes.get("api")?.x).toBe(288 + LAYER_GAP + 0);
	});

	test("data goes to the last column, a worker moves up against what it feeds", () => {
		const result = layeredLayout(
			[
				card("web", { entry: true }),
				card("api"),
				card("worker"),
				card("db", { data: true }),
			],
			[
				["web", "api"],
				["api", "db"],
				["worker", "db"],
			],
		);
		expect(result.columns.get("db")).toBe(2);
		expect(result.columns.get("worker")).toBe(1);
		expect(result.columns.get("web")).toBe(0);
	});

	test("an entry stays above the other cards of its column", () => {
		const result = layeredLayout(
			[card("a"), card("b"), card("front", { entry: true }), card("t")],
			[
				["a", "t"],
				["b", "t"],
				["front", "t"],
			],
		);
		const front = result.boxes.get("front")?.y ?? 99;
		expect(front).toBeLessThan(result.boxes.get("a")?.y ?? 0);
		expect(front).toBeLessThan(result.boxes.get("b")?.y ?? 0);
	});

	test("two consumers of the same two providers cross once, as little as a column drawing can", () => {
		const result = layeredLayout(
			[
				card("a"),
				card("b"),
				card("x", { data: true }),
				card("y", { data: true }),
			],
			[
				["a", "x"],
				["a", "y"],
				["b", "x"],
				["b", "y"],
			],
		);
		expect(result.crossings).toBe(1);
	});

	test("a tree needs no crossing", () => {
		const result = layeredLayout(
			[card("a"), card("b"), card("c"), card("d"), card("e")],
			[
				["a", "b"],
				["a", "c"],
				["b", "d"],
				["c", "e"],
			],
		);
		expect(result.crossings).toBe(0);
	});

	test("a cycle still lands in distinct columns", () => {
		const result = layeredLayout(
			[card("a"), card("b"), card("c")],
			[
				["a", "b"],
				["b", "c"],
				["c", "a"],
			],
		);
		expect(new Set(result.columns.values()).size).toBe(3);
	});

	test("every anchor is on the lattice, and boxes in a column never overlap", () => {
		const ids = ["a", "b", "c", "d", "e", "f"];
		const result = layeredLayout(
			ids.map((id) => card(id, { h: id === "c" ? 300 : 176 })),
			[
				["a", "d"],
				["a", "e"],
				["b", "e"],
				["b", "f"],
				["c", "f"],
			],
		);
		for (const at of result.boxes.values()) {
			expect(at.x % GRID).toBe(0);
			expect(at.y % GRID).toBe(0);
		}
		const column = [...result.boxes.entries()]
			.filter(([id]) => result.columns.get(id) === 0)
			.map(([id, at]) => ({ at, h: id === "c" ? 300 : 176 }))
			.sort((p, q) => p.at.y - q.at.y);
		for (const [i, p] of column.entries()) {
			const next = column[i + 1];
			if (next) expect(p.at.y + p.h).toBeLessThanOrEqual(next.at.y);
		}
	});

	test("a frame's anchor, not its corner, is on the lattice", () => {
		const result = layeredLayout(
			[card("plain"), { id: "frame", w: 336, h: 400, insetY: 64 }],
			[["plain", "frame"]],
		);
		const frame = result.boxes.get("frame");
		expect(((frame?.y ?? 0) + 64) % GRID).toBe(0);
	});

	test("the order links come in does not matter", () => {
		const vertices = ["a", "b", "c", "d", "e"].map((id) => card(id));
		const links = [
			["a", "c"],
			["a", "d"],
			["b", "d"],
			["b", "e"],
			["c", "e"],
		] as const;
		const forward = layeredLayout(vertices, links);
		const backward = layeredLayout(vertices, [...links].reverse());
		expect([...backward.boxes]).toEqual([...forward.boxes]);
	});
});
