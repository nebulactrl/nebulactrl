import { describe, expect, it } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { NetworksSection } from "./networks-section";

const attached = {
	alias: "api.backend",
	envName: "Production",
	envSlug: "production",
	environmentId: "e1",
	kind: "app",
	name: "api",
	projectId: "p1",
	projectName: "Shop",
	serviceId: "svc-1",
	slug: "api",
};

const network = (over = {}) => ({
	id: "n1",
	name: "backend",
	serverId: "srv-1",
	serverName: "hetzner-01",
	dockerName: "nc-backend",
	createdAt: "2026-01-01T00:00:00Z",
	services: [],
	...over,
});

const me = (scopes: string[]) => ({
	"GET /me": {
		body: {
			createdAt: "2026-01-01T00:00:00Z",
			email: "a@example.com",
			id: "u1",
			instanceAdmin: false,
			name: "Ada",
			principal: { label: "Ada", role: "admin", scopes, type: "user" },
			workspaceId: "w1",
		},
	},
});

describe("NetworksSection", () => {
	it("explains shared networks and offers New network to a manager when there are none", async () => {
		stubApi({ "GET /networks": { body: { items: [] } }, ...me(["admin"]) });
		await renderWithProviders(<NetworksSection />);
		expect(await screen.findByText("No shared networks")).toBeInTheDocument();
		expect(
			await screen.findByRole("button", { name: "New network" }),
		).toBeInTheDocument();
	});

	it("offers no New network or Delete without the servers:write or admin scope", async () => {
		stubApi({
			"GET /networks": { body: { items: [network()] } },
			...me(["services:read"]),
		});
		await renderWithProviders(<NetworksSection />);
		expect(await screen.findByText("backend")).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "New network" }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Delete" }),
		).not.toBeInTheDocument();
	});

	it("lets a servers:write token manage networks", async () => {
		stubApi({
			"GET /networks": { body: { items: [network()] } },
			...me(["servers:write"]),
		});
		await renderWithProviders(<NetworksSection />);
		expect(
			await screen.findByRole("button", { name: "New network" }),
		).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Delete" })).toBeInTheDocument();
	});

	it("lists each network with its server and the services attached, linking to both", async () => {
		stubApi({
			"GET /networks": {
				body: {
					items: [
						network({ services: [attached] }),
						network({ id: "n2", name: "data", dockerName: "nc-data" }),
					],
				},
			},
			...me(["admin"]),
		});
		await renderWithProviders(<NetworksSection />);
		const first = (await screen.findByText("backend")).closest(
			"li",
		) as HTMLElement;
		expect(
			within(first).getByRole("link", { name: /hetzner-01/ }),
		).toHaveAttribute("href", "/servers/srv-1");
		expect(within(first).getByText("nc-backend")).toBeInTheDocument();
		expect(within(first).getByRole("link", { name: "api" })).toHaveAttribute(
			"href",
			"/p/p1/production?service=svc-1",
		);
		expect(within(first).getByText("Shop · Production")).toBeInTheDocument();
		expect(within(first).getByText("api.backend")).toBeInTheDocument();
		const second = screen.getByText("data").closest("li") as HTMLElement;
		expect(
			within(second).getByText("No service attached yet"),
		).toBeInTheDocument();
	});

	it("shows an error with retry when the networks do not load", async () => {
		const user = userEvent.setup();
		let fail = true;
		stubApi({
			"GET /networks": () =>
				fail
					? Response.json({ title: "Down", status: 500 }, { status: 500 })
					: Response.json({ items: [network()] }),
			...me(["admin"]),
		});
		await renderWithProviders(<NetworksSection />);
		expect(
			await screen.findByText("Networks did not load"),
		).toBeInTheDocument();
		fail = false;
		await user.click(screen.getByRole("button", { name: "Try again" }));
		expect(await screen.findByText("backend")).toBeInTheDocument();
	});

	describe("deleting", () => {
		it("asks first, then deletes and says so", async () => {
			const user = userEvent.setup();
			const api = stubApi({
				"GET /networks": { body: { items: [network()] } },
				"DELETE /networks/:id": { status: 204 },
				...me(["admin"]),
			});
			await renderWithProviders(<NetworksSection />);
			await user.click(await screen.findByRole("button", { name: "Delete" }));
			const dialog = await screen.findByRole("alertdialog");
			expect(within(dialog).getByText("Delete backend?")).toBeInTheDocument();
			expect(
				within(dialog).getByText(
					"The network is removed. Nothing else changes.",
				),
			).toBeInTheDocument();
			expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
			await user.click(
				within(dialog).getByRole("button", { name: "Delete network" }),
			);
			expect(await screen.findByText("Deleted backend")).toBeInTheDocument();
			expect(api.calls.find((c) => c.method === "DELETE")?.path).toBe(
				"/networks/n1",
			);
		});

		it("warns when services are still attached", async () => {
			const user = userEvent.setup();
			stubApi({
				"GET /networks": {
					body: { items: [network({ services: [attached] })] },
				},
				...me(["admin"]),
			});
			await renderWithProviders(<NetworksSection />);
			await user.click(await screen.findByRole("button", { name: "Delete" }));
			expect(
				await screen.findByText(/Services are still attached\. Detach them/),
			).toBeInTheDocument();
		});

		it("cancel deletes nothing", async () => {
			const user = userEvent.setup();
			const api = stubApi({
				"GET /networks": { body: { items: [network()] } },
				...me(["admin"]),
			});
			await renderWithProviders(<NetworksSection />);
			await user.click(await screen.findByRole("button", { name: "Delete" }));
			const dialog = await screen.findByRole("alertdialog");
			await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
			await waitFor(() =>
				expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
			);
			expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
		});

		it("keeps the dialog open with the server's reason when delete fails", async () => {
			const user = userEvent.setup();
			stubApi({
				"GET /networks": { body: { items: [network()] } },
				"DELETE /networks/:id": problem(409, {
					title: "Conflict",
					status: 409,
					detail: "Network in use",
				}),
				...me(["admin"]),
			});
			await renderWithProviders(<NetworksSection />);
			await user.click(await screen.findByRole("button", { name: "Delete" }));
			const dialog = await screen.findByRole("alertdialog");
			await user.click(
				within(dialog).getByRole("button", { name: "Delete network" }),
			);
			expect(
				await within(dialog).findByText("Network in use"),
			).toBeInTheDocument();
			expect(
				await screen.findByText("Could not delete the network"),
			).toBeInTheDocument();
		});
	});

	it("opens the create dialog from New network", async () => {
		const user = userEvent.setup();
		stubApi({
			"GET /networks": { body: { items: [network()] } },
			"GET /servers": { body: { items: [] } },
			...me(["admin"]),
		});
		await renderWithProviders(<NetworksSection />);
		await user.click(
			await screen.findByRole("button", { name: "New network" }),
		);
		expect(await screen.findByText("New shared network")).toBeInTheDocument();
	});
});
