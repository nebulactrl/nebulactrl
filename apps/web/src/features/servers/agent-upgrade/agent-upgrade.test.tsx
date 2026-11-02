import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	InstanceUpgradeView,
	ServerUpgradeView,
} from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { AgentSkew, AgentUpgradeBar, AgentUpgradeCard } from "./agent-upgrade";

function upgrade(over: Loose<ServerUpgradeView> = {}): ServerUpgradeView {
	return {
		agentVersion: "1.4.0",
		canSelfUpgrade: true,
		name: "edge-1",
		serverId: "srv-1",
		skew: "behind",
		status: "online",
		upgradable: true,
		...over,
	} as ServerUpgradeView;
}

function overview(
	servers: ServerUpgradeView[],
	over: Partial<InstanceUpgradeView> = {},
): InstanceUpgradeView {
	return {
		agentAutoUpgrade: false,
		behind: servers.filter((s) => s.skew === "behind").length,
		controlPlaneVersion: "1.5.0",
		servers,
		upgradable: servers.filter((s) => s.upgradable).length,
		...over,
	};
}

/** Lets a test clear an optional field with `undefined`. */
type Loose<T> = { [K in keyof T]?: T[K] | undefined };

const GET = "GET /instance/upgrade";

describe("AgentSkew", () => {
	test("a lagging agent shows how it compares with the control plane", async () => {
		stubApi({ [GET]: { body: overview([upgrade()]) } });
		await renderWithProviders(<AgentSkew serverId="srv-1" />);
		expect(await screen.findByText("Behind 1.5.0")).toBeInTheDocument();
	});

	test.each([
		["ahead", "Newer than the control plane"],
		["unknown", "Version not comparable"],
	] as const)("a %s agent reads %s", async (skew, label) => {
		stubApi({ [GET]: { body: overview([upgrade({ skew })]) } });
		await renderWithProviders(<AgentSkew serverId="srv-1" />);
		expect(await screen.findByText(label)).toBeInTheDocument();
	});

	test("an agent that is current shows nothing", async () => {
		const api = stubApi({
			[GET]: {
				body: overview([upgrade({ skew: "current", upgradable: false })]),
			},
		});
		const { container } = await renderWithProviders(
			<AgentSkew serverId="srv-1" />,
		);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(container).not.toHaveTextContent("Behind");
		expect(container).not.toHaveTextContent("Up to date");
	});

	test("a server missing from the overview shows nothing", async () => {
		const api = stubApi({
			[GET]: { body: overview([upgrade({ serverId: "other" })]) },
		});
		const { container } = await renderWithProviders(
			<AgentSkew serverId="srv-1" />,
		);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(container).not.toHaveTextContent("Behind");
	});

	test("an upgrade that runs replaces the skew with its state", async () => {
		stubApi({
			[GET]: {
				body: overview([upgrade({ state: "queued", targetVersion: "1.5.0" })]),
			},
		});
		await renderWithProviders(<AgentSkew serverId="srv-1" />);
		expect(
			await screen.findByText("Upgrade to 1.5.0 queued"),
		).toBeInTheDocument();
		expect(screen.queryByText("Behind 1.5.0")).toBeNull();
	});

	test("a finished failed upgrade is not the badge: the skew is", async () => {
		stubApi({
			[GET]: {
				body: overview([upgrade({ state: "failed", targetVersion: "1.5.0" })]),
			},
		});
		await renderWithProviders(<AgentSkew serverId="srv-1" />);
		expect(await screen.findByText("Behind 1.5.0")).toBeInTheDocument();
		expect(screen.queryByText(/failed/)).toBeNull();
	});
});

describe("AgentUpgradeBar", () => {
	test("renders nothing before there are servers", async () => {
		const api = stubApi({
			[GET]: { body: overview([], { upgradable: 0, behind: 0 }) },
		});
		const { container } = await renderWithProviders(<AgentUpgradeBar />);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(container.querySelector("section")).toBeNull();
	});

	test("says how many agents are behind and offers the staged upgrade", async () => {
		stubApi({
			[GET]: {
				body: overview([
					upgrade(),
					upgrade({ serverId: "srv-2", name: "edge-2" }),
				]),
			},
		});
		await renderWithProviders(<AgentUpgradeBar />);
		const bar = await screen.findByRole("region", { name: "Agent versions" });
		expect(bar).toHaveTextContent("2 agents are behind 1.5.0");
		expect(bar).toHaveTextContent("Control plane 1.5.0.");
		expect(bar).toHaveTextContent("replaces its own binary");
		expect(
			within(bar).getByRole("button", { name: /Upgrade agents \(2\)/ }),
		).toBeEnabled();
	});

	test("one agent behind reads singular", async () => {
		stubApi({ [GET]: { body: overview([upgrade()]) } });
		await renderWithProviders(<AgentUpgradeBar />);
		expect(
			await screen.findByText("1 agent is behind 1.5.0"),
		).toBeInTheDocument();
	});

	test("when everything is current there is nothing to upgrade", async () => {
		stubApi({
			[GET]: {
				body: overview([upgrade({ skew: "current", upgradable: false })], {
					upgradable: 0,
				}),
			},
		});
		await renderWithProviders(<AgentUpgradeBar />);
		expect(
			await screen.findByText("All agents are on 1.5.0"),
		).toBeInTheDocument();
		const button = screen.getByRole("button", { name: "Upgrade agents" });
		expect(button).toBeDisabled();
	});

	test("failed upgrades are counted and point at the server", async () => {
		stubApi({
			[GET]: {
				body: overview([
					upgrade({ state: "failed" }),
					upgrade({ serverId: "srv-2", state: "failed" }),
				]),
			},
		});
		await renderWithProviders(<AgentUpgradeBar />);
		expect(
			await screen.findByText(
				/2 upgrades failed: open the server for the reason\./,
			),
		).toBeInTheDocument();
	});

	test("one failed upgrade reads singular", async () => {
		stubApi({ [GET]: { body: overview([upgrade({ state: "failed" })]) } });
		await renderWithProviders(<AgentUpgradeBar />);
		expect(
			await screen.findByText(
				/1 upgrade failed: open the server for the reason\./,
			),
		).toBeInTheDocument();
	});

	test("a rollout in progress says so and blocks a second one", async () => {
		stubApi({
			[GET]: {
				body: overview([
					upgrade({ state: "restarting", targetVersion: "1.5.0" }),
				]),
			},
		});
		await renderWithProviders(<AgentUpgradeBar />);
		expect(await screen.findByText("Upgrading agents")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: /Upgrade agents/ }),
		).toBeDisabled();
	});

	test("Upgrade agents confirms with the rollout plan, then posts and toasts", async () => {
		let loads = 0;
		const api = stubApi({
			[GET]: () => {
				loads += 1;
				return Response.json(
					overview([upgrade(), upgrade({ serverId: "srv-2" })]),
				);
			},
			"POST /servers/agent-upgrades": {
				status: 202,
				body: make("RolloutView"),
			},
		});
		await renderWithProviders(<AgentUpgradeBar />);
		const user = userEvent.setup();
		await user.click(
			await screen.findByRole("button", { name: /Upgrade agents \(2\)/ }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent("Upgrade 2 agents to 1.5.0?");
		expect(dialog).toHaveTextContent("2 servers, one at a time in name order.");
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);
		await user.click(
			within(dialog).getByRole("button", { name: "Upgrade agents" }),
		);
		await waitFor(() =>
			expect(api.calls.find((c) => c.method === "POST")).toMatchObject({
				path: "/servers/agent-upgrades",
				body: {},
			}),
		);
		expect(
			(await screen.findAllByText("Upgrading agents")).length,
		).toBeGreaterThan(0);
		await waitFor(() => expect(loads).toBe(2));
	});

	test("a single agent's confirmation reads singular", async () => {
		stubApi({ [GET]: { body: overview([upgrade()]) } });
		await renderWithProviders(<AgentUpgradeBar />);
		await userEvent
			.setup()
			.click(
				await screen.findByRole("button", { name: /Upgrade agents \(1\)/ }),
			);
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent("Upgrade 1 agent to 1.5.0?");
		expect(dialog).toHaveTextContent("1 server, one at a time");
	});

	test("cancelling the confirmation starts nothing", async () => {
		const api = stubApi({ [GET]: { body: overview([upgrade()]) } });
		await renderWithProviders(<AgentUpgradeBar />);
		const user = userEvent.setup();
		await user.click(
			await screen.findByRole("button", { name: /Upgrade agents \(1\)/ }),
		);
		await user.click(
			within(await screen.findByRole("alertdialog")).getByRole("button", {
				name: "Cancel",
			}),
		);
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);
	});

	test("a rollout that cannot start toasts the reason", async () => {
		stubApi({
			[GET]: { body: overview([upgrade()]) },
			"POST /servers/agent-upgrades": () =>
				Response.json(
					{ title: "x", detail: "a rollout is already running" },
					{ status: 409 },
				),
		});
		await renderWithProviders(<AgentUpgradeBar />);
		const user = userEvent.setup();
		await user.click(
			await screen.findByRole("button", { name: /Upgrade agents \(1\)/ }),
		);
		await user.click(
			within(await screen.findByRole("alertdialog")).getByRole("button", {
				name: "Upgrade agents",
			}),
		);
		expect(
			await screen.findByText("Could not start the upgrade"),
		).toBeInTheDocument();
		expect(
			screen.getByText("a rollout is already running"),
		).toBeInTheDocument();
	});

	test("the automatic switch reflects the setting and PATCHes the opposite", async () => {
		let auto = false;
		const api = stubApi({
			[GET]: () =>
				Response.json(overview([upgrade()], { agentAutoUpgrade: auto })),
			"PATCH /instance/upgrade": (req) =>
				req.json().then((b: { agentAutoUpgrade: boolean }) => {
					auto = b.agentAutoUpgrade;
					return Response.json(
						overview([upgrade()], { agentAutoUpgrade: auto }),
					);
				}),
		});
		await renderWithProviders(<AgentUpgradeBar />);
		const toggle = await screen.findByRole("switch");
		expect(toggle).toHaveAttribute("aria-checked", "false");
		await userEvent.setup().click(toggle);
		await waitFor(() =>
			expect(api.calls.find((c) => c.method === "PATCH")?.body).toEqual({
				agentAutoUpgrade: true,
			}),
		);
		await waitFor(() =>
			expect(screen.getByRole("switch")).toHaveAttribute(
				"aria-checked",
				"true",
			),
		);
	});

	test("a failed switch toasts the reason", async () => {
		stubApi({
			[GET]: { body: overview([upgrade()]) },
			"PATCH /instance/upgrade": () =>
				Response.json(
					{ title: "x", detail: "only the instance admin" },
					{ status: 403 },
				),
		});
		await renderWithProviders(<AgentUpgradeBar />);
		await userEvent.setup().click(await screen.findByRole("switch"));
		expect(
			await screen.findByText("Could not change the setting"),
		).toBeInTheDocument();
		expect(screen.getByText("only the instance admin")).toBeInTheDocument();
	});
});

describe("AgentUpgradeCard", () => {
	test("shows both versions and the skew, and offers the upgrade", async () => {
		stubApi({ [GET]: { body: overview([upgrade()]) } });
		await renderWithProviders(<AgentUpgradeCard serverId="srv-1" />);
		expect(await screen.findByText("Agent version")).toBeInTheDocument();
		expect(screen.getByText("1.4.0")).toBeInTheDocument();
		expect(screen.getByText("1.5.0")).toBeInTheDocument();
		expect(screen.getByText("Behind 1.5.0")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Upgrade agent to 1.5.0" }),
		).toBeEnabled();
		expect(screen.queryByRole("button", { name: "Retry anyway" })).toBeNull();
	});

	test("a disconnected agent has no version", async () => {
		stubApi({
			[GET]: {
				body: overview([
					upgrade({
						agentVersion: undefined,
						skew: "unknown",
						upgradable: false,
					}),
				]),
			},
		});
		await renderWithProviders(<AgentUpgradeCard serverId="srv-1" />);
		expect(await screen.findByText("not connected")).toBeInTheDocument();
		expect(screen.getByText("Version not comparable")).toBeInTheDocument();
	});

	test("renders nothing for a server the overview does not list", async () => {
		const api = stubApi({
			[GET]: { body: overview([upgrade({ serverId: "other" })]) },
		});
		const { container } = await renderWithProviders(
			<AgentUpgradeCard serverId="srv-1" />,
		);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(container).not.toHaveTextContent("Agent version");
	});

	test("Upgrade posts force false for this server and toasts", async () => {
		const api = stubApi({
			[GET]: { body: overview([upgrade()]) },
			"POST /servers/:serverId/agent-upgrade": {
				status: 202,
				body: upgrade({ state: "queued", targetVersion: "1.5.0" }),
			},
		});
		await renderWithProviders(<AgentUpgradeCard serverId="srv-1" />);
		await userEvent
			.setup()
			.click(
				await screen.findByRole("button", { name: "Upgrade agent to 1.5.0" }),
			);
		await waitFor(() =>
			expect(api.calls.find((c) => c.method === "POST")).toMatchObject({
				path: "/servers/srv-1/agent-upgrade",
				body: { force: false },
			}),
		);
		expect(await screen.findByText("Upgrade queued")).toBeInTheDocument();
	});

	test("after a failed upgrade Retry anyway posts force true", async () => {
		const api = stubApi({
			[GET]: {
				body: overview([
					upgrade({
						state: "failed",
						targetVersion: "1.5.0",
						message: "rolled back: new binary did not start",
						finishedAt: "2026-10-27T10:00:00Z",
					}),
				]),
			},
			"POST /servers/:serverId/agent-upgrade": {
				status: 202,
				body: upgrade({ state: "queued", targetVersion: "1.5.0" }),
			},
		});
		await renderWithProviders(<AgentUpgradeCard serverId="srv-1" />);
		const user = userEvent.setup();
		expect(
			await screen.findByText("Upgrade to 1.5.0 failed"),
		).toBeInTheDocument();
		expect(
			screen.getByText("rolled back: new binary did not start"),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Retry anyway" }));
		await waitFor(() =>
			expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
				force: true,
			}),
		);
	});

	test("a successful automatic upgrade is reported as such with its message", async () => {
		stubApi({
			[GET]: {
				body: overview([
					upgrade({
						skew: "current",
						upgradable: false,
						state: "succeeded",
						trigger: "auto",
						targetVersion: "1.5.0",
						message: "took 12s",
					}),
				]),
			},
		});
		await renderWithProviders(<AgentUpgradeCard serverId="srv-1" />);
		expect(
			await screen.findByText("Upgraded to 1.5.0 automatically"),
		).toBeInTheDocument();
		expect(screen.getByText("took 12s")).toBeInTheDocument();
		expect(screen.getByText("Up to date")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: /Upgrade agent/ })).toBeNull();
	});

	test("while the upgrade runs there is no button, only its state", async () => {
		stubApi({
			[GET]: {
				body: overview([
					upgrade({ state: "restarting", targetVersion: "1.5.0" }),
				]),
			},
		});
		await renderWithProviders(<AgentUpgradeCard serverId="srv-1" />);
		expect(
			await screen.findByText("Installed 1.5.0, restarting"),
		).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: /Upgrade agent/ })).toBeNull();
	});

	test("an agent that cannot upgrade shows why", async () => {
		stubApi({
			[GET]: {
				body: overview([
					upgrade({
						upgradable: false,
						reason: "The agent runs in a container you manage",
					}),
				]),
			},
		});
		await renderWithProviders(<AgentUpgradeCard serverId="srv-1" />);
		expect(
			await screen.findByText("The agent runs in a container you manage"),
		).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: /Upgrade agent/ })).toBeNull();
	});

	test("a reason is not repeated when the agent is already current", async () => {
		stubApi({
			[GET]: {
				body: overview([
					upgrade({
						skew: "current",
						upgradable: false,
						reason: "Nothing to do",
					}),
				]),
			},
		});
		await renderWithProviders(<AgentUpgradeCard serverId="srv-1" />);
		await screen.findByText("Agent version");
		expect(screen.queryByText("Nothing to do")).toBeNull();
	});

	test("a refused upgrade toasts the reason", async () => {
		stubApi({
			[GET]: { body: overview([upgrade()]) },
			"POST /servers/:serverId/agent-upgrade": () =>
				Response.json(
					{ title: "x", detail: "server is offline" },
					{ status: 409 },
				),
		});
		await renderWithProviders(<AgentUpgradeCard serverId="srv-1" />);
		await userEvent
			.setup()
			.click(
				await screen.findByRole("button", { name: "Upgrade agent to 1.5.0" }),
			);
		expect(
			await screen.findByText("Could not start the upgrade"),
		).toBeInTheDocument();
		expect(screen.getByText("server is offline")).toBeInTheDocument();
	});
});
