import { afterEach, describe, expect, test } from "bun:test";
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import {
	type Edge,
	type Node,
	Position,
	ReactFlow,
	ReactFlowProvider,
} from "@xyflow/react";
import type { ServiceLink } from "#/api/models";
import { linkActions, useLinkState } from "#/features/links/store";
import { MeshCanvasProvider } from "#/features/mesh/canvas-mesh";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { MANUAL_DASH, STAGED_DASH } from "./lib/cable";
import {
	ConnectionCable,
	type LinkEdgeData,
	ServiceEdge,
} from "./service-edge";

const handle = (
	id: string,
	type: "source" | "target",
	position: Position,
	x: number,
	y: number,
) => ({
	id,
	type,
	position,
	x,
	y,
	width: 10,
	height: 10,
});

const nodes: Node[] = [
	{
		id: "a",
		position: { x: 0, y: 0 },
		width: 100,
		height: 50,
		data: {},
		handles: [handle("right", "source", Position.Right, 95, 20)],
	},
	{
		id: "b",
		position: { x: 400, y: 0 },
		width: 100,
		height: 50,
		data: {},
		handles: [handle("left", "target", Position.Left, -5, 20)],
	},
];

const LINK: ServiceLink = {
	id: "link-1",
	createdAt: "2026-01-01T00:00:00Z",
	environmentId: "env-1",
	fromServiceId: "a",
	toServiceId: "b",
	references: [],
	source: "reference",
	staged: false,
};

function edgeOf(data: Partial<LinkEdgeData>): Edge<LinkEdgeData, "link"> {
	return {
		id: "link-1",
		type: "link",
		source: "a",
		target: "b",
		sourceHandle: "right",
		targetHandle: "left",
		data: {
			kind: "reference",
			link: LINK,
			references: ["DATABASE_URL"],
			staged: false,
			inFlight: false,
			...data,
		},
	};
}

function Probe() {
	const { references } = useLinkState();
	return <output data-testid="reviewing">{references?.id ?? ""}</output>;
}

afterEach(() => {
	act(() => linkActions.closeReferences());
});

async function cable(
	data: Partial<LinkEdgeData>,
	{ mesh = false }: { mesh?: boolean } = {},
) {
	const services = mesh
		? [
				{ id: "a", server: { id: "s1", name: "web-02" } },
				{ id: "b", server: { id: "s2", name: "db-01" } },
			]
		: [];
	const links = mesh
		? [{ id: "link-1", fromServiceId: "a", toServiceId: "b" }]
		: [];
	stubApi({
		"GET /environments/:environmentId/mesh": {
			body: make("EnvironmentMeshView"),
		},
	});
	const view = await renderWithProviders(
		<>
			<div style={{ width: 800, height: 400 }}>
				<MeshCanvasProvider
					environmentId="env-1"
					services={services}
					links={links}
				>
					<ReactFlowProvider>
						<ReactFlow
							nodes={nodes}
							edges={[edgeOf(data)]}
							edgeTypes={{ link: ServiceEdge }}
							width={800}
							height={400}
						/>
					</ReactFlowProvider>
				</MeshCanvasProvider>
			</div>
			<Probe />
		</>,
	);
	const hit = await waitFor(() => {
		const el = view.container.querySelector<SVGPathElement>(
			".react-flow__edge-interaction",
		);
		if (!el) throw new Error("edge not drawn yet");
		return el;
	});
	return { ...view, hit, group: hit.closest("g") as SVGGElement };
}

function hover(el: Element, pointerType = "mouse") {
	// React's onPointerEnter is driven by pointerover.
	fireEvent.pointerOver(el, { pointerType });
}

describe("ServiceEdge", () => {
	test("draws the cable as shadow, body and highlight over one path", async () => {
		const { group } = await cable({});
		expect(group).toHaveAttribute("data-cable", "reference");
		const paths = [...group.querySelectorAll("path")].map((p) =>
			p.getAttribute("class"),
		);
		expect(paths).toEqual([
			"cable-shadow",
			"cable-body",
			"cable-hi",
			"react-flow__edge-interaction",
		]);
		const d = group.querySelector(".cable-body")?.getAttribute("d");
		expect(d).toBeTruthy();
		expect(group.querySelector(".cable-shadow")?.getAttribute("d")).toBe(d);
	});

	test("a referenced cable is solid, a staged one fine-dashed, a manual one dashed", async () => {
		const solid = await cable({});
		expect(solid.group.querySelector(".cable-body")).not.toHaveAttribute(
			"stroke-dasharray",
		);
		solid.unmount();
		const staged = await cable({ staged: true });
		expect(staged.group.querySelector(".cable-body")).toHaveAttribute(
			"stroke-dasharray",
			STAGED_DASH,
		);
		staged.unmount();
		const manual = await cable({
			kind: "manual",
			references: [],
			staged: true,
		});
		expect(manual.group.querySelector(".cable-body")).toHaveAttribute(
			"stroke-dasharray",
			MANUAL_DASH,
		);
	});

	test("a deploying target tints the cable and runs a bead (with its glow) down it", async () => {
		const quiet = await cable({ inFlight: false });
		expect(quiet.group.querySelector(".cable-bead")).toBeNull();
		expect(quiet.group.querySelector(".cable-body")).not.toHaveAttribute(
			"style",
		);
		quiet.unmount();
		const busy = await cable({ inFlight: true });
		expect(busy.group.querySelector(".cable-body")).toHaveAttribute(
			"style",
			expect.stringContaining("stroke"),
		);
		// The bead and its glow come as a pair; Motion's reduced-motion setting may switch both off.
		expect(!!busy.group.querySelector(".cable-bead")).toBe(
			!!busy.group.querySelector(".cable-bead-glow"),
		);
	});

	test("hovering a referenced cable names the variables behind it and offers a review", async () => {
		const { hit } = await cable({ references: ["DATABASE_URL", "REDIS_URL"] });
		hover(hit);
		const tip = await screen.findByRole("tooltip");
		expect(tip).toHaveTextContent("Variables DATABASE_URL, REDIS_URL");
		expect(tip).toHaveTextContent("Click to review");
		expect(tip).not.toHaveTextContent("Staged");
	});

	test("a staged cable's tooltip says it goes live after the next deploy", async () => {
		const { hit } = await cable({ staged: true });
		hover(hit);
		expect(await screen.findByRole("tooltip")).toHaveTextContent(
			"Staged: live after the next deploy",
		);
	});

	test("a hand-drawn cable's tooltip says nothing references it", async () => {
		const { hit } = await cable({ kind: "manual", references: [] });
		hover(hit);
		expect(await screen.findByRole("tooltip")).toHaveTextContent(
			"Linked by hand. No variable references it yet.",
		);
	});

	test("the tooltip goes when the pointer leaves", async () => {
		const { hit } = await cable({});
		hover(hit);
		await screen.findByRole("tooltip");
		fireEvent.pointerOut(hit, { pointerType: "mouse" });
		await waitFor(() =>
			expect(screen.queryByRole("tooltip")).not.toBeInTheDocument(),
		);
	});

	test("a touch pointer does not latch the tooltip", async () => {
		const { hit } = await cable({});
		hover(hit, "touch");
		expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
	});

	test("the derived Internet cable has no tooltip, no link and no review", async () => {
		const { hit } = await cable({
			kind: "internet",
			link: null,
			references: [],
		});
		hover(hit);
		expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
		fireEvent.click(hit);
		expect(screen.getByTestId("reviewing")).toHaveTextContent("");
		expect(hit.style.cursor).toBe("default");
	});

	test("clicking a stored link opens its references review", async () => {
		const { hit } = await cable({});
		expect(hit.style.cursor).toBe("pointer");
		fireEvent.click(hit);
		await waitFor(() =>
			expect(screen.getByTestId("reviewing")).toHaveTextContent("link-1"),
		);
	});

	test("a cable between servers wears a Mesh badge that hides while hovered", async () => {
		const { hit } = await cable({}, { mesh: true });
		const badge = await screen.findByText("Mesh");
		expect(badge.closest("[data-slot='mesh-edge-badge']")).toHaveTextContent(
			"Not connected yet",
		);
		hover(hit);
		const tip = await screen.findByRole("tooltip");
		expect(tip).toHaveTextContent("Over the mesh: web-02 → db-01");
		expect(screen.queryByText("Mesh")).not.toBeInTheDocument();
	});
});

describe("ConnectionCable", () => {
	test("draws the cable being dragged, ending in a plug at the pointer", () => {
		const { container } = render(
			<svg>
				<title>Connection cable</title>
				<ConnectionCable
					{...({
						fromX: 10,
						fromY: 20,
						toX: 300,
						toY: 120,
						fromPosition: Position.Right,
						toPosition: Position.Left,
					} as React.ComponentProps<typeof ConnectionCable>)}
				/>
			</svg>,
		);
		const plug = container.querySelector("circle");
		expect(plug).toHaveAttribute("cx", "300");
		expect(plug).toHaveAttribute("cy", "120");
		const body = container.querySelector(".cable-body");
		expect(body?.getAttribute("d")).toMatch(/^M\s?10[ ,]20/);
		expect(container.querySelector(".cable-shadow")?.getAttribute("d")).toBe(
			body?.getAttribute("d"),
		);
	});
});
