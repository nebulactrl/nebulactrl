import { describe, expect, test } from "bun:test";
import {
	act,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { Server } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { type ServerView, toServerView } from "./model";
import { ServerSettingsForm } from "./server-settings-form";

function wire(over: Partial<Server> = {}): Server {
	return {
		id: "srv-1",
		name: "edge-1",
		status: "online",
		publicIp: "203.0.113.10",
		baseDomain: "apps.example.com",
		acmeEmail: "ops@example.com",
		agentVersion: "1.2.3",
		protocolVersion: 5,
		facts: {
			arch: "amd64",
			cgroupVersion: "2",
			cpus: 4,
			diskTotal: 100e9,
			dockerVersion: "27.0",
			kernel: "6.8",
			memTotal: 8e9,
			os: "Ubuntu 24.04",
			port443Free: true,
			port80Free: true,
		},
		lastSeenAt: "2026-10-31T12:00:00Z",
		createdAt: "2026-10-01T12:00:00Z",
		composeVersion: "2.29",
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
			cpuPercent: 10,
			diskTotal: 100e9,
			diskUsed: 40e9,
			memTotal: 8e9,
			memUsed: 2e9,
			sampledAt: "2026-10-31T12:00:00Z",
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
	};
}

function view(over: Partial<Server> = {}): ServerView {
	return toServerView(wire(over));
}

const ME = {
	body: make("MeBody", { instanceAdmin: false, principal: { role: "owner" } }),
};
const MAXMIND_NONE = {
	body: make("GeoCredentialsStatus", {
		accountId: "",
		configured: false,
		serversUsing: 0,
		updatedAt: null,
	}),
};

/** The calls a monitoring page makes besides its own save. */
function monitoringRoutes() {
	return {
		"GET /geo/maxmind": MAXMIND_NONE,
		"GET /me": ME,
	};
}

/** The general page over a server the test can replace, as a refetch would. */
function Swappable({ swap }: { swap: { to: (next: ServerView) => void } }) {
	const [current, set] = useState(view());
	swap.to = set;
	return <ServerSettingsForm server={current} part="general" />;
}

describe("ServerSettingsForm: general", () => {
	test("shows the server's name, address, base domain and ACME email", async () => {
		stubApi({});
		await renderWithProviders(
			<ServerSettingsForm server={view()} part="general" />,
		);
		expect(screen.getByLabelText("Name")).toHaveValue("edge-1");
		expect(screen.getByLabelText("Public IP")).toHaveValue("203.0.113.10");
		expect(screen.getByLabelText("Base domain")).toHaveValue(
			"apps.example.com",
		);
		expect(screen.getByLabelText("ACME email")).toHaveValue("ops@example.com");
		expect(screen.getByText("No unsaved changes.")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();
		expect(screen.queryByRole("button", { name: "Discard" })).toBeNull();
	});

	test("nulls from the API show as empty fields, and the base domain hint tracks the public IP", async () => {
		const user = userEvent.setup();
		stubApi({});
		await renderWithProviders(
			<ServerSettingsForm
				server={view({ publicIp: null, baseDomain: null, acmeEmail: null })}
				part="general"
			/>,
		);
		expect(screen.getByLabelText("Public IP")).toHaveValue("");
		expect(screen.getByLabelText("Base domain")).toHaveValue("");
		expect(screen.getByText(/No public IP yet/)).toBeInTheDocument();
		await user.type(screen.getByLabelText("Public IP"), "198.51.100.7");
		expect(
			screen.getByText("Empty falls back to 198.51.100.7.sslip.io."),
		).toBeInTheDocument();
	});

	test("editing a field marks the form dirty; Discard restores the saved values", async () => {
		const user = userEvent.setup();
		stubApi({});
		await renderWithProviders(
			<ServerSettingsForm server={view()} part="general" />,
		);
		const name = screen.getByLabelText("Name");
		await user.clear(name);
		await user.type(name, "edge-2");
		expect(screen.getByText("You have unsaved changes.")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Save changes" })).toBeEnabled();
		await user.click(screen.getByRole("button", { name: "Discard" }));
		expect(screen.getByLabelText("Name")).toHaveValue("edge-1");
		expect(screen.getByText("No unsaved changes.")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();
	});

	test("an empty name is refused with a message and nothing is sent", async () => {
		const user = userEvent.setup();
		const api = stubApi({});
		await renderWithProviders(
			<ServerSettingsForm server={view()} part="general" />,
		);
		await user.clear(screen.getByLabelText("Name"));
		await user.click(screen.getByRole("button", { name: "Save changes" }));
		expect(await screen.findByText("A server needs a name.")).toBeVisible();
		expect(screen.getByLabelText("Name")).toBeInvalid();
		expect(api.calls).toEqual([]);
	});

	test("a whitespace-only name is also refused", async () => {
		const user = userEvent.setup();
		const api = stubApi({});
		await renderWithProviders(
			<ServerSettingsForm server={view()} part="general" />,
		);
		await user.clear(screen.getByLabelText("Name"));
		await user.type(screen.getByLabelText("Name"), "   ");
		await user.click(screen.getByRole("button", { name: "Save changes" }));
		expect(await screen.findByText("A server needs a name.")).toBeVisible();
		expect(api.calls).toEqual([]);
	});

	test("saving PATCHes only the general fields, trimmed, and toasts", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"PATCH /servers/:serverId": { body: wire({ name: "edge-2" }) },
			"GET /servers/:serverId": { body: wire({ name: "edge-2" }) },
			"GET /servers": { body: { items: [wire({ name: "edge-2" })] } },
		});
		await renderWithProviders(
			<ServerSettingsForm server={view()} part="general" />,
		);
		const name = screen.getByLabelText("Name");
		await user.clear(name);
		await user.type(name, "  edge-2  ");
		const ip = screen.getByLabelText("Public IP");
		await user.clear(ip);
		await user.type(ip, " 198.51.100.7 ");
		await user.click(screen.getByRole("button", { name: "Save changes" }));

		expect(await screen.findByText("Server saved")).toBeInTheDocument();
		expect(
			screen.getByText(
				"Name, address and certificate contact apply from now on.",
			),
		).toBeInTheDocument();
		const patch = api.calls.find((c) => c.method === "PATCH");
		expect(patch?.path).toBe("/servers/srv-1");
		expect(patch?.body).toEqual({
			name: "edge-2",
			publicIp: "198.51.100.7",
			baseDomain: "apps.example.com",
			acmeEmail: "ops@example.com",
		});
		expect(api.unmatched).toEqual([]);
	});

	test("clearing optional fields sends empty strings so the server unsets them", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"PATCH /servers/:serverId": { body: wire() },
			"GET /servers/:serverId": { body: wire() },
			"GET /servers": { body: { items: [] } },
		});
		await renderWithProviders(
			<ServerSettingsForm server={view()} part="general" />,
		);
		await user.clear(screen.getByLabelText("Base domain"));
		await user.clear(screen.getByLabelText("ACME email"));
		await user.click(screen.getByRole("button", { name: "Save changes" }));
		await screen.findByText("Server saved");
		expect(api.calls.find((c) => c.method === "PATCH")?.body).toEqual({
			name: "edge-1",
			publicIp: "203.0.113.10",
			baseDomain: "",
			acmeEmail: "",
		});
	});

	test("a failed save shows the API error in the footer and keeps the draft", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"PATCH /servers/:serverId": {
				status: 409,
				body: {
					title: "Conflict",
					status: 409,
					code: "name_taken",
					detail: "name already in use",
				},
			},
		});
		await renderWithProviders(
			<ServerSettingsForm server={view()} part="general" />,
		);
		await user.clear(screen.getByLabelText("Name"));
		await user.type(screen.getByLabelText("Name"), "taken");
		await user.click(screen.getByRole("button", { name: "Save changes" }));
		expect(await screen.findByText(/name already in use/)).toBeVisible();
		expect(screen.queryByText("Server saved")).toBeNull();
		expect(screen.getByLabelText("Name")).toHaveValue("taken");
		expect(screen.getByRole("button", { name: "Save changes" })).toBeEnabled();
		expect(api.calls.filter((c) => c.method === "PATCH")).toHaveLength(1);
	});

	test("a clean form follows a refetched server; a dirty one keeps what was typed", async () => {
		const user = userEvent.setup();
		stubApi({});
		const swap: { to: (next: ServerView) => void } = { to: () => {} };
		await renderWithProviders(<Swappable swap={swap} />);
		act(() => swap.to(view({ name: "renamed" })));
		await waitFor(() =>
			expect(screen.getByLabelText("Name")).toHaveValue("renamed"),
		);
		expect(screen.getByText("No unsaved changes.")).toBeInTheDocument();

		await user.type(screen.getByLabelText("Public IP"), "9");
		act(() => swap.to(view({ name: "renamed-again", publicIp: "10.0.0.1" })));
		// The IP was being edited, so the typed text stays instead of being overwritten.
		await waitFor(() =>
			expect(screen.getByLabelText("Public IP")).toHaveValue("203.0.113.109"),
		);
		expect(screen.getByLabelText("Name")).toHaveValue("renamed");
	});
});

describe("ServerSettingsForm: scheduling", () => {
	test("a deployments server shows the Accept workloads switch only", async () => {
		stubApi({});
		await renderWithProviders(
			<ServerSettingsForm server={view()} part="scheduling" />,
		);
		expect(
			screen.getByRole("switch", { name: /Accept workloads/ }),
		).toBeChecked();
		expect(screen.queryByLabelText("Builds at once")).toBeNull();
		expect(screen.queryByText(/control-plane host/)).toBeNull();
	});

	test("the control-plane host explains Accept workloads for NebulaCtrl's own host", async () => {
		stubApi({});
		await renderWithProviders(
			<ServerSettingsForm
				server={view({ controlPlane: true, acceptWorkloads: false })}
				part="scheduling"
			/>,
		);
		expect(
			screen.getByRole("switch", { name: /Accept workloads/ }),
		).not.toBeChecked();
		expect(
			screen.getByText(/On, you can deploy apps here next to NebulaCtrl/),
		).toBeInTheDocument();
	});

	test("switching to Builds shows build controls and the running-services note", async () => {
		const user = userEvent.setup();
		stubApi({});
		await renderWithProviders(
			<ServerSettingsForm
				server={view({ serviceCount: 3 })}
				part="scheduling"
			/>,
		);
		await user.click(screen.getByRole("button", { name: "Builds" }));
		expect(screen.getByLabelText("Builds at once")).toHaveValue(2);
		expect(
			screen.getByRole("switch", { name: /Cross-architecture builds/ }),
		).not.toBeChecked();
		expect(
			screen.getByRole("switch", { name: /Pull-request previews/ }),
		).not.toBeChecked();
		expect(
			screen.queryByRole("switch", { name: /Accept workloads/ }),
		).toBeNull();
		expect(
			screen.getByText(/3 services already run here and keep running\./),
		).toBeInTheDocument();
		expect(screen.getByText("You have unsaved changes.")).toBeInTheDocument();
	});

	test("the running-services note is singular for one service and absent for Both", async () => {
		const user = userEvent.setup();
		stubApi({});
		await renderWithProviders(
			<ServerSettingsForm
				server={view({ serviceCount: 1 })}
				part="scheduling"
			/>,
		);
		await user.click(screen.getByRole("button", { name: "Builds" }));
		expect(
			screen.getByText(/1 service already runs here and keeps running\./),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Both" }));
		expect(screen.queryByText(/already runs here/)).toBeNull();
	});

	test("a build role on the control-plane host warns about competing for resources", async () => {
		const user = userEvent.setup();
		stubApi({});
		await renderWithProviders(
			<ServerSettingsForm
				server={view({ controlPlane: true })}
				part="scheduling"
			/>,
		);
		expect(screen.queryByText("This is the control-plane host")).toBeNull();
		await user.click(screen.getByRole("button", { name: "Both" }));
		expect(
			screen.getByText("This is the control-plane host"),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Deployments" }));
		expect(screen.queryByText("This is the control-plane host")).toBeNull();
	});

	test("saving a build server PATCHes the placement fields with the chosen role", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"PATCH /servers/:serverId": { body: wire() },
			"GET /servers/:serverId": { body: wire() },
			"GET /servers": { body: { items: [] } },
		});
		await renderWithProviders(
			<ServerSettingsForm server={view()} part="scheduling" />,
		);
		await user.click(screen.getByRole("button", { name: "Builds" }));
		const concurrency = screen.getByLabelText("Builds at once");
		await user.clear(concurrency);
		await user.type(concurrency, "4");
		await user.click(
			screen.getByRole("switch", { name: /Cross-architecture builds/ }),
		);
		await user.click(screen.getByRole("button", { name: "Save changes" }));
		expect(await screen.findByText("Server saved")).toBeInTheDocument();
		expect(
			screen.getByText("Placement and builds follow the new role from now on."),
		).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "PATCH")?.body).toEqual({
			acceptWorkloads: false,
			buildEnabled: true,
			buildMaxConcurrent: 4,
			buildAllowEmulation: true,
			buildAllowPreviews: false,
		});
	});

	test("toggling Accept workloads off is sent as acceptWorkloads false", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"PATCH /servers/:serverId": { body: wire() },
			"GET /servers/:serverId": { body: wire() },
			"GET /servers": { body: { items: [] } },
		});
		await renderWithProviders(
			<ServerSettingsForm server={view()} part="scheduling" />,
		);
		await user.click(screen.getByRole("switch", { name: /Accept workloads/ }));
		await user.click(screen.getByRole("button", { name: "Save changes" }));
		await screen.findByText("Server saved");
		expect(api.calls.find((c) => c.method === "PATCH")?.body).toEqual({
			acceptWorkloads: false,
			buildEnabled: false,
			buildMaxConcurrent: 2,
			buildAllowEmulation: false,
			buildAllowPreviews: false,
		});
	});

	test("an out-of-range build concurrency is rejected on save, naming the bounds", async () => {
		const user = userEvent.setup();
		const api = stubApi({});
		await renderWithProviders(
			<ServerSettingsForm
				server={view({ buildEnabled: true, acceptWorkloads: false })}
				part="scheduling"
			/>,
		);
		const concurrency = screen.getByLabelText("Builds at once");
		await user.clear(concurrency);
		await user.type(concurrency, "17");
		expect(screen.queryByText(/Use a whole number from 1 to 16/)).toBeNull();
		// The browser's own min/max check would stop a real submit first; the form's guard must hold on its own.
		fireEvent.submit(concurrency.closest("form") as HTMLFormElement);
		expect(
			await screen.findByText("Use a whole number from 1 to 16."),
		).toBeVisible();
		expect(concurrency).toBeInvalid();
		expect(api.calls).toEqual([]);
	});

	test("an invalid concurrency is not an error while the role does not build", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"PATCH /servers/:serverId": { body: wire() },
			"GET /servers/:serverId": { body: wire() },
			"GET /servers": { body: { items: [] } },
		});
		await renderWithProviders(
			<ServerSettingsForm
				server={view({ buildEnabled: true, acceptWorkloads: false })}
				part="scheduling"
			/>,
		);
		const concurrency = screen.getByLabelText("Builds at once");
		await user.clear(concurrency);
		await user.type(concurrency, "0");
		await user.click(screen.getByRole("button", { name: "Deployments" }));
		await user.click(screen.getByRole("button", { name: "Save changes" }));
		await screen.findByText("Server saved");
		// An unparseable concurrency falls back to the default, not NaN or 0.
		expect(api.calls.find((c) => c.method === "PATCH")?.body).toMatchObject({
			buildEnabled: false,
			acceptWorkloads: true,
			buildMaxConcurrent: 2,
		});
	});
});

describe("ServerSettingsForm: monitoring", () => {
	test("shows the retention, disk and traffic values and requests the MaxMind account", async () => {
		const api = stubApi(monitoringRoutes());
		await renderWithProviders(
			<ServerSettingsForm server={view()} part="monitoring" />,
		);
		expect(screen.getByLabelText("Resource history kept (days)")).toHaveValue(
			30,
		);
		expect(screen.getByLabelText("Disk alert threshold (%)")).toHaveValue(85);
		expect(
			screen.getByRole("switch", { name: /Collect traffic analytics/ }),
		).toBeChecked();
		expect(screen.getByLabelText("Hourly traffic kept (days)")).toHaveValue(14);
		expect(screen.getByLabelText("Daily traffic kept (days)")).toHaveValue(90);
		await waitFor(() =>
			expect(api.calls.some((c) => c.path === "/geo/maxmind")).toBe(true),
		);
	});

	test("the general page does not fetch the MaxMind account", async () => {
		const api = stubApi({});
		await renderWithProviders(
			<ServerSettingsForm server={view()} part="general" />,
		);
		expect(api.calls).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});

	test("turning analytics off disables the traffic retention and geolocation fields", async () => {
		const user = userEvent.setup();
		stubApi(monitoringRoutes());
		await renderWithProviders(
			<ServerSettingsForm server={view()} part="monitoring" />,
		);
		await user.click(
			screen.getByRole("switch", { name: /Collect traffic analytics/ }),
		);
		expect(screen.getByLabelText("Hourly traffic kept (days)")).toBeDisabled();
		expect(screen.getByLabelText("Daily traffic kept (days)")).toBeDisabled();
		expect(
			screen.getByRole("switch", { name: /Geolocate requests/ }),
		).toHaveAttribute("aria-disabled", "true");
		expect(
			screen.getByText(
				"Needs traffic analytics, which is off for this server.",
			),
		).toBeInTheDocument();
		expect(
			screen.getAllByText(/Nothing is collected while analytics is off\./),
		).toHaveLength(2);
	});

	test.each([
		["Resource history kept (days)", "0", "Keep 1–90 days."],
		["Resource history kept (days)", "91", "Keep 1–90 days."],
		["Resource history kept (days)", "2.5", "Keep 1–90 days."],
		["Disk alert threshold (%)", "49", "Use 50–99 percent."],
		["Disk alert threshold (%)", "100", "Use 50–99 percent."],
		["Hourly traffic kept (days)", "1", "Keep 2–90 days."],
		["Daily traffic kept (days)", "6", "Keep 7–365 days."],
		["Daily traffic kept (days)", "366", "Keep 7–365 days."],
	])(
		"%s = %s is refused with %s and nothing is sent",
		async (label, text, message) => {
			const user = userEvent.setup();
			const api = stubApi(monitoringRoutes());
			await renderWithProviders(
				<ServerSettingsForm server={view()} part="monitoring" />,
			);
			const input = screen.getByLabelText(label);
			await user.clear(input);
			await user.type(input, text);
			fireEvent.submit(input.closest("form") as HTMLFormElement);
			expect(await screen.findByText(message)).toBeVisible();
			expect(input).toBeInvalid();
			expect(api.calls.filter((c) => c.method === "PATCH")).toEqual([]);
		},
	);

	test("a blank number is refused rather than saved as 0", async () => {
		const user = userEvent.setup();
		const api = stubApi(monitoringRoutes());
		await renderWithProviders(
			<ServerSettingsForm server={view()} part="monitoring" />,
		);
		await user.clear(screen.getByLabelText("Resource history kept (days)"));
		await user.click(screen.getByRole("button", { name: "Save changes" }));
		expect(await screen.findByText("Keep 1–90 days.")).toBeVisible();
		expect(api.calls.filter((c) => c.method === "PATCH")).toEqual([]);
	});

	test("an invalid geo refresh interval blocks the save and shows its message in the footer", async () => {
		const user = userEvent.setup();
		const api = stubApi(monitoringRoutes());
		await renderWithProviders(
			<ServerSettingsForm server={view()} part="monitoring" />,
		);
		const days = screen.getByLabelText("Refresh the database every (days)");
		await user.clear(days);
		await user.type(days, "91");
		fireEvent.submit(days.closest("form") as HTMLFormElement);
		expect(await screen.findAllByText("Use 1–90 days.")).toHaveLength(2);
		expect(api.calls.filter((c) => c.method === "PATCH")).toEqual([]);
	});

	test("enabling MaxMind geolocation without a stored account blocks the save", async () => {
		const user = userEvent.setup();
		const api = stubApi(monitoringRoutes());
		await renderWithProviders(
			<ServerSettingsForm
				server={view({ geoEnabled: true, geoSource: "maxmind" })}
				part="monitoring"
			/>,
		);
		await user.clear(screen.getByLabelText("Resource history kept (days)"));
		await user.type(
			screen.getByLabelText("Resource history kept (days)"),
			"31",
		);
		expect(
			await screen.findByText(
				"Add your MaxMind account ID and licence key first.",
			),
		).toBeVisible();
		await user.click(screen.getByRole("button", { name: "Save changes" }));
		expect(api.calls.filter((c) => c.method === "PATCH")).toEqual([]);
	});

	test("saving PATCHes the monitoring fields and the geolocation fields together", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			...monitoringRoutes(),
			"PATCH /servers/:serverId": { body: wire() },
			"GET /servers/:serverId": { body: wire() },
			"GET /servers": { body: { items: [] } },
		});
		await renderWithProviders(
			<ServerSettingsForm server={view()} part="monitoring" />,
		);
		const metrics = screen.getByLabelText("Resource history kept (days)");
		await user.clear(metrics);
		await user.type(metrics, "45");
		const disk = screen.getByLabelText("Disk alert threshold (%)");
		await user.clear(disk);
		await user.type(disk, "90");
		await user.click(
			screen.getByRole("switch", { name: /Collect traffic analytics/ }),
		);
		await user.click(
			screen.getByRole("switch", { name: /Geolocate requests/ }),
		);
		await user.click(screen.getByRole("button", { name: "Save changes" }));
		// Analytics off disables Geolocate, so only the first click took effect.
		expect(await screen.findByText("Server saved")).toBeInTheDocument();
		expect(
			screen.getByText(/Retention, alerts and geolocation apply from now on/),
		).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "PATCH")?.body).toEqual({
			metricsRetentionDays: 45,
			diskAlertThresholdPercent: 90,
			trafficAnalyticsEnabled: false,
			trafficHourlyRetentionDays: 14,
			trafficDailyRetentionDays: 90,
			geoEnabled: false,
			geoSource: "dbip",
			geoLevel: "country",
			geoRefreshDays: 7,
		});
	});

	test("geolocation choices travel in the PATCH: enabled, city level, new interval", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			...monitoringRoutes(),
			"PATCH /servers/:serverId": { body: wire() },
			"GET /servers/:serverId": { body: wire() },
			"GET /servers": { body: { items: [] } },
		});
		await renderWithProviders(
			<ServerSettingsForm server={view()} part="monitoring" />,
		);
		await user.click(
			screen.getByRole("switch", { name: /Geolocate requests/ }),
		);
		await user.click(
			screen.getByRole("combobox", { name: "Geolocation detail" }),
		);
		await user.click(
			await screen.findByRole("option", { name: "City (~130 MB)" }),
		);
		const days = screen.getByLabelText("Refresh the database every (days)");
		await user.clear(days);
		await user.type(days, "30");
		await user.click(screen.getByRole("button", { name: "Save changes" }));
		await screen.findByText("Server saved");
		expect(api.calls.find((c) => c.method === "PATCH")?.body).toMatchObject({
			geoEnabled: true,
			geoSource: "dbip",
			geoLevel: "city",
			geoRefreshDays: 30,
		});
	});

	test("the footer's Discard resets monitoring edits including geolocation", async () => {
		const user = userEvent.setup();
		stubApi(monitoringRoutes());
		await renderWithProviders(
			<ServerSettingsForm server={view()} part="monitoring" />,
		);
		await user.click(
			screen.getByRole("switch", { name: /Geolocate requests/ }),
		);
		expect(
			screen.getByRole("switch", { name: /Geolocate requests/ }),
		).toBeChecked();
		await user.click(screen.getByRole("button", { name: "Discard" }));
		expect(
			screen.getByRole("switch", { name: /Geolocate requests/ }),
		).not.toBeChecked();
		expect(screen.getByText("No unsaved changes.")).toBeInTheDocument();
	});

	test("edits on another page do not count as dirty here", async () => {
		stubApi(monitoringRoutes());
		await renderWithProviders(
			<ServerSettingsForm server={view({ name: "" })} part="monitoring" />,
		);
		// An invalid name belongs to the general page, so it never blocks monitoring.
		const metrics = screen.getByLabelText("Resource history kept (days)");
		expect(
			within(metrics.closest("form") as HTMLElement).getByRole("button", {
				name: "Save changes",
			}),
		).toBeDisabled();
		expect(screen.queryByText("A server needs a name.")).toBeNull();
	});
});
