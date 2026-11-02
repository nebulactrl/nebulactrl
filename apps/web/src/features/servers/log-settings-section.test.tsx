import { describe, expect, test } from "bun:test";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { LogSettingsView, Server, ServiceView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { LogSettingsSection } from "./log-settings-section";
import { type ServerView, toServerView } from "./model";

const MB = 1024 * 1024;
const GB = 1024 * MB;

function server(over: Partial<ServerView> = {}): ServerView {
	const wire = {
		id: "srv-1",
		name: "edge-1",
		status: "online",
		publicIp: null,
		baseDomain: null,
		acmeEmail: null,
		agentVersion: null,
		protocolVersion: null,
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
	} satisfies Server;
	return { ...toServerView(wire), ...over };
}

function logView(over: Partial<LogSettingsView> = {}): LogSettingsView {
	return {
		capture: true,
		retentionDays: 14,
		maxServiceBytes: 512 * MB,
		maxServerBytes: 5 * GB,
		online: true,
		...over,
	};
}

/** A service placed on this server, as the list endpoint returns it. */
const serviceWire = (id: string, name: string) =>
	make("ServiceView", { id, name, serverId: "srv-1", server: { id: "srv-1" } });

const SETTINGS = "GET /servers/:serverId/log-settings";
const SAVE = "PUT /servers/:serverId/log-settings";

/** The three levels useServicesOnServer walks, holding the given services. */
function catalogue(services: ServiceView[]) {
	return {
		"GET /projects": {
			body: make("PageProject", {
				items: [make("Project", { id: "p1", name: "Shop" })],
			}),
		},
		"GET /projects/:projectId/environments": {
			body: make("PageEnvironment", {
				items: [make("Environment", { id: "e1", name: "production" })],
			}),
		},
		"GET /environments/:environmentId/services": {
			body: make("PageServiceView", { items: services }),
		},
	};
}

const OWNER_ID = "11111111-aaaa-bbbb-cccc-000000000001";

describe("LogSettingsSection: loading", () => {
	test("requests the server's own log settings", async () => {
		const api = stubApi({ [SETTINGS]: { body: logView() }, ...catalogue([]) });
		await renderWithProviders(<LogSettingsSection server={server()} />);
		await screen.findByText("Stored logs");
		expect(api.calls.find((c) => c.path.endsWith("/log-settings"))?.path).toBe(
			"/servers/srv-1/log-settings",
		);
	});

	test("a failed load says so with the reason and offers no form", async () => {
		stubApi({
			[SETTINGS]: problem(500, { title: "Internal", detail: "agent bus down" }),
		});
		await renderWithProviders(<LogSettingsSection server={server()} />);
		expect(
			await screen.findByText(/Log settings did not load: .*agent bus down/),
		).toBeVisible();
		expect(
			screen.queryByRole("button", { name: "Save log settings" }),
		).toBeNull();
	});
});

describe("LogSettingsSection: fields", () => {
	test("show the saved capture switch, retention and sizes in the unit that fits", async () => {
		stubApi({ [SETTINGS]: { body: logView() }, ...catalogue([]) });
		await renderWithProviders(<LogSettingsSection server={server()} />);
		await screen.findByText("Stored logs");
		expect(
			screen.getByRole("switch", { name: /Keep each service's output/ }),
		).toBeChecked();
		expect(screen.getByLabelText("Keep logs for (days)")).toHaveValue(14);
		expect(screen.getByLabelText("Limit per service")).toHaveValue(512);
		expect(
			screen.getByRole("combobox", { name: "Unit of the per-service limit" }),
		).toHaveTextContent("MB");
		expect(screen.getByLabelText("Limit for the server")).toHaveValue(5);
		expect(
			screen.getByRole("combobox", { name: "Unit of the server limit" }),
		).toHaveTextContent("GB");
		expect(screen.getByText("No unsaved changes.")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Save log settings" }),
		).toBeDisabled();
	});

	test("an online server shows no offline notice", async () => {
		stubApi({ [SETTINGS]: { body: logView() }, ...catalogue([]) });
		await renderWithProviders(<LogSettingsSection server={server()} />);
		await screen.findByText("Stored logs");
		expect(screen.queryByText("edge-1 is offline")).toBeNull();
	});

	test("an offline server says settings still save and apply on reconnect", async () => {
		const user = userEvent.setup();
		stubApi({
			[SETTINGS]: { body: logView({ online: false }) },
			...catalogue([]),
		});
		await renderWithProviders(<LogSettingsSection server={server()} />);
		expect(await screen.findByText("edge-1 is offline")).toBeVisible();
		expect(
			screen.getByText(/You can still change these settings\./),
		).toBeVisible();
		await user.click(
			screen.getByRole("switch", { name: /Keep each service's output/ }),
		);
		expect(
			screen.getByText(
				/You have unsaved changes\. They apply when the server reconnects\./,
			),
		).toBeVisible();
	});
});

describe("LogSettingsSection: saving", () => {
	test("an edit enables Save and Discard; Discard restores the saved values", async () => {
		const user = userEvent.setup();
		stubApi({ [SETTINGS]: { body: logView() }, ...catalogue([]) });
		await renderWithProviders(<LogSettingsSection server={server()} />);
		const days = await screen.findByLabelText("Keep logs for (days)");
		await user.clear(days);
		await user.type(days, "30");
		expect(screen.getByText("You have unsaved changes.")).toBeVisible();
		expect(
			screen.getByRole("button", { name: "Save log settings" }),
		).toBeEnabled();
		await user.click(screen.getByRole("button", { name: "Discard" }));
		expect(screen.getByLabelText("Keep logs for (days)")).toHaveValue(14);
		expect(screen.getByText("No unsaved changes.")).toBeVisible();
		expect(screen.queryByRole("button", { name: "Discard" })).toBeNull();
	});

	test("Save PUTs the whole policy in bytes and toasts that the agent applies it", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			[SETTINGS]: { body: logView() },
			[SAVE]: { body: logView() },
			...catalogue([]),
		});
		await renderWithProviders(<LogSettingsSection server={server()} />);
		const days = await screen.findByLabelText("Keep logs for (days)");
		await user.clear(days);
		await user.type(days, "30");
		await user.click(
			screen.getByRole("switch", { name: /Keep each service's output/ }),
		);
		const perService = screen.getByLabelText("Limit per service");
		await user.clear(perService);
		await user.type(perService, "256");
		await user.click(screen.getByRole("button", { name: "Save log settings" }));

		expect(await screen.findByText("Log settings saved")).toBeInTheDocument();
		expect(screen.getByText("The agent applies them now.")).toBeInTheDocument();
		const put = api.calls.find((c) => c.method === "PUT");
		expect(put?.path).toBe("/servers/srv-1/log-settings");
		expect(put?.body).toEqual({
			capture: false,
			retentionDays: 30,
			maxServiceBytes: 256 * MB,
			maxServerBytes: 5 * GB,
		});
	});

	test("switching a cap's unit to GB multiplies it into the request", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			[SETTINGS]: { body: logView() },
			[SAVE]: { body: logView() },
			...catalogue([]),
		});
		await renderWithProviders(<LogSettingsSection server={server()} />);
		await screen.findByText("Stored logs");
		await user.click(
			screen.getByRole("combobox", { name: "Unit of the per-service limit" }),
		);
		await user.click(await screen.findByRole("option", { name: "GB" }));
		// 512 GB per service now exceeds the 5 GB server limit; lower it first.
		const perService = screen.getByLabelText("Limit per service");
		await user.clear(perService);
		await user.type(perService, "2");
		await user.click(screen.getByRole("button", { name: "Save log settings" }));
		await screen.findByText("Log settings saved");
		expect(api.calls.find((c) => c.method === "PUT")?.body).toMatchObject({
			maxServiceBytes: 2 * GB,
			maxServerBytes: 5 * GB,
		});
	});

	test("saving while offline promises the change for the reconnect", async () => {
		const user = userEvent.setup();
		stubApi({
			[SETTINGS]: { body: logView({ online: false }) },
			[SAVE]: { body: logView() },
			...catalogue([]),
		});
		await renderWithProviders(<LogSettingsSection server={server()} />);
		await user.click(
			await screen.findByRole("switch", { name: /Keep each service's output/ }),
		);
		await user.click(screen.getByRole("button", { name: "Save log settings" }));
		await screen.findByText("Log settings saved");
		expect(
			screen.getByText("Changes apply when the server reconnects."),
		).toBeInTheDocument();
	});

	test("after a save the refetched settings become the new baseline", async () => {
		const user = userEvent.setup();
		let saved = logView();
		stubApi({
			[SETTINGS]: () => Response.json(saved),
			[SAVE]: () => {
				saved = logView({ retentionDays: 30 });
				return Response.json(saved);
			},
			...catalogue([]),
		});
		await renderWithProviders(<LogSettingsSection server={server()} />);
		const days = await screen.findByLabelText("Keep logs for (days)");
		await user.clear(days);
		await user.type(days, "30");
		await user.click(screen.getByRole("button", { name: "Save log settings" }));
		await screen.findByText("Log settings saved");
		await waitFor(() =>
			expect(screen.getByText("No unsaved changes.")).toBeVisible(),
		);
		expect(screen.getByLabelText("Keep logs for (days)")).toHaveValue(30);
		expect(
			screen.getByRole("button", { name: "Save log settings" }),
		).toBeDisabled();
	});

	test("a rejected save shows the API error, keeps the edit, and Discard clears the error", async () => {
		const user = userEvent.setup();
		stubApi({
			[SETTINGS]: { body: logView() },
			[SAVE]: problem(422, {
				title: "Unprocessable",
				detail: "cap too small for this host",
			}),
			...catalogue([]),
		});
		await renderWithProviders(<LogSettingsSection server={server()} />);
		const days = await screen.findByLabelText("Keep logs for (days)");
		await user.clear(days);
		await user.type(days, "30");
		await user.click(screen.getByRole("button", { name: "Save log settings" }));
		expect(
			await screen.findByText(/cap too small for this host/),
		).toBeVisible();
		expect(screen.queryByText("Log settings saved")).toBeNull();
		expect(screen.getByLabelText("Keep logs for (days)")).toHaveValue(30);
		await user.click(screen.getByRole("button", { name: "Discard" }));
		expect(screen.queryByText(/cap too small for this host/)).toBeNull();
		expect(screen.getByText("No unsaved changes.")).toBeVisible();
	});
});

describe("LogSettingsSection: validation", () => {
	// The browser's own min/max would stop a real submit first; the form's guard must hold on its own.
	function submit(from: HTMLElement) {
		fireEvent.submit(from.closest("form") as HTMLFormElement);
	}

	test.each([
		["0", "Keep 1–90 days."],
		["91", "Keep 1–90 days."],
		["", "Keep 1–90 days."],
	])(
		"retention %p is refused with %s and nothing is sent",
		async (text, message) => {
			const user = userEvent.setup();
			const api = stubApi({
				[SETTINGS]: { body: logView() },
				...catalogue([]),
			});
			await renderWithProviders(<LogSettingsSection server={server()} />);
			const days = await screen.findByLabelText("Keep logs for (days)");
			await user.clear(days);
			if (text) await user.type(days, text);
			expect(screen.queryByText(message)).toBeNull();
			submit(days);
			expect(await screen.findByText(message)).toBeVisible();
			expect(days).toBeInvalid();
			expect(api.calls.filter((c) => c.method === "PUT")).toEqual([]);
		},
	);

	test("a cap under 32 MB is refused", async () => {
		const user = userEvent.setup();
		const api = stubApi({ [SETTINGS]: { body: logView() }, ...catalogue([]) });
		await renderWithProviders(<LogSettingsSection server={server()} />);
		const perService = await screen.findByLabelText("Limit per service");
		await user.clear(perService);
		await user.type(perService, "16");
		submit(perService);
		expect(await screen.findByText("At least 32 MB.")).toBeVisible();
		expect(perService).toBeInvalid();
		expect(api.calls.filter((c) => c.method === "PUT")).toEqual([]);
	});

	test("an empty cap asks for a size", async () => {
		const user = userEvent.setup();
		stubApi({ [SETTINGS]: { body: logView() }, ...catalogue([]) });
		await renderWithProviders(<LogSettingsSection server={server()} />);
		const perServer = await screen.findByLabelText("Limit for the server");
		await user.clear(perServer);
		submit(perServer);
		expect(await screen.findByText("Enter a size.")).toBeVisible();
	});

	test("a per-service limit above the server limit is refused", async () => {
		const user = userEvent.setup();
		const api = stubApi({ [SETTINGS]: { body: logView() }, ...catalogue([]) });
		await renderWithProviders(<LogSettingsSection server={server()} />);
		const perService = await screen.findByLabelText("Limit per service");
		await user.clear(perService);
		await user.type(perService, "6000");
		submit(perService);
		expect(
			await screen.findByText(
				"The per-service limit cannot exceed the server limit.",
			),
		).toBeVisible();
		expect(api.calls.filter((c) => c.method === "PUT")).toEqual([]);
	});
});

describe("LogSettingsSection: space used", () => {
	test("without a report it says no usage is known yet", async () => {
		stubApi({ [SETTINGS]: { body: logView() }, ...catalogue([]) });
		await renderWithProviders(<LogSettingsSection server={server()} />);
		expect(await screen.findByText(/No usage reported yet\./)).toBeVisible();
		expect(screen.queryByRole("progressbar")).toBeNull();
	});

	test("shows the total against the server limit and the biggest services first, by name", async () => {
		stubApi({
			[SETTINGS]: {
				body: logView({
					usage: {
						reportedAt: "2026-10-31T10:00:00Z",
						totalBytes: Math.round(1.2 * GB),
						services: [
							{
								serviceId: "svc-small",
								bytes: 40 * MB,
								lines: 1,
							},
							{
								serviceId: "svc-big",
								bytes: 900 * MB,
								lines: 1200,
								oldest: "2026-10-20T08:30:00Z",
							},
						],
					},
				}),
			},
			...catalogue([
				serviceWire("svc-big", "api"),
				serviceWire("svc-small", "worker"),
			]),
		});
		await renderWithProviders(<LogSettingsSection server={server()} />);
		expect(
			await screen.findByText("Using 1.2 GB of 5 GB on this server"),
		).toBeVisible();
		const bar = screen.getByRole("progressbar", {
			name: "Space used by stored logs on this server",
		});
		expect(Number(bar.getAttribute("aria-valuenow"))).toBeCloseTo(24, 1);
		const rows = await waitFor(() => {
			const items = screen.getAllByRole("listitem");
			expect(items[0]).toHaveTextContent("api");
			return items;
		});
		expect(rows).toHaveLength(2);
		expect(rows[0]).toHaveTextContent(/api.*900 MB · 1,200 lines · back to /);
		expect(rows[1]).toHaveTextContent("worker");
		expect(rows[1]).toHaveTextContent("40 MB · 1 line");
		expect(rows[1]).not.toHaveTextContent("back to");
		expect(
			screen.queryByText(/A deleted service's logs stay on this server/),
		).toBeNull();
	});

	test("a service nobody knows any more is labelled deleted with its short id, and explained", async () => {
		stubApi({
			[SETTINGS]: {
				body: logView({
					usage: {
						reportedAt: "2026-10-31T10:00:00Z",
						totalBytes: 100 * MB,
						services: [{ serviceId: OWNER_ID, bytes: 100 * MB, lines: 10 }],
					},
				}),
			},
			...catalogue([serviceWire("svc-other", "api")]),
		});
		await renderWithProviders(<LogSettingsSection server={server()} />);
		expect(await screen.findByText("Deleted service 11111111")).toBeVisible();
		expect(
			screen.getByText(
				"A deleted service's logs stay on this server until they expire.",
			),
		).toBeVisible();
	});

	test("only the five biggest services are listed", async () => {
		const services = Array.from({ length: 6 }, (_, i) => ({
			serviceId: `svc-${i}`,
			bytes: (i + 1) * 10 * MB,
			lines: i + 1,
		}));
		stubApi({
			[SETTINGS]: {
				body: logView({
					usage: {
						reportedAt: "2026-10-31T10:00:00Z",
						totalBytes: 210 * MB,
						services,
					},
				}),
			},
			...catalogue(
				services.map((s) =>
					serviceWire(s.serviceId, `svc-name-${s.serviceId.slice(-1)}`),
				),
			),
		});
		await renderWithProviders(<LogSettingsSection server={server()} />);
		await screen.findByText(/Using 210 MB of 5 GB/);
		const list = await screen.findByRole("list");
		await waitFor(() =>
			expect(within(list).getAllByRole("listitem")[0]).toHaveTextContent(
				"svc-name-5",
			),
		);
		const items = within(list).getAllByRole("listitem");
		expect(items).toHaveLength(5);
		expect(items.some((li) => li.textContent?.includes("svc-name-0"))).toBe(
			false,
		);
	});

	test("a report with no services says none has stored logs yet", async () => {
		stubApi({
			[SETTINGS]: {
				body: logView({
					usage: {
						reportedAt: "2026-10-31T10:00:00Z",
						totalBytes: 0,
						services: null,
					},
				}),
			},
			...catalogue([]),
		});
		await renderWithProviders(<LogSettingsSection server={server()} />);
		expect(
			await screen.findByText("No service has stored logs yet."),
		).toBeVisible();
	});
});
