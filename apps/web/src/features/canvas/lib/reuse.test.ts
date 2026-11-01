import { describe, expect, test } from "bun:test";
import type { CanvasLayout } from "#/api/models";
import { makeService } from "./fixtures";
import { type AppNode, buildEdges, buildNodes } from "./flow";
import { reuseEdges, reuseNodes, sameElement } from "./reuse";

const a = makeService({ id: "a" });
const b = makeService({ id: "b" });
const services = [a, b];

const layout: CanvasLayout = {
	nodes: { a: { x: 0, y: 0 }, b: { x: 340, y: 100 } },
	groups: [],
	viewport: null,
};

const links = [{ fromServiceId: "a", toServiceId: "b" }];

describe("reuseNodes", () => {
	test("a rebuild of the same services and layout hands back the same array", () => {
		const held = buildNodes(services, layout, new Set(), links);
		const rebuilt = buildNodes(services, layout, new Set(), links);
		expect(rebuilt[0]).not.toBe(held[0]);
		expect(reuseNodes(held, rebuilt)).toBe(held);
	});

	test("only the card that moved is replaced", () => {
		const held = buildNodes(services, layout, new Set(), links);
		const moved: CanvasLayout = {
			...layout,
			nodes: { ...layout.nodes, b: { x: 360, y: 100 } },
		};
		const result = reuseNodes(
			held,
			buildNodes(services, moved, new Set(), links),
		);
		expect(result).not.toBe(held);
		expect(result.find((node) => node.id === "a")).toBe(
			held.find((node) => node.id === "a") as AppNode,
		);
		const replaced = result.find((node) => node.id === "b");
		expect(replaced).not.toBe(held.find((node) => node.id === "b"));
		expect(replaced?.position).toEqual({ x: 360, y: 100 });
	});

	test("a changed service replaces its card only", () => {
		const held = buildNodes(services, layout, new Set(), links);
		const renamed = makeService({ id: "b", name: "Renamed" });
		const result = reuseNodes(
			held,
			buildNodes([a, renamed], layout, new Set(), links),
		);
		expect(result.find((node) => node.id === "a")).toBe(
			held.find((node) => node.id === "a") as AppNode,
		);
		expect(result.find((node) => node.id === "b")).not.toBe(
			held.find((node) => node.id === "b") as AppNode,
		);
	});

	test("a card whose jacks changed is replaced", () => {
		const held = buildNodes(services, layout, new Set(), links);
		const unlinked = buildNodes(services, layout, new Set(), []);
		const first = held[0] as AppNode;
		const next = unlinked[0] as AppNode;
		expect(sameElement(first, next)).toBe(false);
	});

	test("a card that is no longer there drops out, a new one comes in", () => {
		const held = buildNodes(services, layout, new Set(), links);
		const c = makeService({ id: "c" });
		const grown: CanvasLayout = {
			...layout,
			nodes: { ...layout.nodes, c: { x: 800, y: 0 } },
		};
		const result = reuseNodes(
			held,
			buildNodes([a, b, c], grown, new Set(), links),
		);
		expect(result.map((node) => node.id)).toContain("c");
		expect(reuseNodes(held, buildNodes([a], layout, new Set()))).toHaveLength(
			1,
		);
	});

	test("edges: unchanged cables keep their identity, a moved card's cable is replaced", () => {
		const linked = [{ id: "l1", fromServiceId: "a", toServiceId: "b" }];
		const held = buildEdges(linked, layout, null);
		expect(reuseEdges(held, buildEdges(linked, layout, null))).toBe(held);
		const apart: CanvasLayout = {
			...layout,
			nodes: { ...layout.nodes, b: { x: 340, y: 900 } },
		};
		const moved = reuseEdges(held, buildEdges(linked, apart, null));
		expect(moved[0]?.id).toBe("l1");
	});
});
