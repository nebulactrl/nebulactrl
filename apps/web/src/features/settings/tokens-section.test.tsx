import { describe, expect, mock, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Token } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, type Overrides } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { TokensSection } from "./tokens-section";

const token = (over: Overrides<Token> = {}): Token =>
	make("Token", {
		id: "t1",
		name: "ci-deploy",
		prefix: "ab12cd34",
		kind: "rest",
		scopes: ["projects:read", "servers:read"],
		allowProductionWithoutApproval: false,
		createdAt: "2026-01-01T00:00:00Z",
		...over,
	});

const project = (id: string, name: string) =>
	make("Project", { id, name, slug: name, createdAt: "2026-01-01T00:00:00Z" });

function routes(
	items: Token[] | null,
	extra: Parameters<typeof stubApi>[0] = {},
) {
	return {
		"GET /tokens": { body: make("ListTokensOutputBody", { items }) },
		"GET /projects": {
			body: make("PageProject", { items: [project("p1", "Storefront")] }),
		},
		"GET /system": {
			body: make("SystemInfo", { publicUrl: "https://nebula.example.com/" }),
		},
		...extra,
	};
}

function rowOf(name: string): HTMLElement {
	const row = screen.getByText(name).closest("tr");
	if (!row) throw new Error(`no row for ${name}`);
	return row;
}

describe("TokensSection", () => {
	test("an empty list invites creating the first token", async () => {
		stubApi(routes([]));
		const onCreate = mock(() => {});
		const user = userEvent.setup();
		await renderWithProviders(<TokensSection onCreate={onCreate} />);
		expect(
			await screen.findByText("Create your first API token"),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "New token" }));
		expect(onCreate).toHaveBeenCalledTimes(1);
	});

	test("a null item list is treated as empty", async () => {
		stubApi(routes(null));
		await renderWithProviders(<TokensSection onCreate={() => {}} />);
		expect(
			await screen.findByText("Create your first API token"),
		).toBeInTheDocument();
	});

	test("a failed load says so and retries on request", async () => {
		let calls = 0;
		stubApi(
			routes([], {
				"GET /tokens": () => {
					calls += 1;
					return calls === 1
						? Response.json(
								{ status: 500, title: "x", detail: "boom", code: "internal" },
								{ status: 500 },
							)
						: Response.json({ items: [token()] });
				},
			}),
		);
		const user = userEvent.setup();
		await renderWithProviders(<TokensSection onCreate={() => {}} />);
		expect(await screen.findByText("Tokens did not load")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: /try again|retry/i }));
		expect(await screen.findByText("ci-deploy")).toBeInTheDocument();
		expect(screen.queryByText("Tokens did not load")).not.toBeInTheDocument();
	});

	test("the install block names this instance from its public URL", async () => {
		stubApi(routes([]));
		await renderWithProviders(<TokensSection onCreate={() => {}} />);
		expect(
			await screen.findByText(
				"curl -fsSL https://nebula.example.com/cli.sh | sh",
			),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"nebulactrl login --url https://nebula.example.com --token <token>",
			),
		).toBeInTheDocument();
	});

	test("without a public URL the install block falls back to the browser origin", async () => {
		stubApi(
			routes([], {
				"GET /system": { body: make("SystemInfo", { publicUrl: "" }) },
			}),
		);
		await renderWithProviders(<TokensSection onCreate={() => {}} />);
		expect(
			await screen.findByText(
				`curl -fsSL ${window.location.origin}/cli.sh | sh`,
			),
		).toBeInTheDocument();
	});

	test("each token row shows its prefix, access summary, projects and state", async () => {
		stubApi(
			routes([
				token(),
				token({
					id: "t2",
					name: "agent",
					prefix: "ff00ee11",
					kind: "mcp",
					scopes: ["admin"],
					allowProductionWithoutApproval: true,
					projectIds: ["p1", "gone"],
					lastUsedAt: "2026-02-01T00:00:00Z",
				}),
			]),
		);
		await renderWithProviders(<TokensSection onCreate={() => {}} />);
		await screen.findByText("ci-deploy");
		await screen.findByText(/Storefront/);

		const rest = within(rowOf("ci-deploy"));
		expect(rest.getByText("nct_ab12cd34…")).toBeInTheDocument();
		expect(rest.getByText("Read-only")).toBeInTheDocument();
		expect(rest.getByText("REST API and CLI")).toBeInTheDocument();
		expect(
			rest.getByText("Production changes need approval"),
		).toBeInTheDocument();
		expect(rest.getByText("All projects")).toBeInTheDocument();
		expect(rest.getAllByText("never")).toHaveLength(2);
		expect(rest.getByText("active")).toBeInTheDocument();

		const mcp = within(rowOf("agent"));
		expect(mcp.getByText("Full access")).toBeInTheDocument();
		expect(mcp.getByText("MCP agent")).toBeInTheDocument();
		expect(mcp.getByText("Skips production approval")).toBeInTheDocument();
		expect(mcp.getByText("Storefront, unknown project")).toBeInTheDocument();
		expect(mcp.getAllByText("never")).toHaveLength(1);
	});

	test("expired tokens say so and still offer revoke; revoked ones do not", async () => {
		stubApi(
			routes([
				token({ id: "e", name: "old", expiresAt: "2020-01-01T00:00:00Z" }),
				token({
					id: "r",
					name: "gone",
					revokedAt: "2026-01-02T00:00:00Z",
				}),
				token({ id: "f", name: "future", expiresAt: "2999-01-01T00:00:00Z" }),
			]),
		);
		await renderWithProviders(<TokensSection onCreate={() => {}} />);
		await screen.findByText("old");

		const expired = within(rowOf("old"));
		// The state badge and the "expired <when>" prefix in the Expires column.
		expect(expired.getAllByText("expired")).toHaveLength(2);
		expect(
			expired.getAllByText("expired").map((el) => el.getAttribute("title")),
		).toContain("2020-01-01 00:00:00 UTC");
		expect(expired.getByRole("button", { name: "Revoke" })).toBeInTheDocument();

		const revoked = within(rowOf("gone"));
		expect(revoked.getByText("revoked")).toBeInTheDocument();
		expect(revoked.queryByRole("button", { name: "Revoke" })).toBeNull();

		const future = within(rowOf("future"));
		expect(future.getByText("active")).toBeInTheDocument();
		expect(future.queryByText("expired")).toBeNull();
	});

	test("revoking asks first, then deletes the token and confirms", async () => {
		const api = stubApi(
			routes([token()], { "DELETE /tokens/:id": { status: 204 } }),
		);
		const user = userEvent.setup();
		await renderWithProviders(<TokensSection onCreate={() => {}} />);
		await screen.findByText("ci-deploy");
		await user.click(
			within(rowOf("ci-deploy")).getByRole("button", { name: "Revoke" }),
		);
		expect(await screen.findByText("Revoke ci-deploy?")).toBeInTheDocument();
		expect(api.calls.some((call) => call.method === "DELETE")).toBe(false);
		await user.click(screen.getByRole("button", { name: "Revoke token" }));
		expect(await screen.findByText("Revoked ci-deploy")).toBeInTheDocument();
		expect(
			screen.getByText("It stops working immediately."),
		).toBeInTheDocument();
		const del = api.calls.find((call) => call.method === "DELETE");
		expect(del?.path).toBe("/tokens/t1");
		expect(api.unmatched).toEqual([]);
	});

	test("cancelling the confirmation revokes nothing", async () => {
		const api = stubApi(routes([token()]));
		const user = userEvent.setup();
		await renderWithProviders(<TokensSection onCreate={() => {}} />);
		await screen.findByText("ci-deploy");
		await user.click(
			within(rowOf("ci-deploy")).getByRole("button", { name: "Revoke" }),
		);
		await user.click(await screen.findByRole("button", { name: "Cancel" }));
		expect(api.calls.filter((call) => call.method === "DELETE")).toEqual([]);
	});

	test("a failed revoke surfaces the server's reason in the dialog and a toast", async () => {
		stubApi(
			routes([token()], {
				"DELETE /tokens/:id": {
					status: 409,
					body: {
						status: 409,
						title: "Conflict",
						detail: "Token is in use by a running deploy",
						code: "conflict",
					},
				},
			}),
		);
		const user = userEvent.setup();
		await renderWithProviders(<TokensSection onCreate={() => {}} />);
		await screen.findByText("ci-deploy");
		await user.click(
			within(rowOf("ci-deploy")).getByRole("button", { name: "Revoke" }),
		);
		await user.click(
			await screen.findByRole("button", { name: "Revoke token" }),
		);
		expect(
			await screen.findByText("Could not revoke the token"),
		).toBeInTheDocument();
		expect(
			screen.getAllByText("Token is in use by a running deploy").length,
		).toBeGreaterThan(0);
		expect(screen.queryByText("Revoked ci-deploy")).toBeNull();
	});

	test("New token in the header calls onCreate", async () => {
		stubApi(routes([token()]));
		const onCreate = mock(() => {});
		const user = userEvent.setup();
		await renderWithProviders(<TokensSection onCreate={onCreate} />);
		await screen.findByText("ci-deploy");
		await user.click(screen.getByRole("button", { name: "New token" }));
		expect(onCreate).toHaveBeenCalledTimes(1);
	});
});
