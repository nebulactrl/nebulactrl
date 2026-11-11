import { describe, expect, mock, test } from "bun:test";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { getInstanceUpgradeQueryKey } from "#/api/gen/@tanstack/react-query.gen";
import type { Server as ServerWire } from "#/api/gen/types.gen";
import { closeAdoption, openAdoption } from "#/features/server-adoption/store";
import { stubApi } from "#/test/api";
import { renderWithProviders, testQueryClient } from "#/test/render";
import { toServerView } from "./model";
import { serverViewsQuery } from "./queries";
import { ServersPage, type ServersPageProps } from "./servers-page";

const GiB = 1024 ** 3;
const minutesAgo = (n: number) =>
	new Date(Date.now() - n * 60_000).toISOString();

function wireServer(overrides: Partial<ServerWire> = {}): ServerWire {
	return {
		acceptWorkloads: true,
		acmeEmail: null,
		agentVersion: "v0.4.2",
		baseDomain: null,
		buildAllowEmulation: false,
		buildAllowPreviews: false,
		buildEnabled: false,
		buildMaxConcurrent: 2,
		canDecommission: true,
		composeVersion: "",
		controlPlane: false,
		createdAt: "2026-01-01T00:00:00Z",
		diskAlert: false,
		diskAlertThresholdPercent: 90,
		drainMessage: "",
		drainStartedAt: null,
		drainState: "none",
		facts: {
			arch: "arm64",
			cgroupVersion: "2",
			cpus: 4,
			diskTotal: 100 * GiB,
			dockerVersion: "27.3",
			kernel: "6.8",
			memTotal: 16 * GiB,
			os: "Ubuntu 24.04",
			port443Free: true,
			port80Free: true,
		},
		geoEnabled: false,
		geoLevel: "country",
		geoRefreshDays: 7,
		geoSource: "dbip",
		id: "srv-1",
		lastSeenAt: minutesAgo(5),
		latest: {
			cpuPercent: 42,
			diskTotal: 100 * GiB,
			diskUsed: 60 * GiB,
			memTotal: 16 * GiB,
			memUsed: 8 * GiB,
			sampledAt: minutesAgo(5),
		},
		metricsRetentionDays: 14,
		name: "hetzner-01",
		patches: {
			busy: "",
			checkedAt: null,
			known: false,
			rebootRequired: false,
			security: 0,
			supported: true,
			updates: 0,
		},
		protocolVersion: 1,
		proxy: {
			appliedGeneration: 1,
			generation: 1,
			status: "ok",
			tailnet: {
				appliedMode: "off",
				hostname: "",
				ips: null,
				mode: "off",
				state: "",
			},
			v2: true,
		},
		publicIp: "203.0.113.10",
		serviceCount: 3,
		status: "online",
		trafficAnalyticsEnabled: false,
		trafficDailyRetentionDays: 30,
		trafficHourlyRetentionDays: 7,
		tunnel: {
			active: false,
			connections: 0,
			enabled: false,
			observedAt: null,
			state: "off",
		},
		unrestricted: false,
		...overrides,
	};
}

const upgrade = (behind: number) => ({
	agentAutoUpgrade: true,
	behind,
	controlPlaneVersion: "v0.5.0",
	servers: [
		{
			agentVersion: behind ? "v0.4.0" : "v0.5.0",
			canSelfUpgrade: true,
			name: "hetzner-01",
			serverId: "srv-1",
			skew: behind ? "behind" : "current",
			status: "online",
		},
	],
	upgradable: behind,
});

const fleet = {
	servers: [
		{
			committedMemBytes: 0,
			cpuPercent: 12,
			cpus: 4,
			diskTotalBytes: 100 * GiB,
			diskUsedBytes: 10 * GiB,
			drainMessage: "",
			drainState: "none",
			id: "srv-1",
			memTotalBytes: 16 * GiB,
			memUsedBytes: 4 * GiB,
			name: "fleet-box",
			overcommitted: false,
			services: [],
			status: "online",
			unlimitedServices: 0,
		},
	],
	totals: {
		committedMemBytes: 0,
		memTotalBytes: 16 * GiB,
		memUsedBytes: 4 * GiB,
		servers: 1,
		services: 0,
	},
};

const problem = (status: number) => ({
	status,
	body: { status, title: "Error", detail: "boom", code: "internal" },
});

interface Handlers {
	onViewChange: ReturnType<typeof mock>;
	onEnrollOpenChange: ReturnType<typeof mock>;
}

async function renderPage(
	props: Partial<ServersPageProps> = {},
	{ behind = 0 }: { behind?: number } = {},
): Promise<Handlers> {
	const onViewChange = mock((_view: "servers" | "fleet") => {});
	const onEnrollOpenChange = mock((_open: boolean) => {});
	const queryClient = testQueryClient();
	// The upgrade bar reads this; seeding it keeps the page to the calls under test.
	queryClient.setQueryData(getInstanceUpgradeQueryKey(), upgrade(behind));
	await renderWithProviders(
		<ServersPage
			view="servers"
			onViewChange={onViewChange}
			enrollOpen={false}
			onEnrollOpenChange={onEnrollOpenChange}
			{...props}
		/>,
		{ queryClient },
	);
	return { onViewChange, onEnrollOpenChange };
}

describe("ServersPage: servers view", () => {
	test("shows a loading placeholder while the servers load, not the empty state", async () => {
		stubApi({ "GET /servers": () => new Promise<Response>(() => {}) });
		await renderPage();
		expect(
			screen.getByRole("status", { name: "Loading servers" }),
		).toBeInTheDocument();
		expect(screen.queryByText("Add your first server")).not.toBeInTheDocument();
		// Nothing to switch between yet.
		expect(
			screen.queryByRole("group", { name: "Servers view" }),
		).not.toBeInTheDocument();
	});

	test("lists a card per server, linking to it, and sets the page title", async () => {
		const api = stubApi({
			"GET /servers": {
				body: {
					items: [
						wireServer(),
						wireServer({ id: "srv-2", name: "edge-2", serviceCount: 1 }),
					],
				},
			},
		});
		await renderPage();
		expect(
			await screen.findByRole("link", { name: /hetzner-01/ }),
		).toHaveAttribute("href", "/servers/srv-1");
		expect(screen.getByRole("link", { name: /edge-2/ })).toHaveAttribute(
			"href",
			"/servers/srv-2",
		);
		expect(
			screen.getByRole("heading", { name: "Servers" }),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"Docker hosts that run your services, each with its own reverse proxy.",
			),
		).toBeInTheDocument();
		expect(document.title).toBe("Servers · NebulaCtrl");
		expect(screen.queryByText("Add your first server")).not.toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});

	test("Add server in the header asks the route to open the enrol dialog", async () => {
		stubApi({ "GET /servers": { body: { items: [wireServer()] } } });
		const user = userEvent.setup();
		const { onEnrollOpenChange } = await renderPage();
		await screen.findByRole("link", { name: /hetzner-01/ });
		await user.click(screen.getByRole("button", { name: "Add server" }));
		expect(onEnrollOpenChange).toHaveBeenCalledWith(true);
	});

	test("the view switch offers Servers and Fleet and reports a change", async () => {
		stubApi({ "GET /servers": { body: { items: [wireServer()] } } });
		const user = userEvent.setup();
		const { onViewChange } = await renderPage();
		await screen.findByRole("link", { name: /hetzner-01/ });
		const group = screen.getByRole("group", { name: "Servers view" });
		expect(
			within(group).getByRole("button", { name: "Servers" }),
		).toHaveAttribute("aria-pressed", "true");
		await user.click(within(group).getByRole("button", { name: "Fleet" }));
		expect(onViewChange).toHaveBeenCalledWith("fleet");
	});

	test("with no servers it invites adding the first one and hides the view switch and header button", async () => {
		stubApi({ "GET /servers": { body: { items: [] } } });
		const user = userEvent.setup();
		const { onEnrollOpenChange } = await renderPage();
		expect(
			await screen.findByText("Add your first server"),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("group", { name: "Servers view" }),
		).not.toBeInTheDocument();
		// One Add server button: the one in the empty state.
		const buttons = screen.getAllByRole("button", { name: "Add server" });
		expect(buttons).toHaveLength(1);
		expect(screen.getByText("Read the install guide")).toBeInTheDocument();
		await user.click(buttons[0] as HTMLElement);
		expect(onEnrollOpenChange).toHaveBeenCalledWith(true);
	});

	test("a failed load shows an error with a retry that recovers", async () => {
		let attempts = 0;
		stubApi({
			"GET /servers": () => {
				attempts += 1;
				return attempts === 1
					? Response.json(problem(500).body, { status: 500 })
					: Response.json({ items: [wireServer()] });
			},
		});
		const user = userEvent.setup();
		await renderPage();
		expect(await screen.findByText("Servers did not load")).toBeInTheDocument();
		expect(screen.queryByText("Add your first server")).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: /try again|retry/i }));
		expect(
			await screen.findByRole("link", { name: /hetzner-01/ }),
		).toBeInTheDocument();
		expect(screen.queryByText("Servers did not load")).not.toBeInTheDocument();
	});

	test("keeps the servers it already has when a refresh fails", async () => {
		stubApi({
			"GET /servers": () => Response.json(problem(500).body, { status: 500 }),
		});
		const queryClient = testQueryClient();
		queryClient.setQueryData(getInstanceUpgradeQueryKey(), upgrade(0));
		queryClient.setQueryData(serverViewsQuery().queryKey, [
			toServerView(wireServer()),
		]);
		await renderWithProviders(
			<ServersPage
				view="servers"
				onViewChange={() => {}}
				enrollOpen={false}
				onEnrollOpenChange={() => {}}
			/>,
			{ queryClient },
		);
		// The mount refetch fails; the cached card stays, no error page.
		await waitFor(() =>
			expect(
				queryClient.getQueryState(serverViewsQuery().queryKey)?.fetchStatus,
			).toBe("idle"),
		);
		expect(
			screen.getByRole("link", { name: /hetzner-01/ }),
		).toBeInTheDocument();
		expect(screen.queryByText("Servers did not load")).not.toBeInTheDocument();
	});

	test("shows the agent upgrade bar above the list when agents are behind", async () => {
		stubApi({ "GET /servers": { body: { items: [wireServer()] } } });
		await renderPage({}, { behind: 1 });
		expect(
			await screen.findByRole("region", { name: "Agent versions" }),
		).toBeInTheDocument();
	});

	test("opens the enrol dialog when the route says so", async () => {
		stubApi({ "GET /servers": { body: { items: [wireServer()] } } });
		await renderPage({ enrollOpen: true });
		expect(await screen.findByText("Add a server")).toBeInTheDocument();
		expect(screen.getByLabelText("Name")).toBeInTheDocument();
	});

	test("closing the enrol dialog is reported to the route", async () => {
		stubApi({ "GET /servers": { body: { items: [wireServer()] } } });
		const user = userEvent.setup();
		const { onEnrollOpenChange } = await renderPage({ enrollOpen: true });
		await user.click(await screen.findByRole("button", { name: "Cancel" }));
		expect(onEnrollOpenChange).toHaveBeenCalledWith(false);
	});
});

describe("ServersPage: fleet view", () => {
	test("shows capacity per server instead of the cards, with its own description", async () => {
		const api = stubApi({
			"GET /servers": { body: { items: [wireServer()] } },
			"GET /fleet": { body: fleet },
		});
		await renderPage({ view: "fleet" });
		expect(
			screen.getByText("Capacity and placement across every server."),
		).toBeInTheDocument();
		expect(await screen.findByText("fleet-box")).toBeInTheDocument();
		expect(
			screen.queryByRole("link", { name: /hetzner-01/ }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole("region", { name: "Agent versions" }),
		).not.toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});

	test("the switch stays on screen in the fleet view and reports a change back", async () => {
		stubApi({
			"GET /servers": { body: { items: [] } },
			"GET /fleet": { body: fleet },
		});
		const user = userEvent.setup();
		const { onViewChange } = await renderPage({ view: "fleet" });
		const group = await screen.findByRole("group", { name: "Servers view" });
		expect(
			within(group).getByRole("button", { name: "Fleet" }),
		).toHaveAttribute("aria-pressed", "true");
		await user.click(within(group).getByRole("button", { name: "Servers" }));
		expect(onViewChange).toHaveBeenCalledWith("servers");
		// Even with no servers, the fleet view keeps its Add server button.
		expect(
			screen.getByRole("button", { name: "Add server" }),
		).toBeInTheDocument();
	});
});

describe("ServersPage: adoption review", () => {
	test("hosts the review that a server card, or the enrol dialog, opens", async () => {
		const api = stubApi({
			"GET /servers": { body: { items: [wireServer()] } },
			"GET /servers/:serverId": { body: wireServer() },
			"GET /projects": { body: { items: [] } },
			"POST /servers/:serverId/adoption/plan": () =>
				new Promise<Response>(() => {}),
		});
		await renderPage();
		await screen.findByText("hetzner-01");
		act(() => openAdoption("srv-1"));
		expect(
			await screen.findByText("Review workloads on hetzner-01"),
		).toBeInTheDocument();
		expect(
			api.calls.some((c) => c.path === "/servers/srv-1/adoption/plan"),
		).toBe(true);
		act(() => closeAdoption());
	});
});
