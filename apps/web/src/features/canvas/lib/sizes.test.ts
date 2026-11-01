import { describe, expect, test } from "bun:test";
import type { CanvasLayout, Service } from "#/api/models";
import {
	NODE_PROCESS_LIMIT,
	PROCESS_NODE_WIDTH,
	processCardSize,
} from "#/features/processes/model";
import { nodeSummary, stackNodeSize } from "#/features/stacks/model";
import { makeService } from "./fixtures";
import {
	arrangeSelection,
	autoLayout,
	createGroup,
	defaultSize,
	fitGroup,
	GROUP_HEADER,
	GROUP_PAD,
	mergeLayout,
	NODE_HEIGHT,
	NODE_WIDTH,
	type SizeOf,
} from "./layout";
import { sizesOf } from "./sizes";

const components = Array.from({ length: 6 }, (_, index) => ({
	name: `c${index}`,
	index,
}));
const stack = makeService({
	id: "stack",
	kind: "stack",
	config: { ...makeService().config, stack: { compose: "", components } },
});
const app = makeService({ id: "app" });
const processes = (count: number): Service["processes"] =>
	Array.from({ length: count }, (_, index) => ({
		name: index === 0 ? "web" : `p${index}`,
		default: index === 0,
		port: index === 0 ? 3000 : 0,
		command: ["run", `p${index}`],
		singleton: false,
		stopGraceSeconds: 30,
		scaling: makeService().scaling,
		observed: null,
		pending: false,
		removed: false,
	}));
const processed = makeService({ id: "processed", processes: processes(3) });

describe("sizesOf", () => {
	test("an ordinary service keeps the card size", () => {
		expect(sizesOf([app])("app")).toEqual({ w: NODE_WIDTH, h: NODE_HEIGHT });
		expect(sizesOf([app])).toBe(defaultSize);
	});

	test("a stack is as big as its rows make it", () => {
		const size = sizesOf([app, stack])("stack");
		expect(size).toEqual(stackNodeSize(nodeSummary(stack)));
		expect(size.h).toBeGreaterThan(NODE_HEIGHT);
		expect(size.w).toBeGreaterThan(NODE_WIDTH);
		expect(sizesOf([app, stack])("app")).toEqual(defaultSize("app"));
	});

	test("a service with processes is as big as its rows make it", () => {
		const size = sizesOf([app, processed])("processed");
		expect(size).toEqual(processCardSize(processed));
		expect(size.w).toBe(PROCESS_NODE_WIDTH);
		expect(size.h).toBeGreaterThan(NODE_HEIGHT - 40);
		expect(sizesOf([app, processed])("app")).toEqual(defaultSize("app"));
	});

	test("a process card grows by one row per process up to the limit", () => {
		const two = makeService({ id: "two", processes: processes(2) });
		const four = makeService({ id: "four", processes: processes(4) });
		const many = makeService({
			id: "many",
			processes: processes(NODE_PROCESS_LIMIT + 3),
		});
		const h = (service: typeof two) => sizesOf([service])(service.id).h;
		expect(h(four) - h(two)).toBe(2 * 28);
		// Past the limit the card shows the limit plus the "+N more" row.
		expect(h(many) - h(four)).toBe((NODE_PROCESS_LIMIT - 4 + 1) * 28);
	});

	test("processes win over the volume rule", () => {
		const volume = { name: "data", mountPath: "/data" };
		const both = makeService({
			id: "both",
			processes: processes(2),
			volumes: [volume, volume] as never,
		});
		expect(sizesOf([both])("both")).toEqual(processCardSize(both));
	});
});

describe("layout with a stack", () => {
	const sizeOf: SizeOf = sizesOf([app, stack]);
	const big = sizeOf("stack");

	test("a new service is placed clear of the tall stack", () => {
		const saved: CanvasLayout = {
			nodes: { stack: { x: 0, y: 0 } },
			groups: [],
			viewport: null,
		};
		const { layout } = mergeLayout(["stack", "app"], saved, sizeOf);
		const at = layout.nodes.app;
		expect(at).toBeDefined();
		const overlaps =
			(at?.x ?? 0) < big.w + 8 &&
			(at?.y ?? 0) < big.h + 8 &&
			(at?.x ?? 0) + NODE_WIDTH > -8 &&
			(at?.y ?? 0) + NODE_HEIGHT > -8;
		expect(overlaps).toBe(false);
	});

	test("a group frame wraps the stack's real size", () => {
		const group = fitGroup(
			{
				id: "g",
				name: "g",
				x: 0,
				y: 0,
				w: 0,
				h: 0,
				collapsed: false,
				serviceIds: ["stack"],
			},
			{ stack: { x: 100, y: 100 } },
			sizeOf,
		);
		expect(group.w).toBe(big.w + GROUP_PAD * 2);
		expect(group.h).toBe(big.h + GROUP_PAD * 2 + GROUP_HEADER);
		const fixed = fitGroup(group, { stack: { x: 100, y: 100 } });
		expect(fixed.h).toBeLessThan(group.h);
	});

	test("auto-layout keeps ranked nodes apart by their real heights", () => {
		const layout: CanvasLayout = {
			nodes: { stack: { x: 0, y: 0 }, app: { x: 0, y: 0 } },
			groups: [],
			viewport: null,
		};
		const arranged = autoLayout(layout, [], sizeOf);
		const a = arranged.nodes.stack;
		const b = arranged.nodes.app;
		const apart =
			(a?.x ?? 0) + big.w <= (b?.x ?? 0) ||
			(b?.x ?? 0) + NODE_WIDTH <= (a?.x ?? 0) ||
			(a?.y ?? 0) + big.h <= (b?.y ?? 0) ||
			(b?.y ?? 0) + NODE_HEIGHT <= (a?.y ?? 0);
		expect(apart).toBe(true);
	});

	test("arranging isolated cards sizes the grid to the widest and tallest", () => {
		const other = makeService({ id: "other" });
		const all = sizesOf([app, stack, other]);
		const layout: CanvasLayout = {
			nodes: {
				stack: { x: 0, y: 0 },
				app: { x: 10, y: 400 },
				other: { x: 20, y: 800 },
			},
			groups: [],
			viewport: null,
		};
		const arranged = arrangeSelection(
			layout,
			[],
			["stack", "app", "other"],
			all,
		);
		const rects = ["stack", "app", "other"].map((id) => {
			const at = arranged.nodes[id] ?? { x: 0, y: 0 };
			const size = all(id);
			return { x: at.x, y: at.y, w: size.w, h: size.h };
		});
		for (const [i, a] of rects.entries()) {
			for (const b of rects.slice(i + 1)) {
				const clear =
					a.x + a.w <= b.x ||
					b.x + b.w <= a.x ||
					a.y + a.h <= b.y ||
					b.y + b.h <= a.y;
				expect(clear).toBe(true);
			}
		}
	});

	test("createGroup fits the frame around the stack", () => {
		const layout: CanvasLayout = {
			nodes: { stack: { x: 0, y: 0 }, app: { x: 600, y: 0 } },
			groups: [],
			viewport: null,
		};
		const grouped = createGroup(layout, ["stack", "app"], "g", "Group", sizeOf);
		const frame = grouped.groups[0];
		expect(frame?.h).toBe(big.h + GROUP_PAD * 2 + GROUP_HEADER);
	});
});
