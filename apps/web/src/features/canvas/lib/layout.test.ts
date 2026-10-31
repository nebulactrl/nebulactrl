import { describe, expect, test } from "bun:test";
import type { CanvasLayout } from "#/api/models";
import {
	arrangeSelection,
	autoLayout,
	createGroup,
	emptyLayout,
	GROUP_HEADER,
	GROUP_PAD,
	mergeLayout,
	NODE_HEIGHT,
	NODE_WIDTH,
	neighbourInDirection,
	toggleGroup,
} from "./layout";

function overlap(
	a: { x: number; y: number },
	b: { x: number; y: number },
): boolean {
	return (
		a.x < b.x + NODE_WIDTH &&
		a.x + NODE_WIDTH > b.x &&
		a.y < b.y + NODE_HEIGHT &&
		a.y + NODE_HEIGHT > b.y
	);
}

describe("mergeLayout", () => {
	test("keeps saved positions exactly", () => {
		const saved: CanvasLayout = {
			nodes: { a: { x: 10, y: 20 }, b: { x: 500, y: -40 } },
			groups: [],
			viewport: { x: 1, y: 2, zoom: 0.5 },
		};
		const { layout, placed } = mergeLayout(["a", "b"], saved);
		expect(layout.nodes).toEqual(saved.nodes);
		expect(layout.viewport).toEqual(saved.viewport);
		expect(placed).toEqual([]);
	});

	test("places new services to the right of existing ones without overlap", () => {
		const saved: CanvasLayout = {
			nodes: { a: { x: 0, y: 0 }, b: { x: 340, y: 0 } },
			groups: [],
		};
		const { layout, placed } = mergeLayout(["a", "b", "c", "d"], saved);
		expect(placed).toEqual(["c", "d"]);
		for (const id of placed) {
			const p = layout.nodes[id];
			expect(p).toBeDefined();
			if (!p) continue;
			expect(p.x).toBeGreaterThanOrEqual(340 + NODE_WIDTH);
		}
		const all = Object.values(layout.nodes);
		for (let i = 0; i < all.length; i++) {
			for (let j = i + 1; j < all.length; j++) {
				const a = all[i];
				const b = all[j];
				if (a && b) expect(overlap(a, b)).toBe(false);
			}
		}
	});

	test("lays out a fresh canvas on a non-overlapping grid", () => {
		const ids = ["a", "b", "c", "d", "e"];
		const { layout, placed } = mergeLayout(ids, undefined);
		expect(placed).toEqual(ids);
		const all = ids.map((id) => layout.nodes[id]);
		for (let i = 0; i < all.length; i++) {
			for (let j = i + 1; j < all.length; j++) {
				const a = all[i];
				const b = all[j];
				if (a && b) expect(overlap(a, b)).toBe(false);
			}
		}
	});

	test("drops positions and group members of deleted services", () => {
		const saved: CanvasLayout = {
			nodes: { a: { x: 0, y: 0 }, gone: { x: 400, y: 0 } },
			groups: [
				{
					id: "g1",
					name: "Backend",
					x: 0,
					y: 0,
					w: 300,
					h: 200,
					collapsed: false,
					serviceIds: ["gone"],
				},
			],
		};
		const { layout, pruned } = mergeLayout(["a"], saved);
		expect(Object.keys(layout.nodes)).toEqual(["a"]);
		expect(layout.groups).toEqual([]);
		expect(pruned).toBe(true);
	});

	test("does not place a new service over a group frame", () => {
		const saved: CanvasLayout = {
			nodes: { a: { x: 0, y: 0 } },
			groups: [
				{
					id: "g1",
					name: "G",
					x: -24,
					y: -64,
					w: 900,
					h: 400,
					collapsed: false,
					serviceIds: ["a"],
				},
			],
		};
		const { layout } = mergeLayout(["a", "b"], saved);
		const b = layout.nodes.b;
		expect(b).toBeDefined();
		if (b) expect(b.x).toBeGreaterThanOrEqual(-24 + 900);
	});
});

describe("groups", () => {
	const base: CanvasLayout = {
		nodes: { a: { x: 0, y: 0 }, b: { x: 340, y: 100 }, c: { x: 800, y: 0 } },
		groups: [],
	};

	test("createGroup frames its members with padding and header room", () => {
		const layout = createGroup(base, ["a", "b"], "g1", "Pair");
		const g = layout.groups[0];
		expect(g?.x).toBe(-GROUP_PAD);
		expect(g?.y).toBe(-GROUP_PAD - GROUP_HEADER);
		expect(g?.w).toBe(340 + NODE_WIDTH + GROUP_PAD * 2);
	});

	test("a service moves between groups instead of joining two", () => {
		const first = createGroup(base, ["a", "b"], "g1", "One");
		const second = createGroup(first, ["b", "c"], "g2", "Two");
		expect(second.groups.find((g) => g.id === "g1")?.serviceIds).toEqual(["a"]);
		expect(second.groups.find((g) => g.id === "g2")?.serviceIds).toEqual([
			"b",
			"c",
		]);
	});

	test("toggleGroup flips collapsed", () => {
		const layout = createGroup(base, ["a", "b"], "g1", "Pair");
		expect(toggleGroup(layout, "g1").groups[0]?.collapsed).toBe(true);
	});
});

describe("autoLayout", () => {
	test("ranks linked services left to right", () => {
		const layout: CanvasLayout = {
			nodes: { web: { x: 0, y: 0 }, api: { x: 0, y: 0 }, db: { x: 0, y: 0 } },
			groups: [],
		};
		const next = autoLayout(layout, [
			{ fromServiceId: "web", toServiceId: "api" },
			{ fromServiceId: "api", toServiceId: "db" },
		]);
		const web = next.nodes.web;
		const api = next.nodes.api;
		const db = next.nodes.db;
		expect(web && api && db).toBeTruthy();
		if (!(web && api && db)) return;
		expect(web.x).toBeLessThan(api.x);
		expect(api.x).toBeLessThan(db.x);
	});

	test("unlinked services share a grid with at least 72px between cards", () => {
		const layout: CanvasLayout = {
			nodes: { a: { x: 0, y: 0 }, b: { x: 0, y: 0 } },
			groups: [],
		};
		const next = autoLayout(layout, []);
		const a = next.nodes.a;
		const b = next.nodes.b;
		expect(a && b).toBeTruthy();
		if (!(a && b)) return;
		expect(a.y).toBe(b.y);
		expect(Math.abs(a.x - b.x) - NODE_WIDTH).toBeGreaterThanOrEqual(72);
	});
});

describe("autoLayout with groups", () => {
	test("a group is ranked as one frame, so neighbours never land inside it", () => {
		const base: CanvasLayout = {
			nodes: {
				a: { x: 0, y: 0 },
				b: { x: 0, y: 0 },
				c: { x: 0, y: 0 },
			},
			groups: [],
		};
		const grouped = createGroup(base, ["a", "b"], "g1", "Pair");
		for (const layout of [grouped, toggleGroup(grouped, "g1")]) {
			const next = autoLayout(layout, [
				{ fromServiceId: "a", toServiceId: "c" },
			]);
			const group = next.groups[0];
			const c = next.nodes.c;
			expect(group && c).toBeTruthy();
			if (!(group && c)) continue;
			const inside =
				c.x < group.x + group.w &&
				c.x + NODE_WIDTH > group.x &&
				c.y < group.y + group.h &&
				c.y + NODE_HEIGHT > group.y;
			expect(inside).toBe(false);
			for (const id of ["a", "b"]) {
				const p = next.nodes[id];
				expect(p && p.x >= group.x && p.y >= group.y).toBe(true);
				expect(p && p.x + NODE_WIDTH <= group.x + group.w).toBe(true);
				expect(p && p.y + NODE_HEIGHT <= group.y + group.h).toBe(true);
			}
		}
	});
});

describe("neighbourInDirection", () => {
	const centres = {
		a: { x: 0, y: 0 },
		b: { x: 300, y: 10 },
		c: { x: 300, y: 200 },
		d: { x: -300, y: 0 },
	};
	test("picks the nearest in line", () => {
		expect(neighbourInDirection("a", "right", centres)).toBe("b");
		expect(neighbourInDirection("a", "left", centres)).toBe("d");
		expect(neighbourInDirection("b", "down", centres)).toBe("c");
	});
	test("returns undefined when nothing lies that way", () => {
		expect(neighbourInDirection("d", "left", centres)).toBeUndefined();
		expect(neighbourInDirection("a", "up", centres)).toBeUndefined();
	});
	test("an empty layout is valid", () => {
		expect(emptyLayout().groups).toEqual([]);
	});
});

describe("arrangeSelection", () => {
	const layout: CanvasLayout = {
		nodes: {
			a: { x: 1000, y: 500 },
			b: { x: 1000, y: 100 },
			c: { x: 1400, y: 900 },
			far: { x: -600, y: -600 },
		},
		groups: [],
		viewport: null,
	};

	test("leaves services outside the selection where they are", () => {
		const next = arrangeSelection(layout, [], ["a", "b", "c"]);
		expect(next.nodes.far).toEqual({ x: -600, y: -600 });
	});

	test("keeps the selection's top-left corner", () => {
		const next = arrangeSelection(layout, [], ["a", "b", "c"]);
		const xs = ["a", "b", "c"].map((id) => next.nodes[id]?.x ?? 0);
		const ys = ["a", "b", "c"].map((id) => next.nodes[id]?.y ?? 0);
		expect(Math.min(...xs)).toBe(1000);
		expect(Math.min(...ys)).toBe(100);
	});

	test("ranks linked services left to right", () => {
		const next = arrangeSelection(
			layout,
			[{ fromServiceId: "a", toServiceId: "b" }],
			["a", "b"],
		);
		const a = next.nodes.a;
		const b = next.nodes.b;
		expect(a && b && b.x >= a.x + NODE_WIDTH).toBe(true);
	});

	test("unlinked services land in a grid without overlapping", () => {
		const next = arrangeSelection(layout, [], ["a", "b", "c"]);
		const spots = ["a", "b", "c"].map((id) => next.nodes[id]);
		for (const [i, p] of spots.entries()) {
			for (const q of spots.slice(i + 1)) {
				expect(p && q && overlap(p, q)).toBe(false);
			}
		}
	});

	test("ignores links that leave the selection", () => {
		const next = arrangeSelection(
			layout,
			[{ fromServiceId: "a", toServiceId: "far" }],
			["a", "b"],
		);
		expect(next.nodes.far).toEqual({ x: -600, y: -600 });
		const a = next.nodes.a;
		const b = next.nodes.b;
		expect(a && b && overlap(a, b)).toBe(false);
	});

	test("fewer than two services changes nothing", () => {
		expect(arrangeSelection(layout, [], ["a"])).toBe(layout);
		expect(arrangeSelection(layout, [], ["a", "missing"])).toBe(layout);
	});

	test("a group frame follows its members", () => {
		const grouped = createGroup(layout, ["a", "b"], "g", "G");
		const next = arrangeSelection(
			grouped,
			[{ fromServiceId: "a", toServiceId: "b" }],
			["a", "b"],
		);
		const frame = next.groups[0];
		const a = next.nodes.a;
		expect(
			frame && a && a.x >= frame.x && a.x + NODE_WIDTH <= frame.x + frame.w,
		).toBe(true);
	});
});
