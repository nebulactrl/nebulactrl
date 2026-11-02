import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { McpSwitchCard } from "./mcp-switch";

const system = (over: Record<string, unknown> = {}) => ({
	mcpEnabled: false,
	mcpLocked: false,
	mcpUrl: "",
	publicUrl: "",
	version: "0.1.0",
	...over,
});

const problem = (status: number, detail: string) => ({
	status,
	body: { status, title: "Error", detail, code: "bad" },
});

describe("McpSwitchCard", () => {
	test("turning MCP on sends enabled=true, confirms and shows the new state", async () => {
		let state = system();
		const api = stubApi({
			"GET /system": () => Response.json(state),
			"PUT /system/mcp": () => {
				state = system({ mcpEnabled: true });
				return new Response(null, { status: 204 });
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<McpSwitchCard />);

		const toggle = await screen.findByRole("switch");
		expect(toggle).not.toBeChecked();
		expect(screen.getByText("Off")).toBeInTheDocument();
		await user.click(toggle);

		expect(await screen.findByText("MCP turned on")).toBeInTheDocument();
		expect(
			screen.getByText(
				"/mcp answers now. People can create a token for their AI client.",
			),
		).toBeInTheDocument();
		const put = api.calls.find((c) => c.method === "PUT");
		expect(put?.path).toBe("/system/mcp");
		expect(put?.body).toEqual({ enabled: true });
		await waitFor(() => expect(screen.getByRole("switch")).toBeChecked());
		expect(screen.getByText("On")).toBeInTheDocument();
	});

	test("turning MCP off sends enabled=false and says /mcp answers 404", async () => {
		let state = system({ mcpEnabled: true });
		const api = stubApi({
			"GET /system": () => Response.json(state),
			"PUT /system/mcp": () => {
				state = system();
				return new Response(null, { status: 204 });
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<McpSwitchCard />);
		const toggle = await screen.findByRole("switch");
		expect(toggle).toBeChecked();
		await user.click(toggle);
		expect(await screen.findByText("MCP turned off")).toBeInTheDocument();
		expect(screen.getByText("/mcp answers 404 now.")).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
			enabled: false,
		});
	});

	test("a refused switch toasts the reason and does not refetch the state", async () => {
		const api = stubApi({
			"GET /system": { body: system() },
			"PUT /system/mcp": problem(500, "database is locked"),
		});
		const user = userEvent.setup();
		await renderWithProviders(<McpSwitchCard />);
		await user.click(await screen.findByRole("switch"));
		expect(await screen.findByText("Could not switch MCP")).toBeInTheDocument();
		expect(screen.getByText("database is locked")).toBeInTheDocument();
		expect(screen.queryByText("MCP turned on")).not.toBeInTheDocument();
		expect(api.calls.filter((c) => c.path === "/system")).toHaveLength(1);
		expect(screen.getByRole("switch")).not.toBeChecked();
	});

	test("a 409 means the environment decides: the card refetches into the locked state", async () => {
		let state = system();
		stubApi({
			"GET /system": () => Response.json(state),
			"PUT /system/mcp": () => {
				state = system({ mcpLocked: true });
				return Response.json(
					{
						status: 409,
						title: "x",
						detail: "set by environment",
						code: "locked",
					},
					{ status: 409 },
				);
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<McpSwitchCard />);
		await user.click(await screen.findByRole("switch"));
		expect(await screen.findByText("set by environment")).toBeInTheDocument();
		expect(
			await screen.findByText("NEBULACTRL_MCP_ENABLED"),
		).toBeInTheDocument();
		expect(screen.queryByRole("switch")).not.toBeInTheDocument();
	});

	test("a locked state shows who decides and offers no switch", async () => {
		stubApi({
			"GET /system": { body: system({ mcpLocked: true, mcpEnabled: true }) },
		});
		await renderWithProviders(<McpSwitchCard />);
		expect(await screen.findByText(/MCP is on\./)).toBeInTheDocument();
		expect(screen.getByText("NEBULACTRL_MCP_ENABLED")).toBeInTheDocument();
		expect(screen.getByText("On")).toBeInTheDocument();
		expect(screen.queryByRole("switch")).not.toBeInTheDocument();
	});

	test("a locked-off state says MCP is off", async () => {
		stubApi({ "GET /system": { body: system({ mcpLocked: true }) } });
		await renderWithProviders(<McpSwitchCard />);
		expect(await screen.findByText(/MCP is off\./)).toBeInTheDocument();
		expect(screen.getByText("Off")).toBeInTheDocument();
	});

	test("a failed load offers a retry that recovers", async () => {
		let attempts = 0;
		stubApi({
			"GET /system": () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{ status: 500, title: "x", detail: "boom", code: "internal" },
							{ status: 500 },
						)
					: Response.json(system());
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<McpSwitchCard />);
		expect(
			await screen.findByText("The MCP state did not load"),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: /try again|retry/i }));
		expect(await screen.findByRole("switch")).toBeInTheDocument();
	});
});
