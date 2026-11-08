import { describe, expect, test } from "bun:test";
import {
	act,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { useState } from "react";
import type { PrincipalBody, Server } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { type ServerView, toServerView } from "./model";
import { ServerProvider } from "./server-context";
import { ServerSettings } from "./server-settings";

function server(over: Partial<Server> = {}): ServerView {
	return toServerView({
		id: "srv-1",
		name: "edge-1",
		status: "online",
		publicIp: "203.0.113.10",
		baseDomain: "apps.example.com",
		acmeEmail: "ops@example.com",
		agentVersion: "1.0.0",
		protocolVersion: 5,
		facts: {
			arch: "amd64",
			cgroupVersion: "2",
			cpus: 1,
			diskTotal: 1,
			dockerVersion: "27",
			kernel: "6",
			memTotal: 1,
			os: "Ubuntu",
			port443Free: true,
			port80Free: true,
		},
		lastSeenAt: null,
		createdAt: "2026-10-01T00:00:00Z",
		composeVersion: "",
		proxy: {
			status: "ok",
			v2: true,
			generation: 1,
			appliedGeneration: 1,
			tailnet: {
				appliedMode: "off",
				hostname: "",
				ips: null,
				mode: "off",
				state: "",
			},
		},
		latest: {
			cpuPercent: 0,
			diskTotal: 0,
			diskUsed: 0,
			memTotal: 0,
			memUsed: 0,
			sampledAt: "",
		},
		serviceCount: 0,
		drainState: "none",
		drainMessage: "",
		drainStartedAt: null,
		acceptWorkloads: true,
		buildEnabled: false,
		buildMaxConcurrent: 2,
		buildAllowEmulation: false,
		buildAllowPreviews: false,
		controlPlane: false,
		unrestricted: false,
		canDecommission: true,
		diskAlert: false,
		diskAlertThresholdPercent: 85,
		metricsRetentionDays: 30,
		trafficAnalyticsEnabled: true,
		trafficHourlyRetentionDays: 14,
		trafficDailyRetentionDays: 90,
		geoEnabled: false,
		geoSource: "dbip",
		geoLevel: "country",
		geoRefreshDays: 7,
		patches: {
			busy: "",
			checkedAt: null,
			known: false,
			rebootRequired: false,
			security: 0,
			supported: true,
			updates: 0,
		},
		tunnel: {
			active: false,
			connections: 0,
			enabled: false,
			observedAt: null,
			state: "off",
		},
		...over,
	});
}

const ME = (role: NonNullable<PrincipalBody["role"]>) => ({
	body: make("MeBody", {
		id: "u1",
		name: "Ada",
		email: "ada@example.com",
		instanceAdmin: false,
		workspaceId: "w1",
		createdAt: "2026-01-01T00:00:00Z",
		principal: { type: "user", label: "Ada", scopes: [], role },
	}),
});

const CLEANUP = {
	agentSupported: true,
	buildCacheMinAgeDays: 7,
	cron: "0 4 * * *",
	diskAlert: false,
	diskAlertThresholdPercent: 85,
	diskTriggerPercent: null,
	enabled: true,
	nextRunAt: null,
	removeStoppedUnmanaged: false,
	running: false,
	runs: [],
	schedule: "Every day at 04:00 UTC",
	unusedImageMinAgeHours: 72,
};

const LOGS = {
	capture: true,
	retentionDays: 14,
	maxServiceBytes: 512 * 1024 * 1024,
	maxServerBytes: 5 * 1024 * 1024 * 1024,
	online: true,
	requestLogs: true,
	requestLogsKeepQuery: false,
	requestLogsKeepAssets: false,
	requestsTotalBytes: 0,
};

/** Every call the pages make; each test reads only what its page asks for. */
const ROUTES = {
	"GET /me": ME("owner"),
	"GET /geo/maxmind": {
		body: make("GeoCredentialsStatus", {
			accountId: "",
			configured: false,
			serversUsing: 0,
			updatedAt: null,
		}),
	},
	"GET /servers/:serverId/cleanup": { body: CLEANUP },
	"GET /servers/:serverId/log-settings": { body: LOGS },
	"GET /projects": { body: make("PageProject", { items: [] }) },
	"GET /servers/:serverId/mesh": problem(500, { title: "x" }),
	"GET /servers/:serverId/host-shell": problem(403, { title: "no" }),
	"GET /servers/:serverId/unrestricted": problem(500, { title: "x" }),
};

async function renderSettings(section: string, srv: ServerView = server()) {
	return renderWithProviders(
		<ServerProvider value={{ server: srv, openDrain: () => {} }}>
			<ServerSettings server={srv} section={section} />
		</ServerProvider>,
	);
}

/** The general page over a server the test can replace, as the server page does on navigation. */
function Swappable({ swap }: { swap: { to: (next: ServerView) => void } }) {
	const [current, set] = useState(server());
	swap.to = set;
	return (
		<ServerProvider value={{ server: current, openDrain: () => {} }}>
			<ServerSettings server={current} section="general" />
		</ServerProvider>
	);
}

describe("ServerSettings", () => {
	test("offers an owner every page, linked under this server's settings", async () => {
		stubApi(ROUTES);
		await renderSettings("general");
		await screen.findByLabelText("Name");
		const pages = within(screen.getByRole("navigation"));
		const sections: Record<string, string> = {
			General: "general",
			Scheduling: "scheduling",
			Monitoring: "monitoring",
			"Logs and disk": "logs-and-disk",
			Mesh: "mesh",
			"Host shell": "host-shell",
			"Danger zone": "danger",
		};
		for (const [title, section] of Object.entries(sections)) {
			expect(pages.getByRole("link", { name: title })).toHaveAttribute(
				"href",
				`/servers/srv-1/settings/${section}`,
			);
		}
	});

	test("general shows the identity fields only", async () => {
		stubApi(ROUTES);
		await renderSettings("general");
		expect(await screen.findByLabelText("Name")).toHaveValue("edge-1");
		expect(screen.getByLabelText("ACME email")).toHaveValue("ops@example.com");
		expect(screen.queryByLabelText("Disk alert threshold (%)")).toBeNull();
		expect(
			screen.queryByRole("switch", { name: /Accept workloads/ }),
		).toBeNull();
	});

	test("scheduling shows the role and placement controls only", async () => {
		stubApi(ROUTES);
		await renderSettings("scheduling");
		expect(
			await screen.findByRole("switch", { name: /Accept workloads/ }),
		).toBeChecked();
		expect(screen.queryByLabelText("Name")).toBeNull();
	});

	test("monitoring shows retention, traffic and geolocation", async () => {
		stubApi(ROUTES);
		await renderSettings("monitoring");
		expect(
			await screen.findByLabelText("Disk alert threshold (%)"),
		).toHaveValue(85);
		expect(
			screen.getByRole("switch", { name: /Geolocate requests/ }),
		).toBeInTheDocument();
		expect(screen.queryByLabelText("Name")).toBeNull();
	});

	test("logs and disk shows the stored-logs form and the Docker cleanup together", async () => {
		const api = stubApi(ROUTES);
		await renderSettings("logs-and-disk");
		expect(await screen.findByText("Stored logs")).toBeVisible();
		expect(await screen.findByText("Scheduled cleanup")).toBeVisible();
		expect(api.calls.map((c) => c.path)).toEqual(
			expect.arrayContaining([
				"/servers/srv-1/log-settings",
				"/servers/srv-1/cleanup",
			]),
		);
	});

	test("mesh shows this server's node card", async () => {
		stubApi(ROUTES);
		await renderSettings("mesh");
		expect(await screen.findByText("This server's node")).toBeVisible();
	});

	test("danger shows the drain, revoke and remove cards", async () => {
		stubApi(ROUTES);
		await renderSettings("danger");
		expect(await screen.findByText("Drain this server")).toBeVisible();
		expect(screen.getByText("Revoke the agent")).toBeVisible();
		expect(screen.getByText("Remove this server")).toBeVisible();
	});

	test("switching server resets each page's form to the new server's values", async () => {
		stubApi(ROUTES);
		const swap: { to: (next: ServerView) => void } = { to: () => {} };
		await renderWithProviders(<Swappable swap={swap} />);
		const name = await screen.findByLabelText("Name");
		fireEvent.change(name, { target: { value: "half typed" } });
		expect(screen.getByLabelText("Name")).toHaveValue("half typed");
		act(() => swap.to(server({ id: "srv-2", name: "edge-2" })));
		await waitFor(() =>
			expect(screen.getByLabelText("Name")).toHaveValue("edge-2"),
		);
		expect(screen.getByText("No unsaved changes.")).toBeVisible();
	});

	test("a developer is told the page is for owners and admins and gets no form", async () => {
		stubApi({ ...ROUTES, "GET /me": ME("developer") });
		await renderSettings("general");
		expect(
			await screen.findByText("Owners and admins manage this."),
		).toBeVisible();
		expect(screen.queryByLabelText("Name")).toBeNull();
	});

	test("an unknown section is the same notice, not a blank page", async () => {
		stubApi(ROUTES);
		await renderSettings("nope");
		expect(
			await screen.findByText("Owners and admins manage this."),
		).toBeVisible();
	});
});
