import { describe, expect, test } from "bun:test";
import type { CanvasLayout, Point, Service } from "#/api/models";
import { PLAIN_DOMAIN_RULE } from "#/api/models";
import { makeService } from "./fixtures";
import {
	INTERNET_HEIGHT,
	INTERNET_ID,
	INTERNET_WIDTH,
	internetPosition,
	internetSummary,
} from "./internet";
import { GRID } from "./layered";
import {
	autoLayout,
	blendLayout,
	CLUSTER_GAP,
	GROUP_HEADER,
	GROUP_PAD,
	NODE_HEIGHT,
	NODE_WIDTH,
	type Size,
} from "./layout";
import { layoutTraits, sizesOf } from "./sizes";

// A typical canvas: a Docmost group (app with a storage
// volume, Postgres 17, Valkey), five ungrouped example services and one
// Internet node with two public domains.
function domain(hostname: string): Service["domains"][number] {
	return {
		...PLAIN_DOMAIN_RULE,
		id: `d-${hostname}`,
		hostname,
		targetPort: 80,
		component: "",
		process: "",
		generated: false,
		status: "active",
		statusMessage: null,
		dnsOk: null,
		dnsCheckedAt: null,
	};
}

const services: Service[] = [
	makeService({
		id: "example-web",
		name: "example-web",
		kind: "app",
		domains: [domain("example.com")],
	}),
	makeService({ id: "example-worker", name: "example-worker", kind: "worker" }),
	makeService({ id: "example-ssr", name: "example-ssr", kind: "app" }),
	makeService({
		id: "example-postgres",
		name: "example-postgres",
		kind: "database",
	}),
	makeService({
		id: "example-valkey",
		name: "example-valkey",
		kind: "database",
	}),

	makeService({
		id: "docmost",
		name: "Docmost",
		kind: "app",
		domains: [domain("wiki.example.com")],
		volumes: [
			{
				id: "v1",
				name: "storage",
				mountPath: "/app/data/storage",
			} as Service["volumes"][number],
		],
	}),
	makeService({ id: "docmost-db", name: "Docmost DB", kind: "database" }),
	makeService({
		id: "docmost-valkey",
		name: "Docmost Valkey",
		kind: "database",
	}),
];

// The consumer is `from`.
const links = [
	["docmost", "docmost-db"],
	["docmost", "docmost-valkey"],
	["example-web", "example-ssr"],
	["example-web", "example-postgres"],
	["example-web", "example-valkey"],
	["example-worker", "example-postgres"],
	["example-worker", "example-valkey"],
].map(([fromServiceId, toServiceId]) => ({
	fromServiceId: fromServiceId as string,
	toServiceId: toServiceId as string,
}));

const DATA = [
	"docmost-db",
	"docmost-valkey",
	"example-postgres",
	"example-valkey",
];
const GROUP_IDS = ["docmost", "docmost-db", "docmost-valkey"];

// Where the left-to-right dagre Arrange left this graph, with the Internet node
// a cable's length above the topmost service it feeds: inside the Docmost frame.
const BEFORE: CanvasLayout = {
	nodes: {
		docmost: { x: 24, y: 192 },
		"docmost-db": { x: 432, y: 320 },
		"docmost-valkey": { x: 432, y: 64 },
		"example-web": { x: 228, y: 866 },
		"example-worker": { x: 228, y: 600 },
		"example-ssr": { x: 864, y: 994 },
		"example-postgres": { x: 864, y: 738 },
		"example-valkey": { x: 864, y: 482 },
	},
	groups: [
		{
			id: "docmost-group",
			name: "Docmost",
			x: 0,
			y: 0,
			w: 744,
			h: 520,
			collapsed: false,
			serviceIds: GROUP_IDS,
		},
	],
	viewport: null,
};
const BEFORE_INTERNET: Point = { x: 110, y: -4 };

function scattered(): CanvasLayout {
	return {
		nodes: Object.fromEntries(
			services.map((s, i) => [
				s.id,
				{ x: (i % 3) * 20, y: Math.floor(i / 3) * 20 },
			]),
		),
		groups: [
			{
				id: "docmost-group",
				name: "Docmost",
				x: 0,
				y: 0,
				w: 100,
				h: 100,
				collapsed: false,
				serviceIds: GROUP_IDS,
			},
		],
		viewport: null,
	};
}

const sizeOf = sizesOf(services);
const traits = layoutTraits(services);
const arranged = autoLayout(scattered(), links, sizeOf, traits);
const internetAt = internetPosition(
	arranged,
	internetSummary(services)?.serviceIds ?? [],
);

interface Box {
	id: string;
	x: number;
	y: number;
	w: number;
	h: number;
}

function boxesOf(layout: CanvasLayout): Box[] {
	return Object.entries(layout.nodes).map(([id, at]) => {
		const size = sizeOf(id);
		return { id, x: at.x, y: at.y, w: size.w, h: size.h };
	});
}

function apart(a: Box, b: Box): boolean {
	return (
		a.x + a.w <= b.x || b.x + b.w <= a.x || a.y + a.h <= b.y || b.y + b.h <= a.y
	);
}

type Segment = readonly [Point, Point];

/** A straight stand-in for a cable: right jack of the consumer to left jack of the provider; Internet bottom to card top. */
function cables(layout: CanvasLayout, internet: Point | null): Segment[] {
	const out: Segment[] = [];
	const at = (id: string): Box => {
		const found = boxesOf(layout).find((b) => b.id === id);
		if (!found) throw new Error(id);
		return found;
	};
	for (const link of links) {
		const a = at(link.fromServiceId);
		const b = at(link.toServiceId);
		const [from, to] =
			b.x >= a.x
				? [
						{ x: a.x + a.w, y: a.y + a.h / 2 },
						{ x: b.x, y: b.y + b.h / 2 },
					]
				: [
						{ x: a.x, y: a.y + a.h / 2 },
						{ x: b.x + b.w, y: b.y + b.h / 2 },
					];
		out.push([from, to]);
	}
	if (internet) {
		const from = {
			x: internet.x + INTERNET_WIDTH / 2,
			y: internet.y + INTERNET_HEIGHT,
		};
		for (const id of internetSummary(services)?.serviceIds ?? []) {
			const b = at(id);
			out.push([from, { x: b.x + b.w / 2, y: b.y }]);
		}
	}
	return out;
}

const cross = (o: Point, a: Point, b: Point) =>
	(a.x - o.x) * (b.y - o.y) - (a.y - o.y) * (b.x - o.x);

function sameEnd(p: Point, q: Point) {
	return p.x === q.x && p.y === q.y;
}

/** Proper crossings between cables (cables that share a jack do not count). */
export function crossingCount(segments: readonly Segment[]): number {
	let count = 0;
	for (let i = 0; i < segments.length; i++) {
		const [a, b] = segments[i] as Segment;
		for (let j = i + 1; j < segments.length; j++) {
			const [c, d] = segments[j] as Segment;
			if (sameEnd(a, c) || sameEnd(a, d) || sameEnd(b, c) || sameEnd(b, d)) {
				continue;
			}
			if (
				cross(a, b, c) * cross(a, b, d) < 0 &&
				cross(c, d, a) * cross(c, d, b) < 0
			) {
				count += 1;
			}
		}
	}
	return count;
}

interface Frame {
	x: number;
	y: number;
	w: number;
	h: number;
}

/** Whether a straight cable passes through a rectangle (touching an edge does not count). */
function runsThrough([a, b]: Segment, r: Frame): boolean {
	let enter = 0;
	let leave = 1;
	for (const [from, to, lo, hi] of [
		[a.x, b.x, r.x, r.x + r.w],
		[a.y, b.y, r.y, r.y + r.h],
	] as const) {
		const d = to - from;
		if (d === 0) {
			if (from <= lo || from >= hi) return false;
			continue;
		}
		const t1 = (lo - from) / d;
		const t2 = (hi - from) / d;
		enter = Math.max(enter, Math.min(t1, t2));
		leave = Math.min(leave, Math.max(t1, t2));
	}
	return enter < leave - 1e-9;
}

/** Cables through a card that is not at one of their ends, or across a group frame holding neither end. */
function cableHits(layout: CanvasLayout, internet: Point): number {
	const ends = [
		...links.map((l) => [l.fromServiceId, l.toServiceId] as const),
		...(internetSummary(services)?.serviceIds ?? []).map(
			(id) => [INTERNET_ID, id] as const,
		),
	];
	const segments = cables(layout, internet);
	let hits = 0;
	for (const [i, segment] of segments.entries()) {
		const [from, to] = ends[i] as readonly [string, string];
		for (const box of boxesOf(layout)) {
			if (box.id !== from && box.id !== to && runsThrough(segment, box)) hits++;
		}
		for (const group of layout.groups) {
			const holds = (id: string) => group.serviceIds.includes(id);
			if (!holds(from) && !holds(to) && runsThrough(segment, group)) hits++;
		}
	}
	return hits;
}

describe("autoLayout on a typical canvas", () => {
	test("no two cards overlap", () => {
		const boxes = boxesOf(arranged);
		for (const [i, a] of boxes.entries()) {
			for (const b of boxes.slice(i + 1)) expect(apart(a, b)).toBe(true);
		}
	});

	test("the Internet node is above every card and frame, outside every group", () => {
		const top = internetAt.y + INTERNET_HEIGHT;
		for (const box of boxesOf(arranged)) expect(top).toBeLessThanOrEqual(box.y);
		for (const group of arranged.groups) {
			expect(top).toBeLessThanOrEqual(group.y);
			const inside =
				internetAt.x < group.x + group.w &&
				internetAt.x + INTERNET_WIDTH > group.x &&
				internetAt.y < group.y + group.h &&
				internetAt.y + INTERNET_HEIGHT > group.y;
			expect(inside).toBe(false);
		}
		expect(INTERNET_ID).toBe("internet:public");
	});

	test("the group frame holds its members and nothing else", () => {
		const group = arranged.groups[0];
		expect(group).toBeDefined();
		if (!group) return;
		const boxes = boxesOf(arranged);
		const members = boxes.filter((b) => GROUP_IDS.includes(b.id));
		const left = Math.min(...members.map((b) => b.x));
		const top = Math.min(...members.map((b) => b.y));
		expect(group.x).toBe(left - GROUP_PAD);
		expect(group.y).toBe(top - GROUP_PAD - GROUP_HEADER);
		expect(group.x + group.w).toBe(
			Math.max(...members.map((b) => b.x + b.w)) + GROUP_PAD,
		);
		expect(group.y + group.h).toBe(
			Math.max(...members.map((b) => b.y + b.h)) + GROUP_PAD,
		);
		for (const other of boxes.filter((b) => !GROUP_IDS.includes(b.id))) {
			const inside =
				other.x < group.x + group.w &&
				other.x + other.w > group.x &&
				other.y < group.y + group.h &&
				other.y + other.h > group.y;
			expect(inside).toBe(false);
		}
	});

	test("every data node sits in a column right of the apps that use it", () => {
		for (const link of links) {
			const user = arranged.nodes[link.fromServiceId] as Point;
			const data = arranged.nodes[link.toServiceId] as Point;
			expect(data.x).toBeGreaterThanOrEqual(user.x + NODE_WIDTH);
		}
		// ... and all data of one stack stands in the same last column.
		for (const ids of [
			["docmost-db", "docmost-valkey"],
			["example-postgres", "example-valkey"],
		]) {
			expect(new Set(ids.map((id) => arranged.nodes[id]?.x)).size).toBe(1);
		}
		expect(DATA.length).toBe(4);
	});

	test("the two stacks are clusters side by side, ordered by domain", () => {
		const web = arranged.nodes["example-web"] as Point;
		const group = arranged.groups[0];
		expect(group).toBeDefined();
		if (!group) return;
		const example = boxesOf(arranged).filter((b) => b.id.startsWith("example"));
		const right = Math.max(...example.map((b) => b.x + b.w));
		// example.com comes before wiki.example.com: its cluster is on the left.
		expect(web.x).toBeLessThan(group.x);
		expect(group.x - right).toBeGreaterThanOrEqual(CLUSTER_GAP);
	});

	test("every anchor sits on the lattice and rows keep equal gaps", () => {
		for (const [id, at] of Object.entries(arranged.nodes)) {
			expect({ id, x: at.x % GRID }).toEqual({ id, x: 0 });
			expect({ id, y: at.y % GRID }).toEqual({ id, y: 0 });
		}
		const column = (xs: string[]) =>
			xs.map((id) => arranged.nodes[id] as Point).sort((a, b) => a.y - b.y);
		const data = column(["example-postgres", "example-valkey"]);
		expect(data).toHaveLength(2);
	});

	test("cables cross at most twice (and no more than before)", () => {
		const now = crossingCount(cables(arranged, internetAt));
		const before = crossingCount(cables(BEFORE, BEFORE_INTERNET));
		expect(now).toBeLessThanOrEqual(2);
		expect(now).toBeLessThanOrEqual(before);
	});

	test("no cable runs through a card or across a group it does not belong to", () => {
		const before = cableHits(BEFORE, BEFORE_INTERNET);
		const now = cableHits(arranged, internetAt);
		expect(before).toBeGreaterThan(0);
		expect(now).toBe(0);
	});

	test("the same graph gives the same layout, whatever it started from", () => {
		const again = autoLayout(scattered(), links, sizeOf, traits);
		expect(again).toEqual(arranged);
		const reversed = autoLayout(
			{
				...BEFORE,
				groups: BEFORE.groups.map((g) => ({
					...g,
					serviceIds: [...GROUP_IDS].reverse(),
				})),
			},
			[...links].reverse(),
			sizeOf,
			traits,
		);
		expect(reversed.nodes).toEqual(arranged.nodes);
		expect(reversed.groups[0]?.x).toBe(arranged.groups[0]?.x);
		expect(autoLayout(arranged, links, sizeOf, traits)).toEqual(arranged);
	});
});

describe("autoLayout details", () => {
	test("a card with several volumes counts them in its height", () => {
		const volume = (n: number) =>
			({
				id: `v${n}`,
				name: `v${n}`,
				mountPath: `/v${n}`,
			}) as Service["volumes"][number];
		const fat = makeService({
			id: "fat",
			volumes: [volume(1), volume(2), volume(3)],
		});
		const size: Size = sizesOf([fat])("fat");
		expect(size.h).toBeGreaterThan(NODE_HEIGHT);
		const lean = makeService({ id: "lean", volumes: [volume(1)] });
		expect(sizesOf([lean])("lean").h).toBe(NODE_HEIGHT);
	});

	test("a cycle of links still lays out left to right without overlap", () => {
		const layout: CanvasLayout = {
			nodes: { a: { x: 0, y: 0 }, b: { x: 0, y: 0 }, c: { x: 0, y: 0 } },
			groups: [],
			viewport: null,
		};
		const next = autoLayout(layout, [
			{ fromServiceId: "a", toServiceId: "b" },
			{ fromServiceId: "b", toServiceId: "c" },
			{ fromServiceId: "c", toServiceId: "a" },
		]);
		const boxes = Object.entries(next.nodes).map(([id, at]) => ({
			id,
			x: at.x,
			y: at.y,
			w: NODE_WIDTH,
			h: NODE_HEIGHT,
		}));
		for (const [i, a] of boxes.entries()) {
			for (const b of boxes.slice(i + 1)) expect(apart(a, b)).toBe(true);
		}
	});

	test("services with no link share a grid after the linked clusters", () => {
		const names = ["a", "b", "c", "d", "e"];
		const layout: CanvasLayout = {
			nodes: Object.fromEntries(names.map((n) => [n, { x: 0, y: 0 }])),
			groups: [],
			viewport: null,
		};
		const next = autoLayout(layout, [{ fromServiceId: "a", toServiceId: "b" }]);
		const loose = ["c", "d", "e"].map((id) => next.nodes[id] as Point);
		const a = next.nodes.a as Point;
		for (const at of loose) expect(at.x).toBeGreaterThan(a.x + NODE_WIDTH);
		expect(new Set(loose.map((p) => p.x)).size).toBe(2);
	});

	test("blendLayout glides positions and frame sizes", () => {
		const from = scattered();
		const half = blendLayout(from, arranged, 0.5);
		const start = from.nodes["example-web"] as Point;
		const end = arranged.nodes["example-web"] as Point;
		const mid = half.nodes["example-web"] as Point;
		expect(mid.x).toBeCloseTo((start.x + end.x) / 2);
		expect(blendLayout(from, arranged, 1)).toEqual(arranged);
		expect(blendLayout(from, arranged, 0).nodes).toEqual(from.nodes);
	});
});
