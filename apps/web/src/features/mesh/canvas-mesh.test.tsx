import { describe, expect, test } from "bun:test";
import { render, screen, waitFor } from "@testing-library/react";
import type { RouteView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import {
	MeshCanvasProvider,
	MeshEdgeBadge,
	MeshViaChip,
	useMeshEdge,
} from "./canvas-mesh";
import type { CanvasLinkInput, CanvasServiceInput } from "./model";

const web = { id: "s-web", name: "web-02" };
const db = { id: "s-db", name: "db-01" };

const services: CanvasServiceInput[] = [
	{ id: "api", server: web },
	{ id: "worker", server: web },
	{ id: "pg", server: db },
	{ id: "cache", server: db },
	{ id: "floating", server: null },
];

function route(overrides: Partial<RouteView> = {}): RouteView {
	return {
		alias: "",
		consumerId: "api",
		consumerSlug: "api",
		devicesAdded: 0,
		keys: [],
		port: 5432,
		provider: "postgres",
		providerId: "pg",
		server: { id: "s-db", name: "db-01" },
		source: "reference",
		sources: [],
		state: "online",
		...overrides,
	};
}

function Cards({ links }: { links: CanvasLinkInput[] }) {
	return (
		<>
			{links.map((l) => (
				<Edge key={l.id} id={l.id} />
			))}
			{services.map((s) => (
				<div key={s.id} data-testid={`card-${s.id}`}>
					<MeshViaChip serviceId={s.id} />
				</div>
			))}
		</>
	);
}

function Edge({ id }: { id: string }) {
	const info = useMeshEdge(id);
	return (
		<div data-testid={`edge-${id}`}>
			{info ? <MeshEdgeBadge info={info} /> : "no badge"}
		</div>
	);
}

function canvas(links: CanvasLinkInput[]) {
	return renderWithProviders(
		<MeshCanvasProvider environmentId="env-1" services={services} links={links}>
			<Cards links={links} />
		</MeshCanvasProvider>,
	);
}

const cross: CanvasLinkInput = {
	id: "l-cross",
	fromServiceId: "api",
	toServiceId: "pg",
};
const same: CanvasLinkInput = {
	id: "l-same",
	fromServiceId: "api",
	toServiceId: "worker",
};

describe("MeshCanvasProvider", () => {
	test("an environment on one server never asks for the mesh and draws no badge", async () => {
		const api = stubApi({});
		await canvas([same]);
		expect(screen.getByTestId("edge-l-same")).toHaveTextContent("no badge");
		expect(screen.queryByText("via mesh")).not.toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	test("a cable to a service with no server is not a mesh cable", async () => {
		const api = stubApi({});
		await canvas([
			{ id: "l-f", fromServiceId: "api", toServiceId: "floating" },
		]);
		expect(screen.getByTestId("edge-l-f")).toHaveTextContent("no badge");
		expect(api.calls).toEqual([]);
	});

	test("a cable between servers is badged 'Not connected yet' while the mesh loads, and with no route", async () => {
		const api = stubApi({
			"GET /environments/:id/mesh": {
				body: { grants: [], links: [], routes: [] },
			},
		});
		await canvas([cross]);
		const edge = screen.getByTestId("edge-l-cross");
		expect(edge).toHaveTextContent("Mesh");
		expect(edge).toHaveTextContent("Not connected yet");
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(api.calls[0]?.path).toBe("/environments/env-1/mesh");
		expect(edge).toHaveTextContent("Not connected yet");
		expect(screen.queryByText("via mesh")).not.toBeInTheDocument();
	});

	test("an online route badges the cable with its state and puts 'via mesh' on the consumer, naming provider and servers", async () => {
		stubApi({
			"GET /environments/:id/mesh": {
				body: { grants: [], links: [], routes: [route()] },
			},
		});
		await canvas([cross]);
		const chip = await screen.findByText("via mesh");
		expect(screen.getByTestId("edge-l-cross")).toHaveTextContent("Online");
		expect(screen.getByTestId("edge-l-cross")).not.toHaveTextContent(
			"Not connected yet",
		);
		const title =
			"Reaches postgres over the mesh (web-02 → db-01). Link online.";
		expect(chip.closest('[data-slot="mesh-via-chip"]')).toHaveAttribute(
			"title",
			title,
		);
		// Only the consumer carries the chip.
		expect(screen.getByTestId("card-api")).toContainElement(chip);
		expect(screen.getByTestId("card-pg")).not.toHaveTextContent("via mesh");
	});

	test("a route still coming up shows its state and a status icon on the chip", async () => {
		stubApi({
			"GET /environments/:id/mesh": {
				body: {
					grants: [],
					links: [],
					routes: [route({ state: "granted", devicesAdded: 1 })],
				},
			},
		});
		await canvas([cross]);
		const chip = (await screen.findByText("via mesh")).closest(
			'[data-slot="mesh-via-chip"]',
		) as HTMLElement;
		expect(screen.getByTestId("edge-l-cross")).toHaveTextContent(
			"Provisioning",
		);
		expect(chip).toHaveAttribute(
			"title",
			"Reaches postgres over the mesh (web-02 → db-01). Link provisioning.",
		);
		expect(
			chip.querySelector("svg:not([class*='lucide-network'])"),
		).not.toBeNull();
	});

	test("a route in an unknown state reads Unknown rather than healthy", async () => {
		stubApi({
			"GET /environments/:id/mesh": {
				body: { grants: [], links: [], routes: [route({ state: "weird" })] },
			},
		});
		await canvas([cross]);
		await screen.findByText("via mesh");
		expect(screen.getByTestId("edge-l-cross")).toHaveTextContent("Unknown");
	});

	test("a consumer reaching two providers shows the worse state and both providers", async () => {
		stubApi({
			"GET /environments/:id/mesh": {
				body: {
					grants: [],
					links: [],
					routes: [
						route(),
						route({
							providerId: "cache",
							provider: "redis",
							state: "offline",
						}),
					],
				},
			},
		});
		const second: CanvasLinkInput = {
			id: "l-cache",
			fromServiceId: "api",
			toServiceId: "cache",
		};
		await canvas([cross, second]);
		const chip = (await screen.findByText("via mesh")).closest(
			'[data-slot="mesh-via-chip"]',
		) as HTMLElement;
		expect(chip).toHaveAttribute(
			"title",
			"Reaches postgres, redis over the mesh (web-02 → db-01, web-02 → db-01). Link offline.",
		);
		expect(screen.getByTestId("edge-l-cross")).toHaveTextContent("Online");
		expect(screen.getByTestId("edge-l-cache")).toHaveTextContent("Offline");
	});

	test("when the mesh cannot be read the cable stays badged as not connected", async () => {
		const api = stubApi({
			"GET /environments/:id/mesh": {
				status: 500,
				body: { status: 500, title: "x", detail: "boom", code: "internal" },
			},
		});
		await canvas([cross]);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(screen.getByTestId("edge-l-cross")).toHaveTextContent(
			"Not connected yet",
		);
		expect(screen.queryByText("via mesh")).not.toBeInTheDocument();
	});
});

describe("MeshEdgeBadge", () => {
	test("says the state in words, and a pending cable 'Not connected yet'", () => {
		const { unmount } = render(
			<MeshEdgeBadge
				info={{
					label: "a → b",
					state: "degraded",
					devicesAdded: 0,
					purpose: "service",
				}}
			/>,
		);
		expect(screen.getByText("Degraded")).toBeInTheDocument();
		unmount();
		render(
			<MeshEdgeBadge
				info={{
					label: "a → b",
					state: "pending",
					devicesAdded: 1,
					purpose: "service",
				}}
			/>,
		);
		expect(screen.getByText("Not connected yet")).toBeInTheDocument();
	});
});

describe("MeshViaChip", () => {
	test("renders nothing outside a provider", () => {
		const { container } = render(<MeshViaChip serviceId="api" />);
		expect(container).toBeEmptyDOMElement();
	});
});
