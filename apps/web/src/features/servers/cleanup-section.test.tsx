import { describe, expect, spyOn, test } from "bun:test";
import {
	act,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	CleanupRun,
	CleanupStep,
	CleanupView,
	Server,
} from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { CleanupSection } from "./cleanup-section";
import { type ServerView, toServerView } from "./model";

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

function cleanup(over: Partial<CleanupView> = {}): CleanupView {
	return {
		agentSupported: true,
		buildCacheMinAgeDays: 7,
		cron: "0 4 * * *",
		diskAlert: false,
		diskAlertThresholdPercent: 85,
		diskTriggerPercent: null,
		enabled: true,
		nextRunAt: "2030-01-01T04:00:00Z",
		removeStoppedUnmanaged: false,
		running: false,
		runs: [],
		schedule: "Every day at 04:00 UTC",
		unusedImageMinAgeHours: 72,
		...over,
	};
}

function run(over: Partial<CleanupRun> = {}): CleanupRun {
	return {
		createdAt: "2026-10-30T04:00:00Z",
		finishedAt: "2026-10-30T04:01:00Z",
		id: "run-1",
		items: 3,
		mode: "standard",
		reclaimedBytes: 2 * 1024 * 1024,
		status: "succeeded",
		steps: [],
		trigger: "scheduled",
		...over,
	};
}

function step(over: Partial<CleanupStep> = {}): CleanupStep {
	return {
		items: 2,
		name: "images_unused",
		protected: 0,
		reclaimedBytes: 0,
		removed: [],
		...over,
	};
}

const GET = "GET /servers/:serverId/cleanup";
const PUT = "PUT /servers/:serverId/cleanup";
const RUN = "POST /servers/:serverId/cleanup/runs";
const VOLUMES = "POST /servers/:serverId/cleanup/volumes";

const HOLD_MS = 1300;

/** Presses a hold key with its 1.3 s timer shortened to the next tick (fake clocks would strand react-query's timers) and lets it run. */
async function hold(button: HTMLElement) {
	const real = window.setTimeout;
	const rushed = spyOn(window, "setTimeout").mockImplementation(((
		handler: TimerHandler,
		ms?: number,
		...args: unknown[]
	) =>
		real(
			handler,
			ms === HOLD_MS ? 0 : ms,
			...args,
		)) as typeof window.setTimeout);
	try {
		fireEvent.keyDown(button, { key: "Enter" });
	} finally {
		rushed.mockRestore();
	}
	const tick = Promise.withResolvers<void>();
	window.setTimeout(tick.resolve, 0);
	await act(async () => {
		await tick.promise;
	});
}

async function renderSection(
	view: CleanupView = cleanup(),
	srv: ServerView = server(),
) {
	const api = stubApi({ [GET]: { body: view } });
	await renderWithProviders(<CleanupSection server={srv} />);
	await screen.findByText("Scheduled cleanup");
	return api;
}

const saveButton = () => screen.getByRole("button", { name: "Save changes" });

describe("CleanupSection: loading", () => {
	test("requests this server's cleanup", async () => {
		const api = await renderSection();
		expect(api.calls[0]?.path).toBe("/servers/srv-1/cleanup");
	});

	test("a failed load says so with the reason", async () => {
		stubApi({
			[GET]: problem(500, { title: "Internal", detail: "docker unreachable" }),
		});
		await renderWithProviders(<CleanupSection server={server()} />);
		expect(
			await screen.findByText(
				/Docker cleanup did not load: .*docker unreachable/,
			),
		).toBeVisible();
		expect(screen.queryByText("Scheduled cleanup")).toBeNull();
	});
});

describe("CleanupSection: settings", () => {
	test("show the saved schedule, limits and the disk trigger placeholder", async () => {
		await renderSection(
			cleanup({ unusedImageMinAgeHours: 48, buildCacheMinAgeDays: 3 }),
		);
		expect(
			screen.getByRole("switch", { name: /Scheduled cleanup/ }),
		).toBeChecked();
		expect(
			screen.getByText(
				"Every day at 04:00 UTC, and when disk use reaches 85%.",
			),
		).toBeVisible();
		expect(
			screen.getByRole("switch", { name: /Stopped containers/ }),
		).not.toBeChecked();
		expect(screen.getByLabelText("Schedule")).toHaveValue("0 4 * * *");
		expect(screen.getByLabelText("Unused images")).toHaveValue(48);
		expect(screen.getByLabelText("Build cache")).toHaveValue(3);
		const trigger = screen.getByLabelText("Disk trigger (%)");
		expect(trigger).toHaveValue(null);
		expect(trigger).toHaveAttribute("placeholder", "85");
		expect(screen.getByText("No unsaved changes.")).toBeVisible();
		expect(saveButton()).toBeDisabled();
	});

	test("an explicit disk trigger replaces the threshold in the description and fills the field", async () => {
		await renderSection(cleanup({ diskTriggerPercent: 90 }));
		expect(
			screen.getByText(
				"Every day at 04:00 UTC, and when disk use reaches 90%.",
			),
		).toBeVisible();
		expect(screen.getByLabelText("Disk trigger (%)")).toHaveValue(90);
	});

	test("the next run is shown while on, and an off schedule says nothing is removed", async () => {
		const user = userEvent.setup();
		await renderSection();
		const next = screen.getByText(/Next run/);
		expect(within(next).getByText(/.+/, { selector: "time" })).toHaveAttribute(
			"datetime",
			"2030-01-01T04:00:00Z",
		);
		await user.click(screen.getByRole("switch", { name: /Scheduled cleanup/ }));
		expect(
			screen.getByText("Off. Nothing is removed unless you run a cleanup."),
		).toBeVisible();
		expect(screen.getByText(/You have unsaved changes\./)).toBeVisible();
	});

	test("a schedule that was off says it starts once saved after switching on", async () => {
		const user = userEvent.setup();
		await renderSection(cleanup({ enabled: false, nextRunAt: null }));
		expect(
			screen.getByText("Off. Nothing is removed unless you run a cleanup."),
		).toBeVisible();
		await user.click(screen.getByRole("switch", { name: /Scheduled cleanup/ }));
		expect(screen.getByText("Starts once you save.")).toBeVisible();
	});

	test("a preset fills the schedule", async () => {
		const user = userEvent.setup();
		await renderSection();
		await user.click(screen.getByRole("button", { name: "Every 6 hours" }));
		expect(screen.getByLabelText("Schedule")).toHaveValue("0 */6 * * *");
		await user.click(screen.getByRole("button", { name: "Sundays at 03:00" }));
		expect(screen.getByLabelText("Schedule")).toHaveValue("0 3 * * 0");
	});

	test("Discard restores every field and both switches", async () => {
		const user = userEvent.setup();
		await renderSection();
		await user.click(screen.getByRole("button", { name: "Every 6 hours" }));
		await user.click(
			screen.getByRole("switch", { name: /Stopped containers/ }),
		);
		const hours = screen.getByLabelText("Unused images");
		await user.clear(hours);
		await user.type(hours, "10");
		await user.click(screen.getByRole("button", { name: "Discard" }));
		expect(screen.getByLabelText("Schedule")).toHaveValue("0 4 * * *");
		expect(screen.getByLabelText("Unused images")).toHaveValue(72);
		expect(
			screen.getByRole("switch", { name: /Stopped containers/ }),
		).not.toBeChecked();
		expect(screen.getByText("No unsaved changes.")).toBeVisible();
	});
});

describe("CleanupSection: validation", () => {
	test.each([
		["Schedule", "", "Enter a cron expression, like 0 4 * * *."],
		[
			"Schedule",
			"0 4 * *",
			"A schedule has five fields: minute hour day-of-month month day-of-week.",
		],
		["Unused images", "0", "Image age must be 1 to 8760."],
		["Unused images", "9000", "Image age must be 1 to 8760."],
		["Build cache", "366", "Build cache age must be 1 to 365."],
		["Build cache", "", "Build cache age must be a whole number."],
		["Disk trigger (%)", "49", "The disk trigger must be 50 to 99."],
		["Disk trigger (%)", "100", "The disk trigger must be 50 to 99."],
	])("%s = %p shows %p and blocks Save", async (label, text, message) => {
		const user = userEvent.setup();
		const api = await renderSection();
		const input = screen.getByLabelText(label);
		await user.clear(input);
		if (text) await user.type(input, text);
		expect(await screen.findByText(message)).toBeVisible();
		expect(input).toBeInvalid();
		expect(saveButton()).toBeDisabled();
		expect(api.calls.filter((c) => c.method === "PUT")).toEqual([]);
	});

	test("an empty disk trigger is valid: it follows the alert threshold", async () => {
		const user = userEvent.setup();
		await renderSection(cleanup({ diskTriggerPercent: 90 }));
		await user.clear(screen.getByLabelText("Disk trigger (%)"));
		expect(screen.getByLabelText("Disk trigger (%)")).toBeValid();
		expect(saveButton()).toBeEnabled();
	});
});

describe("CleanupSection: saving", () => {
	test("Save PUTs every setting, normalising the cron and toasts", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			[GET]: { body: cleanup() },
			[PUT]: { body: cleanup() },
		});
		await renderWithProviders(<CleanupSection server={server()} />);
		await screen.findByText("Scheduled cleanup");
		const cron = screen.getByLabelText("Schedule");
		await user.clear(cron);
		await user.type(cron, "  30   2 * * 1 ");
		await user.click(
			screen.getByRole("switch", { name: /Stopped containers/ }),
		);
		const days = screen.getByLabelText("Build cache");
		await user.clear(days);
		await user.type(days, "14");
		const disk = screen.getByLabelText("Disk trigger (%)");
		await user.type(disk, "90");
		await user.click(saveButton());

		expect(
			await screen.findByText("Cleanup settings saved"),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"They apply from now; a new schedule does not catch up on earlier runs.",
			),
		).toBeInTheDocument();
		const put = api.calls.find((c) => c.method === "PUT");
		expect(put?.path).toBe("/servers/srv-1/cleanup");
		expect(put?.body).toEqual({
			enabled: true,
			removeStoppedUnmanaged: true,
			cron: "30 2 * * 1",
			unusedImageMinAgeHours: 72,
			buildCacheMinAgeDays: 14,
			diskTriggerPercent: 90,
		});
	});

	test("an empty disk trigger is sent as 0 so the server follows the threshold", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			[GET]: { body: cleanup({ diskTriggerPercent: 90 }) },
			[PUT]: { body: cleanup() },
		});
		await renderWithProviders(<CleanupSection server={server()} />);
		await screen.findByText("Scheduled cleanup");
		await user.clear(screen.getByLabelText("Disk trigger (%)"));
		await user.click(saveButton());
		await screen.findByText("Cleanup settings saved");
		expect(api.calls.find((c) => c.method === "PUT")?.body).toMatchObject({
			diskTriggerPercent: 0,
		});
	});

	test("turning the schedule off is sent as enabled false", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			[GET]: { body: cleanup() },
			[PUT]: { body: cleanup() },
		});
		await renderWithProviders(<CleanupSection server={server()} />);
		await screen.findByText("Scheduled cleanup");
		await user.click(screen.getByRole("switch", { name: /Scheduled cleanup/ }));
		await user.click(saveButton());
		await screen.findByText("Cleanup settings saved");
		expect(api.calls.find((c) => c.method === "PUT")?.body).toMatchObject({
			enabled: false,
			removeStoppedUnmanaged: false,
		});
	});

	test("after a save the refetched view is the new baseline", async () => {
		const user = userEvent.setup();
		let saved = cleanup();
		stubApi({
			[GET]: () => Response.json(saved),
			[PUT]: () => {
				saved = cleanup({ buildCacheMinAgeDays: 14 });
				return Response.json({});
			},
		});
		await renderWithProviders(<CleanupSection server={server()} />);
		await screen.findByText("Scheduled cleanup");
		const days = screen.getByLabelText("Build cache");
		await user.clear(days);
		await user.type(days, "14");
		await user.click(saveButton());
		await screen.findByText("Cleanup settings saved");
		await waitFor(() =>
			expect(screen.getByText("No unsaved changes.")).toBeVisible(),
		);
		expect(screen.getByLabelText("Build cache")).toHaveValue(14);
		expect(saveButton()).toBeDisabled();
	});

	test("a rejected save shows the API's reason; Discard clears it", async () => {
		const user = userEvent.setup();
		stubApi({
			[GET]: { body: cleanup() },
			[PUT]: problem(422, {
				title: "Unprocessable",
				detail: "runs must be at least 15 minutes apart",
			}),
		});
		await renderWithProviders(<CleanupSection server={server()} />);
		await screen.findByText("Scheduled cleanup");
		const cron = screen.getByLabelText("Schedule");
		await user.clear(cron);
		await user.type(cron, "* * * * *");
		await user.click(saveButton());
		expect(
			await screen.findByText(/runs must be at least 15 minutes apart/),
		).toBeVisible();
		expect(screen.queryByText("Cleanup settings saved")).toBeNull();
		expect(screen.getByLabelText("Schedule")).toHaveValue("* * * * *");
		await user.click(screen.getByRole("button", { name: "Discard" }));
		expect(screen.queryByText(/runs must be at least/)).toBeNull();
		expect(screen.getByLabelText("Schedule")).toHaveValue("0 4 * * *");
	});
});

describe("CleanupSection: run now", () => {
	test("POSTs a run, toasts and re-reads the view", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			[GET]: { body: cleanup() },
			[RUN]: {
				status: 202,
				body: run({ status: "pending", trigger: "manual" }),
			},
		});
		await renderWithProviders(<CleanupSection server={server()} />);
		await screen.findByText("Scheduled cleanup");
		const button = screen.getByRole("button", { name: "Run cleanup now" });
		expect(button).toBeEnabled();
		await user.click(button);
		expect(await screen.findByText("Cleanup started")).toBeInTheDocument();
		expect(
			screen.getByText("The result appears under Recent cleanups."),
		).toBeInTheDocument();
		expect(
			api.calls.filter((c) => c.method === "POST").map((c) => c.path),
		).toEqual(["/servers/srv-1/cleanup/runs"]);
		await waitFor(() =>
			expect(api.calls.filter((c) => c.method === "GET")).toHaveLength(2),
		);
	});

	test("a refused run shows the API's reason", async () => {
		const user = userEvent.setup();
		stubApi({
			[GET]: { body: cleanup() },
			[RUN]: problem(409, {
				title: "Conflict",
				detail: "a cleanup is already queued",
			}),
		});
		await renderWithProviders(<CleanupSection server={server()} />);
		await user.click(
			await screen.findByRole("button", { name: "Run cleanup now" }),
		);
		expect(
			await screen.findByText(/a cleanup is already queued/),
		).toBeVisible();
		expect(screen.queryByText("Cleanup started")).toBeNull();
	});

	test("with unsaved changes it warns the run uses the saved settings", async () => {
		const user = userEvent.setup();
		await renderSection();
		expect(
			screen.queryByText(
				"Runs with the saved settings, not the unsaved changes.",
			),
		).toBeNull();
		await user.click(screen.getByRole("button", { name: "Every 6 hours" }));
		expect(
			screen.getByText(
				"Runs with the saved settings, not the unsaved changes.",
			),
		).toBeVisible();
		expect(
			screen.getByRole("button", { name: "Run cleanup now" }),
		).toBeEnabled();
	});

	test("an offline server cannot run one and says it waits for the server", async () => {
		await renderSection(cleanup(), server({ status: "offline" }));
		expect(
			screen.getByRole("button", { name: "Run cleanup now" }),
		).toBeDisabled();
		expect(
			screen.getByText(
				"edge-1 is offline. A cleanup runs when it is back online.",
			),
		).toBeVisible();
		expect(
			screen.getByRole("button", { name: /Remove unused volumes/ }),
		).toBeDisabled();
	});

	test("an agent that is too old cannot run one and is told to update", async () => {
		await renderSection(cleanup({ agentSupported: false }));
		expect(
			screen.getByRole("button", { name: "Run cleanup now" }),
		).toBeDisabled();
		expect(
			screen.getByText(
				"This agent is too old to run a cleanup. Update it first.",
			),
		).toBeVisible();
	});

	test("while a cleanup runs the button reads Cleaning up and is disabled", async () => {
		await renderSection(cleanup({ running: true }));
		expect(screen.getByRole("button", { name: /Cleaning up/ })).toBeDisabled();
		expect(screen.getByText("A cleanup is running.")).toBeVisible();
		expect(
			screen.queryByRole("button", { name: "Run cleanup now" }),
		).toBeNull();
		expect(
			screen.getByRole("button", { name: /Remove unused volumes/ }),
		).toBeDisabled();
	});
});

describe("CleanupSection: recent cleanups", () => {
	test("an empty history explains when the first run happens", async () => {
		await renderSection();
		expect(
			screen.getByText(/No cleanup has run on this server yet\./),
		).toBeVisible();
	});

	test("lists each run with who started it and what it freed", async () => {
		await renderSection(
			cleanup({
				runs: [
					run({ id: "a", trigger: "scheduled" }),
					run({
						id: "b",
						trigger: "emergency",
						reclaimedBytes: 0,
						items: 0,
					}),
					run({ id: "c", trigger: "manual" }),
					run({ id: "d", mode: "volumes", trigger: "manual" }),
					run({ id: "e", status: "pending" }),
					run({ id: "f", status: "failed", reclaimedBytes: 0, items: 0 }),
					run({ id: "g", status: "failed" }),
				],
			}),
		);
		const rows = screen.getAllByRole("listitem");
		expect(rows).toHaveLength(7);
		expect(rows[0]).toHaveTextContent("Scheduled");
		expect(rows[0]).toHaveTextContent("Freed 2 MiB · 3 items removed");
		expect(rows[1]).toHaveTextContent("Disk alert");
		expect(rows[1]).toHaveTextContent("Freed 0 B · 0 items removed");
		expect(rows[2]).toHaveTextContent("Run by hand");
		expect(rows[3]).toHaveTextContent("Unused volumes");
		expect(rows[4]).toHaveTextContent("Running");
		expect(rows[5]).toHaveTextContent(/Failed$/);
		expect(rows[6]).toHaveTextContent("Failed · Freed 2 MiB · 3 items removed");
	});

	test("a run with steps expands to each step's outcome, errors and removed names", async () => {
		const user = userEvent.setup();
		await renderSection(
			cleanup({
				runs: [
					run({
						steps: [
							step({
								name: "images_unused",
								items: 5,
								reclaimedBytes: 1024 * 1024,
								protected: 2,
								removed: ["nginx:1", "redis:6"],
							}),
							step({
								name: "build_cache",
								items: 0,
								skipped: "builder busy",
							}),
							step({
								name: "networks",
								items: 1,
								error: "network in use",
							}),
						],
					}),
				],
			}),
		);
		const row = screen.getByRole("listitem");
		const toggle = within(row).getByRole("button");
		expect(toggle).toHaveAttribute("aria-expanded", "false");
		await user.click(toggle);
		expect(toggle).toHaveAttribute("aria-expanded", "true");
		expect(within(row).getByText("Unused images")).toBeVisible();
		expect(
			within(row).getByText("5 items removed · 1 MiB · 2 kept for releases"),
		).toBeVisible();
		expect(within(row).getByText("nginx:1, redis:6 and 3 more")).toBeVisible();
		expect(within(row).getByText("Build cache")).toBeVisible();
		expect(within(row).getByText("builder busy")).toBeVisible();
		expect(within(row).getByText("Networks")).toBeVisible();
		expect(within(row).getByText("network in use")).toBeVisible();
	});

	test("a failed run without steps still expands to its error", async () => {
		const user = userEvent.setup();
		await renderSection(
			cleanup({
				runs: [
					run({ status: "failed", error: "agent lost connection", steps: [] }),
					run({ id: "plain", steps: [] }),
				],
			}),
		);
		const [failed, plain] = screen.getAllByRole("listitem");
		expect(within(plain as HTMLElement).getByRole("button")).toBeDisabled();
		const toggle = within(failed as HTMLElement).getByRole("button");
		expect(toggle).toBeEnabled();
		await user.click(toggle);
		expect(
			within(failed as HTMLElement).getByText("agent lost connection"),
		).toBeVisible();
	});
});

describe("CleanupSection: unused volumes", () => {
	test("needs the server's name typed before holding removes anything", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			[GET]: { body: cleanup() },
			[VOLUMES]: {
				status: 202,
				body: run({ mode: "volumes", status: "pending", trigger: "manual" }),
			},
		});
		await renderWithProviders(<CleanupSection server={server()} />);
		await user.click(
			await screen.findByRole("button", { name: "Remove unused volumes…" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Remove unused volumes on edge-1?"),
		).toBeVisible();
		const holdButton = within(dialog).getByRole("button", {
			name: /Hold to remove unused volumes/,
		});
		await hold(holdButton);
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);

		await user.type(
			within(dialog).getByRole("textbox", { name: /Type edge-1 to confirm/ }),
			"edge-1",
		);
		await hold(holdButton);
		await waitFor(() =>
			expect(api.calls.filter((c) => c.method === "POST")).toHaveLength(1),
		);
		const post = api.calls.find((c) => c.method === "POST");
		expect(post?.path).toBe("/servers/srv-1/cleanup/volumes");
		expect(post?.body).toEqual({ confirm: "edge-1" });
		expect(
			await screen.findByText("Removing unused volumes"),
		).toBeInTheDocument();
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
	});

	test("a refused removal keeps the dialog open with the reason", async () => {
		const user = userEvent.setup();
		stubApi({
			[GET]: { body: cleanup() },
			[VOLUMES]: problem(409, {
				title: "Conflict",
				detail: "a cleanup is running",
			}),
		});
		await renderWithProviders(<CleanupSection server={server()} />);
		await user.click(
			await screen.findByRole("button", { name: "Remove unused volumes…" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.type(
			within(dialog).getByRole("textbox", { name: /Type edge-1 to confirm/ }),
			"edge-1",
		);
		await hold(
			within(dialog).getByRole("button", {
				name: /Hold to remove unused volumes/,
			}),
		);
		expect(
			await within(dialog).findByText(/a cleanup is running/),
		).toBeVisible();
		expect(screen.queryByText("Removing unused volumes")).toBeNull();
	});

	test("cancelling the dialog sends nothing", async () => {
		const user = userEvent.setup();
		const api = await renderSection();
		await user.click(
			screen.getByRole("button", { name: "Remove unused volumes…" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);
	});
});
