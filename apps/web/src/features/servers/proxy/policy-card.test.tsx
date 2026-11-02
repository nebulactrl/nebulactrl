import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ProxyUpstreamPolicy } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { PolicyCard } from "./policy-card";

const HOST = "Reach services on the host";
const LOOPBACK = "Allow loopback upstreams";
const LINK_LOCAL = "Allow link-local upstreams";

const policy = (
	over: Partial<ProxyUpstreamPolicy> = {},
): ProxyUpstreamPolicy => ({
	allowHostGateway: false,
	allowLoopback: false,
	allowLinkLocal: false,
	...over,
});

const me = (scopes: string[]) => ({
	id: "u1",
	name: "Ada",
	email: "ada@example.com",
	instanceAdmin: scopes.includes("admin"),
	workspaceId: "w1",
	createdAt: "2026-01-01T00:00:00Z",
	principal: { type: "user", label: "Ada", scopes },
});

const problem = (status: number, code: string, detail: string) => ({
	status,
	body: { status, title: "Error", detail, code },
});

const SETTINGS = "PATCH /servers/:serverId/proxy/settings";

function card(
	over: {
		desired?: ProxyUpstreamPolicy;
		applied?: ProxyUpstreamPolicy;
		agentTooOld?: boolean;
	} = {},
) {
	const desired = over.desired ?? policy();
	return (
		<PolicyCard
			serverId="srv-1"
			desired={desired}
			applied={over.applied ?? desired}
			agentTooOld={over.agentTooOld ?? false}
		/>
	);
}

const sw = (name: string) => screen.getByRole("switch", { name });

describe("PolicyCard", () => {
	test("renders the three policy switches reflecting the desired policy", async () => {
		stubApi({ "GET /me": { body: me(["admin"]) } });
		await renderWithProviders(
			card({
				desired: policy({ allowHostGateway: true, allowLoopback: true }),
			}),
		);
		expect(sw(HOST)).toBeChecked();
		expect(sw(LOOPBACK)).toBeChecked();
		expect(sw(LINK_LOCAL)).not.toBeChecked();
	});

	test("a non-administrator can only change the host-gateway switch", async () => {
		const api = stubApi({ "GET /me": { body: me([]) } });
		await renderWithProviders(card());
		await waitFor(() => expect(api.calls.length).toBeGreaterThan(0));
		expect(sw(HOST)).not.toHaveAttribute("aria-disabled", "true");
		expect(sw(LOOPBACK)).toHaveAttribute("aria-disabled", "true");
		expect(sw(LINK_LOCAL)).toHaveAttribute("aria-disabled", "true");
		expect(
			screen.getAllByText("Only an administrator can change this."),
		).toHaveLength(2);
		expect(screen.getAllByText("administrators")).toHaveLength(2);
	});

	test("an administrator can change every switch and sees no lock note", async () => {
		stubApi({ "GET /me": { body: me(["admin"]) } });
		await renderWithProviders(card());
		await waitFor(() =>
			expect(sw(LOOPBACK)).not.toHaveAttribute("aria-disabled", "true"),
		);
		expect(sw(LINK_LOCAL)).not.toHaveAttribute("aria-disabled", "true");
		expect(sw(HOST)).not.toHaveAttribute("aria-disabled", "true");
		expect(
			screen.queryByText("Only an administrator can change this."),
		).not.toBeInTheDocument();
	});

	test("turning a switch on stages exactly that one setting and confirms", async () => {
		const api = stubApi({
			"GET /me": { body: me([]) },
			[SETTINGS]: { body: make("ServerProxy") },
		});
		const user = userEvent.setup();
		await renderWithProviders(card());
		await user.click(sw(HOST));
		expect(await screen.findByText("Policy staged")).toBeInTheDocument();
		expect(
			screen.getByText("It applies on the next Deploy proxy."),
		).toBeInTheDocument();
		const patch = api.calls.find((call) => call.method === "PATCH");
		expect(patch?.path).toBe("/servers/srv-1/proxy/settings");
		expect(patch?.body).toEqual({ upstreamPolicy: { allowHostGateway: true } });
		expect(api.unmatched).toEqual([]);
	});

	test("turning an enabled switch off sends false for that key", async () => {
		const api = stubApi({
			"GET /me": { body: me(["admin"]) },
			[SETTINGS]: { body: make("ServerProxy") },
		});
		const user = userEvent.setup();
		await renderWithProviders(
			card({ desired: policy({ allowLoopback: true }) }),
		);
		await waitFor(() =>
			expect(sw(LOOPBACK)).not.toHaveAttribute("aria-disabled", "true"),
		);
		await user.click(sw(LOOPBACK));
		await screen.findByText("Policy staged");
		expect(api.calls.find((c) => c.method === "PATCH")?.body).toEqual({
			upstreamPolicy: { allowLoopback: false },
		});
	});

	test("an administrator can stage link-local access", async () => {
		const api = stubApi({
			"GET /me": { body: me(["admin"]) },
			[SETTINGS]: { body: make("ServerProxy") },
		});
		const user = userEvent.setup();
		await renderWithProviders(card());
		await waitFor(() =>
			expect(sw(LINK_LOCAL)).not.toHaveAttribute("aria-disabled", "true"),
		);
		await user.click(sw(LINK_LOCAL));
		await screen.findByText("Policy staged");
		expect(api.calls.find((c) => c.method === "PATCH")?.body).toEqual({
			upstreamPolicy: { allowLinkLocal: true },
		});
	});

	test("with every risky switch off no caution is shown", async () => {
		stubApi({ "GET /me": { body: me(["admin"]) } });
		await renderWithProviders(card());
		expect(
			screen.queryByText(/proxy container itself/),
		).not.toBeInTheDocument();
		expect(
			screen.queryByText(/cloud metadata service/),
		).not.toBeInTheDocument();
	});

	test("loopback on warns about the proxy's admin API", async () => {
		stubApi({ "GET /me": { body: me(["admin"]) } });
		await renderWithProviders(
			card({ desired: policy({ allowLoopback: true }) }),
		);
		expect(screen.getByText(/proxy container itself/)).toBeInTheDocument();
		expect(
			screen.queryByText(/cloud metadata service/),
		).not.toBeInTheDocument();
	});

	test("link-local on warns about the cloud metadata service", async () => {
		stubApi({ "GET /me": { body: me(["admin"]) } });
		await renderWithProviders(
			card({ desired: policy({ allowLinkLocal: true }) }),
		);
		expect(screen.getByText(/cloud metadata service/)).toBeInTheDocument();
		expect(
			screen.queryByText(/proxy container itself/),
		).not.toBeInTheDocument();
	});

	test("marks only the settings that differ from the applied policy as staged", async () => {
		stubApi({ "GET /me": { body: me([]) } });
		await renderWithProviders(
			card({
				desired: policy({ allowHostGateway: true, allowLoopback: true }),
				applied: policy({ allowHostGateway: false, allowLoopback: true }),
			}),
		);
		expect(screen.getAllByText("Staged")).toHaveLength(1);
		const row = sw(HOST).closest("li");
		expect(row).toHaveTextContent("Staged");
		expect(sw(LOOPBACK).closest("li")).not.toHaveTextContent("Staged");
	});

	test("an agent that is too old locks every switch, even for an administrator", async () => {
		stubApi({ "GET /me": { body: me(["admin"]) } });
		await renderWithProviders(card({ agentTooOld: true }));
		expect(sw(HOST)).toHaveAttribute("aria-disabled", "true");
		expect(sw(LOOPBACK)).toHaveAttribute("aria-disabled", "true");
		expect(sw(LINK_LOCAL)).toHaveAttribute("aria-disabled", "true");
	});

	test("shows the control plane's message when staging fails", async () => {
		stubApi({
			"GET /me": { body: me([]) },
			[SETTINGS]: problem(500, "internal", "The database is unavailable"),
		});
		const user = userEvent.setup();
		await renderWithProviders(card());
		await user.click(sw(HOST));
		expect(
			await screen.findByText("The database is unavailable"),
		).toBeInTheDocument();
		expect(screen.queryByText("Policy staged")).not.toBeInTheDocument();
		expect(screen.queryByTestId("agent-too-old")).not.toBeInTheDocument();
	});

	test("an agent_too_old answer shows the upgrade banner instead of a plain error", async () => {
		stubApi({
			"GET /me": { body: me([]) },
			[SETTINGS]: problem(422, "agent_too_old", "agent too old"),
		});
		const user = userEvent.setup();
		await renderWithProviders(card());
		await user.click(sw(HOST));
		expect(await screen.findByTestId("agent-too-old")).toBeInTheDocument();
		expect(screen.queryByText("agent too old")).not.toBeInTheDocument();
	});

	test("switches stay disabled while a change is in flight", async () => {
		const gate = Promise.withResolvers<Response>();
		stubApi({
			"GET /me": { body: me(["admin"]) },
			[SETTINGS]: () => gate.promise,
		});
		const user = userEvent.setup();
		await renderWithProviders(card());
		await user.click(sw(HOST));
		await waitFor(() =>
			expect(sw(LOOPBACK)).toHaveAttribute("aria-disabled", "true"),
		);
		expect(sw(LINK_LOCAL)).toHaveAttribute("aria-disabled", "true");
		gate.resolve(Response.json({}));
		await waitFor(() =>
			expect(sw(LOOPBACK)).not.toHaveAttribute("aria-disabled", "true"),
		);
	});
});
