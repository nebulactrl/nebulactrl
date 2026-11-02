import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { MeBody } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { AccountSettings, AdminSettings, TeamSettings } from "./settings-areas";

const me = (
	over: { role?: MeBody["principal"]["role"]; instanceAdmin?: boolean } = {},
) =>
	make("MeBody", {
		id: "u1",
		name: "Ada",
		email: "ada@example.com",
		instanceAdmin: over.instanceAdmin ?? false,
		workspaceId: "w1",
		createdAt: "2026-01-01T00:00:00Z",
		principal: {
			type: "user",
			label: "Ada",
			scopes: [],
			role: over.role,
		},
	});

const team = make("Team", {
	id: "team1",
	name: "Acme",
	slug: "acme",
	current: true,
	role: "owner",
	memberCount: 3,
	createdAt: "2026-01-01T00:00:00Z",
});

const nav = (name: string) => within(screen.getByRole("navigation", { name }));

describe("AccountSettings", () => {
	test("names the person in the scope line and opens the profile page", async () => {
		stubApi({ "GET /me": { body: me() } });
		await renderWithProviders(<AccountSettings section="profile" />);
		expect(
			await screen.findByText(
				"Account · ada@example.com · applies to you in every team",
			),
		).toBeInTheDocument();
		expect(
			await screen.findByLabelText("Current password"),
		).toBeInTheDocument();
		const pages = nav("Account settings sections");
		for (const title of ["Profile", "Security", "API tokens", "MCP clients"]) {
			expect(pages.getByRole("link", { name: title })).toBeInTheDocument();
		}
	});

	test("before the profile loads the scope line has no email", async () => {
		stubApi({ "GET /me": () => new Promise<Response>(() => {}) });
		await renderWithProviders(<AccountSettings section="profile" />);
		expect(
			screen.getByText("Account · applies to you in every team"),
		).toBeInTheDocument();
	});

	test("an unknown section says the page is for someone else", async () => {
		stubApi({ "GET /me": { body: me() } });
		await renderWithProviders(<AccountSettings section="nope" />);
		expect(
			await screen.findByText("Owners and admins manage this."),
		).toBeInTheDocument();
		expect(screen.queryByLabelText("Current password")).toBeNull();
	});

	test("New token on the tokens page opens the dialog on the read-only preset", async () => {
		stubApi({
			"GET /me": { body: me() },
			"GET /teams": { body: { items: [team] } },
			"GET /tokens": { body: { items: [] } },
			"GET /projects": { body: { items: [] } },
			"GET /system": { body: make("SystemInfo") },
		});
		const user = userEvent.setup();
		await renderWithProviders(<AccountSettings section="tokens" />);
		await user.click(await screen.findByRole("button", { name: "New token" }));
		expect(await screen.findByText("New API token")).toBeInTheDocument();
		expect(screen.getByLabelText("Name")).toHaveValue("read-only");
		expect(screen.getByRole("radio", { name: /^Read-only/ })).toBeChecked();
	});

	test("Create an MCP token opens the dialog on the MCP read-only preset", async () => {
		stubApi({
			"GET /me": { body: me() },
			"GET /teams": { body: { items: [team] } },
			"GET /projects": { body: { items: [] } },
			"GET /system": {
				body: make("SystemInfo", {
					mcpEnabled: true,
					mcpUrl: "https://nebula.example.com/mcp",
				}),
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<AccountSettings section="mcp-clients" />);
		await user.click(
			await screen.findByRole("button", { name: "Create an MCP token" }),
		);
		expect(await screen.findByText("New API token")).toBeInTheDocument();
		expect(screen.getByLabelText("Name")).toHaveValue("mcp-read-only");
		expect(screen.getByRole("radio", { name: /^MCP read-only/ })).toBeChecked();
	});
});

describe("TeamSettings", () => {
	test("names the current team and lists every page to an owner", async () => {
		stubApi({
			"GET /me": { body: me({ role: "owner" }) },
			"GET /teams": { body: { items: [team] } },
			"GET /audit-events": { body: { items: [] } },
		});
		await renderWithProviders(<TeamSettings section="audit-log" />);
		expect(
			await screen.findByText("Team · Acme · applies to everyone in it"),
		).toBeInTheDocument();
		expect(await screen.findByText("Nothing recorded yet")).toBeInTheDocument();
		const pages = nav("Team settings sections");
		expect(
			await pages.findByRole("link", { name: "Audit log" }),
		).toBeInTheDocument();
		expect(
			pages.getByRole("link", { name: "Backup storage" }),
		).toBeInTheDocument();
	});

	test("without a team the scope line is generic", async () => {
		stubApi({
			"GET /me": { body: me({ role: "owner" }) },
			"GET /teams": { body: { items: [] } },
		});
		await renderWithProviders(<TeamSettings section="nope" />);
		expect(
			await screen.findByText("Team · applies to everyone in it"),
		).toBeInTheDocument();
	});

	test("a developer who types a manager's address gets the explanation, not the page", async () => {
		const api = stubApi({
			"GET /me": { body: me({ role: "developer" }) },
			"GET /teams": {
				body: { items: [{ ...team, role: "developer" }] },
			},
		});
		await renderWithProviders(<TeamSettings section="audit-log" />);
		expect(
			await screen.findByText("Owners and admins manage this."),
		).toBeInTheDocument();
		const pages = nav("Team settings sections");
		expect(pages.queryByRole("link", { name: "Audit log" })).toBeNull();
		expect(pages.getByRole("link", { name: "Members" })).toBeInTheDocument();
		// The audit log was never requested.
		expect(api.calls.some((call) => call.path === "/audit-events")).toBe(false);
	});
});

describe("AdminSettings", () => {
	test("the instance admin sees the install, access and feature pages", async () => {
		stubApi({
			"GET /me": { body: me({ instanceAdmin: true }) },
			"GET /system": { body: make("SystemInfo", { mcpEnabled: true }) },
		});
		await renderWithProviders(<AdminSettings section="mcp" />);
		expect(
			screen.getByText("Instance · applies to every team on this installation"),
		).toBeInTheDocument();
		const pages = nav("Admin sections");
		expect(
			await pages.findByRole("link", { name: "Updates" }),
		).toBeInTheDocument();
		expect(
			pages.getByRole("link", { name: "Sign-in and teams" }),
		).toBeInTheDocument();
		expect(
			pages.getByRole("link", { name: "Email relay" }),
		).toBeInTheDocument();
		expect(screen.queryByText("Owners and admins manage this.")).toBeNull();
	});

	test("anyone else gets the explanation and no pages", async () => {
		stubApi({ "GET /me": { body: me() } });
		await renderWithProviders(<AdminSettings section="mcp" />);
		expect(
			await screen.findByText("Owners and admins manage this."),
		).toBeInTheDocument();
		expect(
			nav("Admin sections").queryByRole("link", { name: "Updates" }),
		).toBeNull();
	});
});
