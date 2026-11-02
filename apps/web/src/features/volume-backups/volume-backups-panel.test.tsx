import { describe, expect, setDefaultTimeout, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	DestinationView,
	VolumeBackupConfigView,
	VolumeBackupView,
	VolumeRestoreView,
} from "#/api/gen/types.gen";
import { type ApiRoutes, type ApiStub, stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { VolumeBackupsPanel } from "./volume-backups-panel";

setDefaultTimeout(30_000);

const CONFIG_URL = "GET /services/:serviceId/volume-backup";
const BACKUPS_URL = "GET /services/:serviceId/volume-backups";
const RESTORES_URL = "GET /services/:serviceId/volume-restores";
const DEST_URL = "GET /backup-destinations";

const DESTINATION = {
	id: "dest-1",
	name: "Offsite S3",
	kind: "s3",
	offSite: true,
	accessKeySet: true,
	secretKeySet: true,
	compatMode: false,
	pathStyle: false,
	createdAt: "2026-01-01T00:00:00Z",
	inUseBySchedules: 0,
	storedBackups: 0,
} as DestinationView;

function config(
	over: Partial<VolumeBackupConfigView> = {},
): VolumeBackupConfigView {
	return {
		configured: true,
		eligible: true,
		serviceId: "svc-1",
		destinationId: "dest-1",
		cron: "0 3 * * *",
		enabled: true,
		excludes: null,
		freezeBudgetSeconds: 5,
		keepDaily: 7,
		keepWeekly: 4,
		keepMonthly: 6,
		mode: "freeze",
		repoReady: true,
		timezone: "UTC",
		volumes: [{ id: "v1", name: "data", mountPath: "/data" }],
		...over,
	};
}

function run(over: Partial<VolumeBackupView> = {}): VolumeBackupView {
	return {
		id: "b-1",
		serviceId: "svc-1",
		destinationId: "dest-1",
		destinationName: "Offsite S3",
		destinationKind: "s3",
		createdAt: new Date(Date.now() - 3_600_000).toISOString(),
		status: "succeeded",
		trigger: "manual",
		mode: "freeze",
		bytesAdded: 1024,
		files: 10,
		freezeMs: 600,
		stageMs: 100,
		sizeBytes: 5 * 1024 * 1024,
		snapshotId: "abcdef1234567890",
		locked: false,
		objectDeleted: false,
		restorable: true,
		volumes: ["data"],
		warnings: null,
		...over,
	};
}

function restore(over: Partial<VolumeRestoreView> = {}): VolumeRestoreView {
	return {
		id: "r-1",
		serviceId: "svc-1",
		backupId: "b-1",
		createdAt: new Date(Date.now() - 600_000).toISOString(),
		mode: "new_volume",
		status: "succeeded",
		swapStaged: false,
		volumeName: "data",
		...over,
	};
}

interface Setup {
	config?: VolumeBackupConfigView;
	backups?: VolumeBackupView[];
	restores?: VolumeRestoreView[];
	destinations?: DestinationView[];
	extra?: ApiRoutes;
}

function setup(s: Setup = {}): ApiStub {
	return stubApi({
		[CONFIG_URL]: { body: s.config ?? config() },
		[BACKUPS_URL]: {
			body: make("VolumeBackupListOutputBody", { items: s.backups ?? [] }),
		},
		[RESTORES_URL]: { body: { items: s.restores ?? [] } },
		[DEST_URL]: { body: { items: s.destinations ?? [DESTINATION] } },
		...s.extra,
	});
}

async function renderPanel(stagedCount = 0) {
	return renderWithProviders(
		<VolumeBackupsPanel
			serviceId="svc-1"
			serviceSlug="shop"
			stagedCount={stagedCount}
		/>,
	);
}

/** The history table (the uptime bar above it is a table too); its run rows follow the header. */
async function historyRuns(): Promise<HTMLElement[]> {
	const head = await screen.findByRole("columnheader", { name: "Snapshot" });
	const table = head.closest("table");
	if (!table) throw new Error("history table missing");
	return within(table).getAllByRole("row").slice(1);
}

describe("VolumeBackupsPanel: loading and failures", () => {
	test("requests the config, history, restores and destinations of the service", async () => {
		const api = setup();
		await renderPanel();
		await screen.findByRole("button", { name: "Back up now" });
		const get = (path: string) => api.calls.find((c) => c.path === path);
		expect(get("/services/svc-1/volume-backup")).toBeDefined();
		expect(get("/services/svc-1/volume-backups")?.query.get("limit")).toBe(
			"50",
		);
		expect(get("/services/svc-1/volume-restores")).toBeDefined();
		expect(get("/backup-destinations")).toBeDefined();
		expect(api.unmatched).toEqual([]);
	});

	test("shows nothing of the panel while the first answers are pending", async () => {
		stubApi({
			[CONFIG_URL]: () => new Promise<Response>(() => {}),
			[BACKUPS_URL]: {
				body: make("VolumeBackupListOutputBody", { items: [] }),
			},
			[RESTORES_URL]: { body: { items: [] } },
			[DEST_URL]: { body: { items: [] } },
		});
		await renderPanel();
		expect(screen.queryByText("Volume backups")).toBeNull();
		expect(screen.queryByRole("alert")).toBeNull();
		expect(screen.queryByRole("button", { name: "Back up now" })).toBeNull();
	});

	test("a failing config call shows the error state, and Try again recovers", async () => {
		const user = userEvent.setup();
		let healthy = false;
		const api = stubApi({
			[CONFIG_URL]: () =>
				healthy
					? Response.json(config())
					: Response.json({ title: "Down", detail: "no" }, { status: 503 }),
			[BACKUPS_URL]: {
				body: make("VolumeBackupListOutputBody", { items: [] }),
			},
			[RESTORES_URL]: { body: { items: [] } },
			[DEST_URL]: { body: { items: [DESTINATION] } },
		});
		await renderPanel();
		const alert = await screen.findByRole("alert");
		expect(alert).toHaveTextContent("Volume backups did not load");
		expect(alert).toHaveTextContent("nothing was changed");
		expect(screen.queryByRole("button", { name: "Back up now" })).toBeNull();

		healthy = true;
		await user.click(screen.getByRole("button", { name: "Try again" }));
		expect(
			await screen.findByRole("button", { name: "Back up now" }),
		).toBeVisible();
		expect(screen.queryByRole("alert")).toBeNull();
		expect(
			api.calls.filter((c) => c.path === "/services/svc-1/volume-backup"),
		).toHaveLength(2);
	});

	test("a failing history call shows the error state too", async () => {
		setup({
			extra: {
				[BACKUPS_URL]: problem(500, { title: "Boom" }),
			},
		});
		await renderPanel();
		expect(await screen.findByRole("alert")).toHaveTextContent(
			"Volume backups did not load",
		);
	});
});

describe("VolumeBackupsPanel: eligibility", () => {
	test("an ineligible service says why and offers no form", async () => {
		setup({
			config: config({
				eligible: false,
				ineligibleReason: "The server is a remote host without an agent.",
			}),
		});
		await renderPanel();
		expect(
			await screen.findByText("Volume backups are not available"),
		).toBeVisible();
		expect(
			screen.getByText("The server is a remote host without an agent."),
		).toBeVisible();
		expect(screen.queryByRole("button", { name: "Back up now" })).toBeNull();
		expect(screen.queryByRole("button", { name: /Save|Turn on/ })).toBeNull();
	});

	test("without a reason it falls back to the general requirement", async () => {
		setup({ config: config({ eligible: false }) });
		await renderPanel();
		expect(
			await screen.findByText(
				"This service needs a server and at least one volume.",
			),
		).toBeVisible();
	});
});

describe("VolumeBackupsPanel: summary line and Back up now", () => {
	test("an unconfigured service must save settings before it can back up", async () => {
		setup({ config: config({ configured: false, cron: "", enabled: false }) });
		await renderPanel();
		const button = await screen.findByRole("button", { name: "Back up now" });
		expect(button).toBeDisabled();
		expect(button).toHaveAttribute("title", "Save the settings below first");
		expect(
			screen.getByText(
				"No backups yet. Save the settings above, then press Back up now.",
			),
		).toBeVisible();
		expect(screen.queryByText(/Keep (everything|\d+ )/)).toBeNull();
		expect(
			screen.getByRole("button", { name: "Turn on volume backups" }),
		).toBeVisible();
	});

	test("a scheduled config summarises the schedule, next run and retention", async () => {
		setup({
			config: config({
				nextRunAt: new Date(Date.now() + 7_200_000).toISOString(),
				lastRunAt: new Date(Date.now() - 86_400_000).toISOString(),
			}),
		});
		await renderPanel();
		const line = await screen.findByText(/Every day at 03:00 · UTC/, {
			selector: "p",
		});
		expect(line).toHaveTextContent("next run");
		expect(line).toHaveTextContent("last run");
		expect(line).toHaveTextContent("Keep 7 daily, 4 weekly, 6 monthly");
		expect(line).not.toHaveTextContent("paused");
		expect(screen.getByRole("button", { name: "Back up now" })).toBeEnabled();
		expect(
			screen.getByText(
				"No backups yet. Press Back up now, or set a schedule above.",
			),
		).toBeVisible();
	});

	test("a paused schedule says so instead of naming the next run", async () => {
		setup({
			config: config({
				enabled: false,
				nextRunAt: new Date(Date.now() + 7_200_000).toISOString(),
			}),
		});
		await renderPanel();
		const line = await screen.findByText(/Every day at 03:00/, {
			selector: "p",
		});
		expect(line).toHaveTextContent("paused");
		expect(line).not.toHaveTextContent("next run");
	});

	test("a manual-only config shows no schedule markers", async () => {
		setup({
			config: config({
				cron: "",
				keepDaily: 0,
				keepWeekly: 0,
				keepMonthly: 0,
			}),
		});
		await renderPanel();
		const line = await screen.findByText(/Manual only · /, { selector: "p" });
		expect(line).toHaveTextContent("Manual only · Keep everything");
		expect(line).not.toHaveTextContent("paused");
		expect(line).not.toHaveTextContent("next run");
	});

	test("Back up now POSTs an empty body and announces the start", async () => {
		const user = userEvent.setup();
		const api = setup({
			extra: {
				"POST /services/:serviceId/volume-backups": {
					status: 202,
					body: run({ status: "queued" }),
				},
			},
		});
		await renderPanel();
		await user.click(
			await screen.findByRole("button", { name: "Back up now" }),
		);
		expect(await screen.findByText("Backup started")).toBeVisible();
		const posts = api.calls.filter((c) => c.method === "POST");
		expect(posts).toHaveLength(1);
		expect(posts[0]).toMatchObject({
			path: "/services/svc-1/volume-backups",
			body: {},
		});
		// The history is refetched so the new run shows up.
		await waitFor(() =>
			expect(
				api.calls.filter(
					(c) =>
						c.path === "/services/svc-1/volume-backups" && c.method === "GET",
				).length,
			).toBeGreaterThan(1),
		);
	});

	test("a refused backup shows an error toast with the reason", async () => {
		const user = userEvent.setup();
		setup({
			extra: {
				"POST /services/:serviceId/volume-backups": problem(409, {
					title: "Conflict",
					detail: "A backup is already running.",
				}),
			},
		});
		await renderPanel();
		await user.click(
			await screen.findByRole("button", { name: "Back up now" }),
		);
		expect(await screen.findByText("Could not start the backup")).toBeVisible();
		expect(screen.getByText("A backup is already running.")).toBeVisible();
		expect(screen.queryByText("Backup started")).toBeNull();
	});
});

describe("VolumeBackupsPanel: history", () => {
	test("lists each run with its snapshot, destination and size", async () => {
		setup({
			backups: [
				run(),
				run({
					id: "b-2",
					status: "failed",
					error: "restic exited 1",
					snapshotId: "",
					restorable: false,
					sizeBytes: 0,
				}),
			],
		});
		await renderPanel();
		const rows = await historyRuns();
		expect(rows).toHaveLength(2);
		expect(rows[0]).toHaveTextContent("abcdef12");
		expect(rows[0]).toHaveTextContent("Offsite S3");
		expect(rows[0]).toHaveTextContent("5 MiB");
		expect(rows[1]).toHaveTextContent("Failed");
		expect(rows[1]).not.toHaveTextContent("Restore");
		expect(screen.queryByText(/No backups yet/)).toBeNull();
	});

	test("deleting a run asks first and then DELETEs that backup", async () => {
		const user = userEvent.setup();
		const api = setup({
			backups: [run(), run({ id: "b-2", snapshotId: "ffffffffffff" })],
			extra: { "DELETE /volume-backups/:backupId": { status: 204 } },
		});
		await renderPanel();
		const second = (await historyRuns())[1];
		if (!second) throw new Error("second run missing");
		await user.click(
			within(second).getByRole("button", { name: "Delete backup" }),
		);

		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent("Delete this backup?");
		expect(dialog).toHaveTextContent("5 MiB of files");
		expect(dialog).toHaveTextContent("removed from Offsite S3");
		expect(api.calls.filter((c) => c.method === "DELETE")).toEqual([]);

		await user.click(
			within(dialog).getByRole("button", { name: "Delete backup" }),
		);
		expect(await screen.findByText("Deleting the stored backup")).toBeVisible();
		const deletes = api.calls.filter((c) => c.method === "DELETE");
		expect(deletes).toHaveLength(1);
		expect(deletes[0]?.path).toBe("/volume-backups/b-2");
	});

	test("a failed delete leaves the dialog open and raises an error toast", async () => {
		const user = userEvent.setup();
		setup({
			backups: [run()],
			extra: {
				"DELETE /volume-backups/:backupId": problem(409, {
					title: "Conflict",
					detail: "Backup is being restored.",
				}),
			},
		});
		await renderPanel();
		await historyRuns();
		await user.click(screen.getByRole("button", { name: "Delete backup" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Delete backup" }),
		);
		expect(
			await screen.findByText("Could not delete the backup"),
		).toBeVisible();
		expect(screen.queryByText("Deleting the stored backup")).toBeNull();
		expect(screen.getByRole("alertdialog")).toBeVisible();
	});

	test("Lock sends locked=true, Unlock sends locked=false", async () => {
		const user = userEvent.setup();
		const api = setup({
			backups: [
				run({ id: "b-open", locked: false }),
				run({ id: "b-held", locked: true, snapshotId: "99999999aaaa" }),
			],
			extra: {
				"PUT /volume-backups/:backupId/lock": { body: run({ locked: true }) },
			},
		});
		await renderPanel();
		await historyRuns();
		await user.click(screen.getByRole("button", { name: "Lock backup" }));
		await user.click(screen.getByRole("button", { name: "Unlock backup" }));
		await waitFor(() =>
			expect(api.calls.filter((c) => c.method === "PUT")).toHaveLength(2),
		);
		const puts = api.calls.filter((c) => c.method === "PUT");
		expect(puts[0]).toMatchObject({
			path: "/volume-backups/b-open/lock",
			body: { locked: true },
		});
		expect(puts[1]).toMatchObject({
			path: "/volume-backups/b-held/lock",
			body: { locked: false },
		});
	});

	test("a refused lock change raises an error toast", async () => {
		const user = userEvent.setup();
		setup({
			backups: [run()],
			extra: {
				"PUT /volume-backups/:backupId/lock": problem(500, {
					title: "Boom",
					detail: "Could not write.",
				}),
			},
		});
		await renderPanel();
		await historyRuns();
		await user.click(screen.getByRole("button", { name: "Lock backup" }));
		expect(await screen.findByText("Could not change the lock")).toBeVisible();
	});

	test("Restore opens the restore dialog for that backup", async () => {
		const user = userEvent.setup();
		setup({ backups: [run()] });
		await renderPanel();
		await historyRuns();
		await user.click(screen.getByRole("button", { name: "Restore" }));
		const dialog = await screen.findByRole("dialog");
		expect(dialog).toHaveTextContent("Restore volume");
	});

	test("Browse files opens the file browser for that backup", async () => {
		const user = userEvent.setup();
		const api = setup({
			backups: [run()],
			extra: {
				"GET /volume-backups/:backupId/files": {
					body: make("VolumeFileListing", { entries: [], truncated: false }),
				},
			},
		});
		await renderPanel();
		await historyRuns();
		await user.click(screen.getByRole("button", { name: "Browse files" }));
		const dialog = await screen.findByRole("dialog");
		expect(dialog).toHaveTextContent("Browse files");
		await waitFor(() =>
			expect(
				api.calls.some((c) => c.path === "/volume-backups/b-1/files"),
			).toBe(true),
		);
	});

	test("a run still in flight offers no delete", async () => {
		setup({ backups: [run({ status: "running", snapshotId: "" })] });
		await renderPanel();
		await historyRuns();
		expect(screen.queryByRole("button", { name: "Delete backup" })).toBeNull();
	});
});

describe("VolumeBackupsPanel: restores", () => {
	test("no restores, no Restores section", async () => {
		setup({ backups: [run()] });
		await renderPanel();
		await historyRuns();
		expect(
			screen.queryByText("Recent restores of this service's volumes."),
		).toBeNull();
	});

	test("lists restores with their status, mode, actor and failure reason", async () => {
		setup({
			backups: [run()],
			restores: [
				restore({ id: "r-ok", actor: "ada@example.com" }),
				restore({
					id: "r-bad",
					status: "failed",
					mode: "in_place",
					error: "agent offline during restore",
				}),
			],
		});
		await renderPanel();
		expect(await screen.findByText("Restored")).toBeVisible();
		expect(
			screen.getByText("Failed", { selector: "[data-slot=status-pill]" }),
		).toBeVisible();
		expect(screen.getByText(/Into a new volume/)).toHaveTextContent(
			"ada@example.com",
		);
		expect(screen.getByText(/In place/)).toBeVisible();
		expect(screen.getByText("agent offline during restore")).toBeVisible();
	});

	test("a staged swap points at Deploy with the staged count and the backup's time", async () => {
		setup({
			backups: [run({ createdAt: "2026-10-30T10:00:00Z" })],
			restores: [restore({ swapStaged: true })],
		});
		await renderPanel(3);
		expect(
			await screen.findByText("Restored, waiting for Deploy"),
		).toBeVisible();
		const pointer = screen.getByText(/Restore of data from .* is staged/);
		expect(pointer).toHaveTextContent("3 changes to apply");
		expect(pointer).toHaveTextContent("Press Deploy · 3 in the top bar");
	});

	test("a staged swap whose backup is no longer listed still names the volume", async () => {
		setup({
			backups: [run({ id: "other" })],
			restores: [restore({ swapStaged: true, backupId: "gone" })],
		});
		await renderPanel(0);
		const pointer = await screen.findByText(/Restore of data is staged/);
		expect(pointer).toHaveTextContent("1 change to apply");
	});
});

describe("VolumeBackupsPanel: destinations", () => {
	test("Add in the form opens the destination dialog", async () => {
		const user = userEvent.setup();
		setup();
		await renderPanel();
		await user.click(await screen.findByRole("button", { name: "Add" }));
		const dialog = await screen.findByRole("dialog");
		expect(dialog).toHaveTextContent("Add backup destination");
	});
});

describe("VolumeBackupsPanel: settings form wiring", () => {
	test("saving settings PUTs to this service", async () => {
		const user = userEvent.setup();
		const api = setup({
			config: config({ configured: false, cron: "", enabled: false }),
			extra: {
				"PUT /services/:serviceId/volume-backup": { body: config() },
			},
		});
		await renderPanel();
		await user.click(
			await screen.findByRole("button", { name: "Turn on volume backups" }),
		);
		expect(
			await screen.findByText("Volume backup settings saved"),
		).toBeVisible();
		const put = api.calls.find((c) => c.method === "PUT");
		expect(put?.path).toBe("/services/svc-1/volume-backup");
		expect(put?.body).toMatchObject({
			destinationId: "dest-1",
			mode: "freeze",
		});
	});
});
