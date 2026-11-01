import { describe, expect, test } from "bun:test";
import { type CanvasLayout, PLAIN_DOMAIN_RULE } from "#/api/models";
import { makeService } from "./fixtures";
import {
	buildEdges,
	buildNodes,
	edgeLabel,
	groupNodeId,
	layoutFromNodes,
} from "./flow";
import { createGroup, toggleGroup } from "./layout";

const a = makeService({ id: "a" });
const b = makeService({ id: "b" });
const c = makeService({ id: "c" });
const services = [a, b, c];

const base: CanvasLayout = {
	nodes: { a: { x: 0, y: 0 }, b: { x: 340, y: 100 }, c: { x: 800, y: 0 } },
	groups: [],
	viewport: null,
};

describe("buildNodes / layoutFromNodes", () => {
	test("ungrouped nodes round-trip exactly", () => {
		const nodes = buildNodes(services, base, new Set());
		expect(layoutFromNodes(base, nodes).nodes).toEqual(base.nodes);
	});

	test("members sit relative to their group and round-trip to absolute", () => {
		const layout = createGroup(base, ["a", "b"], "g1", "Pair");
		const nodes = buildNodes(services, layout, new Set());
		const group = nodes.find((n) => n.id === groupNodeId("g1"));
		const member = nodes.find((n) => n.id === "b");
		expect(nodes[0]?.id).toBe(groupNodeId("g1"));
		expect(member?.parentId).toBe(groupNodeId("g1"));
		expect(member?.position.x).toBe(340 - (group?.position.x ?? 0));
		expect(layoutFromNodes(layout, nodes).nodes).toEqual(base.nodes);
	});

	test("dragging a group moves its members", () => {
		const layout = createGroup(base, ["a", "b"], "g1", "Pair");
		const nodes = buildNodes(services, layout, new Set()).map((n) =>
			n.id === groupNodeId("g1")
				? { ...n, position: { x: n.position.x + 100, y: n.position.y + 50 } }
				: n,
		);
		const next = layoutFromNodes(layout, nodes);
		expect(next.nodes.a).toEqual({ x: 100, y: 50 });
		expect(next.nodes.b).toEqual({ x: 440, y: 150 });
		expect(next.nodes.c).toEqual(base.nodes.c ?? { x: 0, y: 0 });
	});

	test("a collapsed group hides its members", () => {
		const layout = toggleGroup(
			createGroup(base, ["a", "b"], "g1", "Pair"),
			"g1",
		);
		const nodes = buildNodes(services, layout, new Set());
		expect(nodes.find((n) => n.id === "a")?.hidden).toBe(true);
		expect(nodes.find((n) => n.id === "c")?.hidden).toBeUndefined();
	});
});

describe("buildEdges", () => {
	const links = [
		{ id: "l1", fromServiceId: "a", toServiceId: "b" },
		{ id: "l2", fromServiceId: "a", toServiceId: "c" },
		{ id: "l3", fromServiceId: "b", toServiceId: "c" },
	];

	test("one edge per link when nothing is grouped", () => {
		expect(buildEdges(links, base, null).map((e) => e.id)).toEqual([
			"l1",
			"l2",
			"l3",
		]);
	});

	test("collapsed groups absorb inner edges and merge parallel ones", () => {
		const layout = toggleGroup(
			createGroup(base, ["a", "b"], "g1", "Pair"),
			"g1",
		);
		const edges = buildEdges(links, layout, null);
		expect(edges).toHaveLength(1);
		expect(edges[0]?.source).toBe(groupNodeId("g1"));
		expect(edges[0]?.target).toBe("c");
	});

	test("a reference edge is staged only while every reference is", () => {
		const reference = (id: string, staged: boolean) => ({
			id,
			fromServiceId: "a",
			toServiceId: "b",
			source: "reference" as const,
			references: [{ key: "DATABASE_URL", staged }],
			staged,
			createdAt: "2026-01-01T00:00:00Z",
			environmentId: "env-1",
		});
		const [stitched] = buildEdges([reference("ref:a:b", true)], base, null);
		expect(stitched?.data?.staged).toBe(true);
		expect(stitched?.data?.kind).toBe("reference");
		const [live] = buildEdges([reference("ref:a:b", false)], base, null);
		expect(live?.data?.staged).toBe(false);
		// A link without the flag (hand-drawn, or not yet loaded) is never staged.
		const [drawn] = buildEdges(links.slice(0, 1), base, null);
		expect(drawn?.data?.staged).toBe(false);
	});

	test("an edge to a non-matching service is dimmed", () => {
		const edges = buildEdges(links, base, new Set(["a", "b"]));
		expect(edges.find((e) => e.id === "l1")?.style).toBeUndefined();
		expect(edges.find((e) => e.id === "l2")?.style?.opacity).toBe(0.25);
	});
});

describe("plugs and the Internet node", () => {
	const exposed = makeService({
		id: "web",
		domains: [
			{
				...PLAIN_DOMAIN_RULE,
				id: "d1",
				hostname: "shop.acme.dev",
				targetPort: 80,
				component: "",
				process: "",
				generated: false,
				status: "active",
				statusMessage: null,
				dnsOk: true,
				dnsCheckedAt: null,
			},
		],
	});
	const db = makeService({ id: "db" });
	const layout: CanvasLayout = {
		nodes: { web: { x: 0, y: 300 }, db: { x: 400, y: 300 } },
		groups: [],
		viewport: null,
	};

	test("a jack holds a plug when a link ends there", () => {
		const nodes = buildNodes([exposed, db], layout, new Set(), [
			{ fromServiceId: "web", toServiceId: "db" },
		]);
		const web = nodes.find((n) => n.id === "web");
		const dbNode = nodes.find((n) => n.id === "db");
		// web → db runs right → left; the Internet cable plugs web's own top jack.
		expect(web?.type === "service" && web.data.plugs).toEqual({
			left: false,
			right: true,
			top: true,
		});
		expect(dbNode?.type === "service" && dbNode.data.plugs).toEqual({
			left: true,
			right: false,
			top: false,
		});
	});

	test("a link to a card on the left leaves by the left jack and never loops", () => {
		const edges = buildEdges(
			[{ id: "back", fromServiceId: "db", toServiceId: "web" }],
			layout,
			null,
		);
		expect(edges[0]?.sourceHandle).toBe("left");
		expect(edges[0]?.targetHandle).toBe("right");
		const forward = buildEdges(
			[{ id: "fwd", fromServiceId: "web", toServiceId: "db" }],
			layout,
			null,
		);
		expect(forward[0]?.sourceHandle).toBe("right");
		expect(forward[0]?.targetHandle).toBe("left");
	});

	test("an exposed environment gets a fixed Internet node, last, above the cards", () => {
		const nodes = buildNodes([exposed, db], layout, new Set());
		const internet = nodes.at(-1);
		expect(internet?.type).toBe("internet");
		expect(internet?.draggable).toBe(false);
		expect(internet?.selectable).toBe(false);
		expect(internet && internet.position.y < 300).toBe(true);
		expect(
			buildNodes(
				[db],
				{ ...layout, nodes: { db: { x: 0, y: 0 } } },
				new Set(),
			).some((n) => n.type === "internet"),
		).toBe(false);
	});

	test("edges: reference links are solid, manual-only dashed, the Internet one derived", () => {
		const edges = buildEdges(
			[
				{
					id: "ref:web:db",
					fromServiceId: "web",
					toServiceId: "db",
					source: "reference",
					references: [{ key: "DATABASE_URL", staged: false }],
					staged: false,
					createdAt: "2026-01-01T00:00:00Z",
					environmentId: "env-1",
				},
				{ id: "m1", fromServiceId: "db", toServiceId: "web" },
			],
			layout,
			null,
			{ internetIds: ["web"], inFlightIds: new Set(["db"]) },
		);
		const ref = edges.find((e) => e.id === "ref:web:db");
		expect(ref?.data?.kind).toBe("reference");
		expect(ref?.data?.references).toEqual(["DATABASE_URL"]);
		expect(ref?.data?.link?.id).toBe("ref:web:db");
		expect(ref?.data?.inFlight).toBe(true);
		const manual = edges.find((e) => e.id === "m1");
		expect(manual?.data?.kind).toBe("manual");
		expect(manual?.data?.link).toBeNull();
		const internet = edges.find((e) => e.id === "internet:web");
		expect(internet?.source).toBe("internet:public");
		expect(internet?.data?.kind).toBe("internet");
		// Internet cables drop into the card's own top jack, whatever side it is on.
		expect(internet?.sourceHandle).toBe("bottom");
		expect(internet?.targetHandle).toBe("top");
	});
});

describe("cables for the keyboard", () => {
	const names: Record<string, string> = {
		a: "web",
		b: "postgres",
		c: "api",
		"internet:public": "Internet",
	};
	const nameOf = (id: string) => names[id] ?? id;
	const ref = (id: string, staged: boolean) => ({
		id,
		fromServiceId: "a",
		toServiceId: "b",
		source: "reference" as const,
		references: [{ key: "DATABASE_URL", staged }],
		staged,
		createdAt: "2026-01-01T00:00:00Z",
		environmentId: "env-1",
	});

	test("edgeLabel names the ends, and says so when a cable is not the plain kind", () => {
		expect(edgeLabel("web", "postgres", "reference", false)).toBe(
			"web → postgres",
		);
		expect(edgeLabel("web", "postgres", "reference", true)).toBe(
			"web → postgres, staged",
		);
		expect(edgeLabel("web", "api", "manual", false)).toBe(
			"web → api, linked by hand",
		);
		expect(edgeLabel("Internet", "web", "internet", false)).toBe(
			"Internet → web",
		);
	});

	test("a cable is labelled with service names, never ids, and is no tab stop", () => {
		const edges = buildEdges(
			[ref("r1", false), { id: "m1", fromServiceId: "a", toServiceId: "c" }],
			base,
			null,
			{ nameOf, internetIds: ["a"] },
		);
		const byId = new Map(edges.map((edge) => [edge.id, edge]));
		expect(byId.get("r1")?.ariaLabel).toBe("web → postgres");
		expect(byId.get("m1")?.ariaLabel).toBe("web → api, linked by hand");
		expect(byId.get("internet:a")?.ariaLabel).toBe("Internet → web");
		expect(edges.every((edge) => edge.focusable === false)).toBe(true);
	});

	test("a collapsed group's cable is named for the group", () => {
		const layout = toggleGroup(
			createGroup(base, ["a", "c"], "g1", "Pair"),
			"g1",
		);
		const [edge] = buildEdges([ref("r1", true)], layout, null, {
			nameOf: (id) => (id === groupNodeId("g1") ? "Pair" : nameOf(id)),
		});
		expect(edge?.ariaLabel).toBe("Pair → postgres, staged");
	});

	test("the Internet node is a tab stop: it is not marked unfocusable", () => {
		const exposed = makeService({
			id: "w",
			domains: [
				{
					...PLAIN_DOMAIN_RULE,
					id: "d1",
					hostname: "shop.acme.dev",
					targetPort: 80,
					component: "",
					process: "",
					generated: false,
					status: "active",
					statusMessage: null,
					dnsOk: true,
					dnsCheckedAt: null,
				},
			],
		});
		const nodes = buildNodes(
			[exposed],
			{ nodes: { w: { x: 0, y: 0 } }, groups: [], viewport: null },
			new Set(),
		);
		const internet = nodes.find((node) => node.type === "internet");
		expect(internet).toBeDefined();
		expect(internet?.focusable).not.toBe(false);
		// Still no selection: a click on it is not a card.
		expect(internet?.selectable).toBe(false);
	});
});
