import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { PlatformServicesSection } from "./platform-services-section";
import { platformListing, platformService, serverView } from "./test-fixtures";

describe("PlatformServicesSection", () => {
	test("loads with a status label", async () => {
		stubApi({
			"GET /platform-services": () => new Promise<Response>(() => {}),
			"GET /servers": { body: { items: [] } },
		});
		await renderWithProviders(<PlatformServicesSection />);
		expect(await screen.findByText("Loading Headscale")).toBeInTheDocument();
	});

	test("a non-admin is told who can set it up, without a retry", async () => {
		stubApi({
			"GET /platform-services": problem(403, {
				title: "Forbidden",
				detail: "no",
			}),
			"GET /servers": { body: { items: [] } },
		});
		await renderWithProviders(<PlatformServicesSection />);
		expect(
			await screen.findByText("Only administrators can manage Headscale"),
		).toBeInTheDocument();
		expect(
			screen.getByText("Ask a team owner or admin to set it up."),
		).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
	});

	test("another failure shows the reason and Retry refetches the listing", async () => {
		let failing = true;
		const api = stubApi({
			"GET /platform-services": () =>
				failing
					? Response.json(
							{ title: "Internal", detail: "Database is down" },
							{ status: 500 },
						)
					: Response.json(platformListing()),
			"GET /servers": { body: { items: [serverView()] } },
		});
		await renderWithProviders(<PlatformServicesSection />);
		expect(
			await screen.findByText("Headscale did not load"),
		).toBeInTheDocument();
		expect(
			screen.getByText("Database is down. Nothing was changed."),
		).toBeInTheDocument();
		failing = false;
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Try again" }));
		expect(
			await screen.findByRole("article", { name: "Headscale" }),
		).toBeInTheDocument();
		expect(
			api.calls.filter((c) => c.path === "/platform-services").length,
		).toBe(2);
	});

	test("not set up: the card invites, enabled while a server is usable", async () => {
		stubApi({
			"GET /platform-services": { body: platformListing() },
			"GET /servers": { body: { items: [serverView()] } },
		});
		await renderWithProviders(<PlatformServicesSection />);
		const card = await screen.findByRole("article", { name: "Headscale" });
		expect(within(card).getByText("Not set up")).toBeInTheDocument();
		expect(
			within(card).getByRole("button", { name: "Set up for me" }),
		).toBeEnabled();
		expect(
			screen.queryByRole("link", { name: "Open the platform project" }),
		).toBeNull();
	});

	test("with every server offline the set-up is disabled with the reason", async () => {
		stubApi({
			"GET /platform-services": { body: platformListing() },
			"GET /servers": {
				body: {
					items: [
						serverView({ status: "offline" }),
						serverView({ id: "s2", acceptWorkloads: false }),
					],
				},
			},
		});
		await renderWithProviders(<PlatformServicesSection />);
		const card = await screen.findByRole("article", { name: "Headscale" });
		await waitFor(() =>
			expect(
				within(card).getByRole("button", { name: "Set up for me" }),
			).toBeDisabled(),
		);
		expect(within(card).getByTestId("platform-reason")).toHaveTextContent(
			"No server is online",
		);
	});

	test("set up: links to the platform project and shows the service", async () => {
		stubApi({
			"GET /platform-services": {
				body: platformListing(
					platformService({ status: "ready" }),
					{},
					{ projectId: "proj-platform" },
				),
			},
			"GET /servers": { body: { items: [serverView()] } },
		});
		await renderWithProviders(<PlatformServicesSection />);
		const link = await screen.findByRole("link", {
			name: "Open the platform project",
		});
		expect(link).toHaveAttribute("href", "/p/proj-platform/platform");
		const card = screen.getByRole("article", { name: "Headscale" });
		expect(
			within(card).getByText("Ready", { selector: "span" }),
		).toBeInTheDocument();
	});

	test("an action refetches the listing so the card follows", async () => {
		let status: "ready" | "provisioning" = "ready";
		const api = stubApi({
			"GET /platform-services": () =>
				Response.json(
					platformListing(
						platformService({
							status,
							step: status === "ready" ? "done" : "wire",
						}),
					),
				),
			"GET /servers": { body: { items: [serverView()] } },
			"POST /platform-services/:id/reconnect": () => {
				status = "provisioning";
				return new Response(null, { status: 204 });
			},
		});
		await renderWithProviders(<PlatformServicesSection />);
		await userEvent
			.setup()
			.click(await screen.findByRole("button", { name: "Reconnect" }));
		const current = await screen.findByRole("listitem", { current: "step" });
		expect(current).toHaveTextContent("Connecting");
		expect(
			api.calls.filter((c) => c.path === "/platform-services").length,
		).toBe(2);
	});
});
