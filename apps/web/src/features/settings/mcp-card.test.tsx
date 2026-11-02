import { describe, expect, mock, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { McpClientsCard } from "./mcp-card";

const system = {
	mcpEnabled: true,
	mcpLocked: false,
	mcpUrl: "https://ctrl.example.com/mcp",
	publicUrl: "https://ctrl.example.com",
	version: "0.1.0",
};

const me = (instanceAdmin: boolean) => ({
	id: "u1",
	name: "Ada",
	email: "ada@example.com",
	instanceAdmin,
	workspaceId: "w1",
	createdAt: "2026-01-01T00:00:00Z",
	principal: { type: "user", label: "Ada", scopes: [] },
});

describe("McpClientsCard", () => {
	test("shows the endpoint and a client config that names it", async () => {
		const api = stubApi({
			"GET /system": { body: system },
			"GET /me": { body: me(false) },
		});
		await renderWithProviders(<McpClientsCard onCreateToken={() => {}} />);

		const endpoint = await screen.findByRole("group", { name: "Endpoint" });
		expect(
			within(endpoint).getByText("https://ctrl.example.com/mcp"),
		).toBeInTheDocument();
		// The configured public URL is the server's own name: no fallback note.
		expect(screen.queryByText("NEBULACTRL_PUBLIC_URL")).not.toBeInTheDocument();

		const config = screen.getByRole("group", { name: "Client config" });
		const json = JSON.parse(
			within(config).getByText(/mcpServers/, { selector: "pre" }).textContent ??
				"",
		);
		expect(json).toEqual({
			mcpServers: {
				nebulactrl: {
					url: "https://ctrl.example.com/mcp",
					headers: { Authorization: "Bearer <your-token>" },
				},
			},
		});
		expect(api.unmatched).toEqual([]);
	});

	test("falls back to the address the browser reached when no public URL is set", async () => {
		stubApi({
			"GET /system": { body: { ...system, mcpUrl: "", publicUrl: "" } },
			"GET /me": { body: me(false) },
		});
		await renderWithProviders(<McpClientsCard onCreateToken={() => {}} />);

		const endpoint = await screen.findByRole("group", { name: "Endpoint" });
		expect(
			within(endpoint).getByText(`${window.location.origin}/mcp`),
		).toBeInTheDocument();
		expect(
			within(endpoint).getByText("NEBULACTRL_PUBLIC_URL"),
		).toBeInTheDocument();
		const config = screen.getByRole("group", { name: "Client config" });
		expect(config).toHaveTextContent(`"url": "${window.location.origin}/mcp"`);
	});

	test("Create an MCP token hands over to the caller", async () => {
		const onCreateToken = mock(() => {});
		stubApi({
			"GET /system": { body: system },
			"GET /me": { body: me(false) },
		});
		const user = userEvent.setup();
		await renderWithProviders(<McpClientsCard onCreateToken={onCreateToken} />);
		await user.click(
			await screen.findByRole("button", { name: "Create an MCP token" }),
		);
		expect(onCreateToken).toHaveBeenCalledTimes(1);
	});

	test("with MCP off an admin is pointed at the Admin switch", async () => {
		stubApi({
			"GET /system": { body: { ...system, mcpEnabled: false } },
			"GET /me": { body: me(true) },
		});
		await renderWithProviders(<McpClientsCard onCreateToken={() => {}} />);
		const notice = await screen.findByRole("status");
		expect(notice).toHaveTextContent("MCP is off.");
		const link = within(notice).getByRole("link", { name: "Admin → MCP" });
		expect(link).toHaveAttribute("href", "/admin/mcp");
		expect(
			screen.queryByRole("button", { name: "Create an MCP token" }),
		).not.toBeInTheDocument();
	});

	test("with MCP off a member is told the admin switched it off", async () => {
		stubApi({
			"GET /system": { body: { ...system, mcpEnabled: false } },
			"GET /me": { body: me(false) },
		});
		await renderWithProviders(<McpClientsCard onCreateToken={() => {}} />);
		expect(
			await screen.findByText("Your instance admin has switched MCP off."),
		).toBeInTheDocument();
		expect(screen.queryByRole("link")).not.toBeInTheDocument();
	});

	test("a failed state load offers a retry that recovers", async () => {
		let attempts = 0;
		stubApi({
			"GET /system": () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{ status: 500, title: "x", detail: "boom", code: "internal" },
							{ status: 500 },
						)
					: Response.json(system);
			},
			"GET /me": { body: me(false) },
		});
		const user = userEvent.setup();
		await renderWithProviders(<McpClientsCard onCreateToken={() => {}} />);
		expect(
			await screen.findByText("The MCP state did not load"),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: /try again|retry/i }));
		expect(
			await screen.findByRole("group", { name: "Endpoint" }),
		).toBeInTheDocument();
	});
});
