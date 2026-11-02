import { describe, expect, it } from "bun:test";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { makeService } from "#/features/canvas/lib/fixtures";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { ServiceNetworks } from "./service-networks";

const NETWORKS = {
	items: [
		{
			id: "n1",
			name: "backend",
			serverId: "server-1",
			serverName: "hetzner-01",
			dockerName: "nc-backend",
			createdAt: "2026-01-01T00:00:00Z",
			services: [],
		},
		{
			id: "n2",
			name: "data",
			serverId: "server-1",
			serverName: "hetzner-01",
			dockerName: "nc-data",
			createdAt: "2026-01-01T00:00:00Z",
			services: [],
		},
	],
};

const placed = (over = {}) =>
	makeService({
		id: "svc-1",
		serverId: "server-1",
		networks: [
			{ id: "n1", name: "backend", alias: "api.backend", serverId: "server-1" },
		],
		...over,
	});

describe("ServiceNetworks", () => {
	it("lists the server's networks with the attached ones checked and their hostname shown", async () => {
		const api = stubApi({ "GET /networks": { body: NETWORKS } });
		await renderWithProviders(<ServiceNetworks service={placed()} />);
		expect(
			await screen.findByRole("checkbox", { name: "backend" }),
		).toBeChecked();
		expect(screen.getByRole("checkbox", { name: "data" })).not.toBeChecked();
		expect(screen.getByText("api.backend")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Copy api.backend" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("link", { name: "Manage private networks" }),
		).toHaveAttribute("href", "/settings/private-networks");
		expect(api.calls[0]?.query.get("serverId")).toBe("server-1");
	});

	it("shows an empty note when the server has no network", async () => {
		stubApi({ "GET /networks": { body: { items: [] } } });
		await renderWithProviders(<ServiceNetworks service={placed()} />);
		expect(
			await screen.findByText(/No shared network on hetzner-01 yet/),
		).toBeInTheDocument();
	});

	it("attaches a network (staged) and says so", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"GET /networks": { body: NETWORKS },
			"PUT /services/:id/networks/:networkId": { body: make("NetworkRef", {}) },
		});
		await renderWithProviders(<ServiceNetworks service={placed()} />);
		await user.click(await screen.findByRole("checkbox", { name: "data" }));
		expect(await screen.findByText("Attached to data")).toBeInTheDocument();
		expect(
			screen.getByText("Staged until the next deploy."),
		).toBeInTheDocument();
		const put = api.calls.find((c) => c.method === "PUT");
		expect(put?.path).toBe("/services/svc-1/networks/n2");
	});

	it("detaches an attached network", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"GET /networks": { body: NETWORKS },
			"DELETE /services/:id/networks/:networkId": { status: 204 },
		});
		await renderWithProviders(<ServiceNetworks service={placed()} />);
		await user.click(await screen.findByRole("checkbox", { name: "backend" }));
		expect(
			await screen.findByText("Detached from backend"),
		).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "DELETE")?.path).toBe(
			"/services/svc-1/networks/n1",
		);
	});

	it("shows the server's reason when the change is refused", async () => {
		const user = userEvent.setup();
		stubApi({
			"GET /networks": { body: NETWORKS },
			"PUT /services/:id/networks/:networkId": problem(422, {
				title: "Unprocessable",
				status: 422,
				detail: "Wrong server",
			}),
		});
		await renderWithProviders(<ServiceNetworks service={placed()} />);
		await user.click(await screen.findByRole("checkbox", { name: "data" }));
		expect(
			await screen.findByText("Could not change the shared network"),
		).toBeInTheDocument();
		expect(screen.getByText("Wrong server")).toBeInTheDocument();
	});

	it.each([
		[
			"a service that blocks egress",
			{ config: { ...makeService().config, blockEgress: true } },
			/Allow outbound internet first/,
		],
		[
			"a service with no server",
			{ serverId: null, server: null },
			/Place the service on a server first/,
		],
		["a route", { kind: "route" as const }, /A route has no container/],
	])(
		"explains why %s cannot join and offers no checkboxes",
		async (_name, over, note) => {
			stubApi({ "GET /networks": { body: NETWORKS } });
			await renderWithProviders(<ServiceNetworks service={placed(over)} />);
			expect(await screen.findByRole("note")).toHaveTextContent(note);
			expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
		},
	);

	it.each([
		[
			"a service that blocks egress",
			{ config: { ...makeService().config, blockEgress: true } },
		],
		["a service with no server", { serverId: null, server: null }],
		["a route", { kind: "route" as const }],
	])("does not ask for networks for %s", async (_name, over) => {
		const api = stubApi({ "GET /networks": { body: NETWORKS } });
		await renderWithProviders(<ServiceNetworks service={placed(over)} />);
		await screen.findByRole("note");
		expect(api.calls).toEqual([]);
	});
});
