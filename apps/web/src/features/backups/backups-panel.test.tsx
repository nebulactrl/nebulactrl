import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	BackupView,
	DestinationView,
	ScheduleView,
} from "#/api/gen/types.gen";
import { type ApiRoutes, stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { BackupsPanel } from "./backups-panel";
import { backup, destination, schedule } from "./backups-panel.fixture";

interface World {
	destinations: DestinationView[];
	backups: BackupView[];
	schedules: ScheduleView[];
	restores: unknown[];
	dumpRestores: unknown[];
	extra?: ApiRoutes;
}

/** Stubs the five lists the panel reads, plus any mutation routes in `extra`. */
function stubWorld(world: Partial<World> = {}) {
	const w: World = {
		destinations: [destination()],
		backups: [],
		schedules: [],
		restores: [],
		dumpRestores: [],
		...world,
	};
	return stubApi({
		"GET /backup-destinations": () => Response.json({ items: w.destinations }),
		"GET /services/:id/backups": () => Response.json({ items: w.backups }),
		"GET /services/:id/backup-schedules": () =>
			Response.json({ items: w.schedules }),
		"GET /services/:id/backup-restores": () =>
			Response.json({ items: w.restores }),
		"GET /services/:id/dump-restores": () =>
			Response.json({ items: w.dumpRestores }),
		...w.extra,
	});
}

async function renderPanel(engine = "postgres") {
	return renderWithProviders(
		<BackupsPanel
			serviceId="s1"
			serviceSlug="orders-db"
			engine={engine}
			serverId="srv1"
		/>,
	);
}

/** The table row that holds `text`. */
async function rowWith(text: string | RegExp): Promise<HTMLElement> {
	const cell = await screen.findByRole("cell", { name: text });
	const row = cell.closest("tr");
	if (!row) throw new Error("not in a row");
	return row;
}

/** The run history table (the history strip carries a screen-reader table too). */
function runTable(): HTMLElement {
	const table = screen
		.getAllByRole("table")
		.find((t) => within(t).queryByRole("columnheader", { name: "When" }));
	if (!table) throw new Error("no run table");
	return table;
}

const hours = (n: number) => new Date(Date.now() - n * 3600_000).toISOString();

describe("BackupsPanel states", () => {
	test("shows nothing of the panel until the lists have loaded", async () => {
		let release: (() => void) | undefined;
		const gate = new Promise<void>((r) => {
			release = r;
		});
		stubWorld({
			extra: {
				"GET /services/:id/backups": async () => {
					await gate;
					return Response.json({ items: [] });
				},
			},
		});
		await renderPanel();
		expect(
			screen.queryByRole("button", { name: /Back up now/ }),
		).not.toBeInTheDocument();
		release?.();
		expect(
			await screen.findByRole("button", { name: /Back up now/ }),
		).toBeInTheDocument();
	});

	test("an unreachable control plane shows an error that Try again recovers from", async () => {
		let fail = true;
		stubWorld({
			extra: {
				"GET /services/:id/backups": () =>
					fail
						? Response.json({ title: "boom", status: 500 }, { status: 500 })
						: Response.json({ items: [backup()] }),
			},
		});
		await renderPanel();
		const alert = await screen.findByRole("alert");
		expect(alert).toHaveTextContent("Backups did not load");
		expect(alert).toHaveTextContent("nothing was changed");
		expect(
			screen.queryByRole("columnheader", { name: "When" }),
		).not.toBeInTheDocument();

		fail = false;
		await userEvent
			.setup()
			.click(within(alert).getByRole("button", { name: "Try again" }));
		expect(
			await screen.findByRole("columnheader", { name: "When" }),
		).toBeInTheDocument();
		expect(screen.queryByRole("alert")).not.toBeInTheDocument();
	});

	test("without a destination backups are blocked and adding one is offered", async () => {
		const api = stubWorld({ destinations: [] });
		await renderPanel();
		const user = userEvent.setup();

		expect(
			await screen.findByRole("button", { name: /Add destination/ }),
		).toBeInTheDocument();
		expect(screen.getByRole("button", { name: /Back up now/ })).toBeDisabled();
		const addSchedule = screen.getByRole("button", { name: /Add schedule/ });
		expect(addSchedule).toBeDisabled();
		expect(addSchedule).toHaveAttribute(
			"title",
			"Add a backup destination first",
		);
		expect(
			screen.getByRole("link", { name: /Backup storage/ }),
		).toHaveAttribute("href", "/settings/backup-storage");
		expect(
			screen.queryByRole("columnheader", { name: "When" }),
		).not.toBeInTheDocument();
		expect(screen.queryByText(/No backups yet/)).not.toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: /Add destination/ }));
		expect(await screen.findByRole("dialog")).toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});

	test("with a destination but no runs it says there are no backups and no schedule", async () => {
		const api = stubWorld();
		await renderPanel();
		expect(await screen.findByText(/No backups yet/)).toBeInTheDocument();
		expect(screen.getByText(/No schedule\. Add one/)).toBeInTheDocument();
		expect(
			screen.queryByRole("columnheader", { name: "When" }),
		).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: /Back up now/ })).toBeEnabled();
		expect(screen.getByRole("button", { name: /Add schedule/ })).toBeEnabled();
		expect(screen.queryByText("Restores")).not.toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});

	test("requests the newest 50 runs of the service", async () => {
		const api = stubWorld();
		await renderPanel();
		await screen.findByText(/No backups yet/);
		const call = api.calls.find((c) => c.path === "/services/s1/backups");
		expect(call?.query.get("limit")).toBe("50");
		expect(api.calls.map((c) => c.path)).toEqual(
			expect.arrayContaining([
				"/backup-destinations",
				"/services/s1/backup-schedules",
				"/services/s1/backup-restores",
				"/services/s1/dump-restores",
			]),
		);
	});
});

describe("BackupsPanel run list", () => {
	test("each status reads as its own label, and only finished ones show a size", async () => {
		stubWorld({
			backups: [
				backup({ id: "ok", status: "succeeded", sizeBytes: 2048 }),
				backup({
					id: "bad",
					status: "failed",
					sizeBytes: 0,
					error: "pg_dump: connection refused",
					createdAt: hours(1),
				}),
				backup({
					id: "q",
					status: "queued",
					sizeBytes: 999,
					createdAt: hours(2),
				}),
				backup({
					id: "r",
					status: "running",
					sizeBytes: 999,
					createdAt: hours(3),
				}),
				backup({
					id: "sk",
					status: "skipped",
					sizeBytes: 999,
					createdAt: hours(4),
				}),
				backup({
					id: "ex",
					status: "expired",
					sizeBytes: 999,
					createdAt: hours(5),
				}),
			],
		});
		await renderPanel();
		await screen.findByText("Queued");
		const rows = within(runTable()).getAllByRole("row").slice(1);
		const text = rows.map((r) => r.textContent ?? "");
		expect(rows).toHaveLength(6);
		expect(text[0]).toContain("Succeeded");
		expect(text[0]).toContain("2 KiB");
		expect(text[1]).toContain("Failed");
		expect(text[1]).toContain("pg_dump: connection refused");
		expect(text[2]).toContain("Queued");
		expect(text[3]).toContain("Running");
		expect(text[4]).toContain("Skipped");
		expect(text[5]).toContain("Not delivered");
		// Unfinished/failed rows never show a size, whatever the field says.
		for (const t of text.slice(1)) expect(t).not.toMatch(/KB|MB|B$/);
		for (const t of text.slice(2)) expect(t).toContain("–");
	});

	test("labels how each run started and flags destinations on the server's own disk", async () => {
		stubWorld({
			backups: [
				backup({
					id: "a",
					trigger: "manual",
					destinationName: "Offsite bucket",
				}),
				backup({
					id: "b",
					trigger: "schedule",
					destinationKind: "local",
					destinationName: "Server disk",
					createdAt: hours(1),
				}),
				backup({ id: "c", trigger: "pre_restore", createdAt: hours(2) }),
			],
		});
		await renderPanel();
		const manual = await rowWith(/Manual/);
		expect(within(manual).queryByText("not off-site")).not.toBeInTheDocument();
		const scheduled = await rowWith(/Scheduled/);
		expect(within(scheduled).getByText("not off-site")).toBeInTheDocument();
		expect(scheduled).toHaveTextContent("Server disk");
		expect(await rowWith(/Before restore/)).toBeInTheDocument();
	});

	test("Delete is hidden while a run is in flight or once its object is gone", async () => {
		stubWorld({
			backups: [
				backup({ id: "ok", status: "succeeded" }),
				backup({ id: "q", status: "queued", createdAt: hours(1) }),
				backup({ id: "r", status: "running", createdAt: hours(2) }),
				backup({
					id: "gone",
					status: "succeeded",
					objectDeleted: true,
					createdAt: hours(3),
				}),
				backup({ id: "bad", status: "failed", createdAt: hours(4) }),
			],
		});
		await renderPanel();
		await screen.findByRole("columnheader", { name: "When" });
		const rows = within(runTable()).getAllByRole("row").slice(1);
		const hasDelete = rows.map(
			(r) => within(r).queryByRole("button", { name: "Delete" }) !== null,
		);
		// succeeded, queued, running, succeeded-but-deleted, failed
		expect(hasDelete).toEqual([true, false, false, false, true]);
	});

	test("Restore and Lock need a finished, restorable backup", async () => {
		stubWorld({
			backups: [
				backup({ id: "ok" }),
				backup({ id: "nr", restorable: false, createdAt: hours(1) }),
				backup({ id: "bad", status: "failed", createdAt: hours(2) }),
			],
		});
		await renderPanel();
		await screen.findByRole("columnheader", { name: "When" });
		const rows = within(runTable()).getAllByRole("row").slice(1);
		const [ok, notRestorable, failed] = rows as [
			HTMLElement,
			HTMLElement,
			HTMLElement,
		];
		expect(
			within(ok).getByRole("button", { name: "Restore" }),
		).toBeInTheDocument();
		expect(
			within(ok).getByRole("button", { name: "Lock backup" }),
		).toBeInTheDocument();
		for (const r of [notRestorable, failed]) {
			expect(within(r).queryByRole("button", { name: "Restore" })).toBeNull();
			expect(
				within(r).queryByRole("button", { name: /lock backup/i }),
			).toBeNull();
		}
	});

	test("an engine without restore support explains the RDB file and hides restore entry points", async () => {
		stubWorld({ backups: [backup({ engine: "valkey" })] });
		await renderPanel("valkey");
		const row = await rowWith(/Manual/);
		expect(within(row).queryByRole("button", { name: "Restore" })).toBeNull();
		// Lock and Delete stay available.
		expect(
			within(row).getByRole("button", { name: "Lock backup" }),
		).toBeInTheDocument();
		expect(
			within(row).getByRole("button", { name: "Delete" }),
		).toBeInTheDocument();
		expect(screen.getByText(/RDB snapshots/)).toBeInTheDocument();
		expect(screen.getByText("dump.rdb")).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: /Restore from a file/ }),
		).not.toBeInTheDocument();
	});

	test("a restorable engine offers restore from a file and no RDB note", async () => {
		stubWorld({ backups: [backup()] });
		await renderPanel("postgres");
		await screen.findByRole("columnheader", { name: "When" });
		expect(
			screen.getByRole("button", { name: /Restore from a file/ }),
		).toBeInTheDocument();
		expect(screen.queryByText(/RDB snapshots/)).not.toBeInTheDocument();
	});

	test("Restore from a file opens the dump restore dialog", async () => {
		stubWorld({ backups: [backup()] });
		await renderPanel();
		await userEvent
			.setup()
			.click(
				await screen.findByRole("button", { name: /Restore from a file/ }),
			);
		expect(
			await screen.findByRole("heading", { name: "Restore from a dump file" }),
		).toBeInTheDocument();
	});

	test("Restore opens the restore dialog for that backup", async () => {
		stubWorld({
			backups: [backup({ id: "b9", database: "orders" })],
			extra: { "GET /servers": { body: { items: [] } } },
		});
		await renderPanel();
		await userEvent
			.setup()
			.click(await screen.findByRole("button", { name: "Restore" }));
		const dialog = await screen.findByRole("dialog");
		expect(within(dialog).getByText("Restore backup")).toBeInTheDocument();
		expect(dialog).toHaveTextContent("from orders");
	});
});

describe("BackupsPanel history strip", () => {
	/** The strip's screen-reader table: one row per day, oldest first. */
	function historyRows(): HTMLElement[] {
		const table = screen
			.getAllByRole("table")
			.find((t) => within(t).queryByRole("columnheader", { name: "Date" }));
		if (!table) throw new Error("no history table");
		return within(table).getAllByRole("row").slice(1);
	}

	test("covers the last 30 days and reports today's runs", async () => {
		const now = new Date().toISOString();
		stubWorld({
			backups: [
				backup({ id: "a", status: "failed", createdAt: now }),
				backup({ id: "b", status: "succeeded", createdAt: now }),
			],
		});
		await renderPanel();
		await screen.findByRole("columnheader", { name: "When" });
		const rows = historyRows();
		expect(rows).toHaveLength(30);
		const today = rows[29];
		expect(today).toHaveTextContent("Failed");
		expect(today).toHaveTextContent("1 succeeded, 1 failed");
		expect(rows[0]).toHaveTextContent("No backup");
		expect(
			screen.getByRole("group", { name: "Backups, last 30 days" }),
		).toBeInTheDocument();
	});

	test("a full page of runs marks the older days as not loaded instead of empty", async () => {
		const now = new Date().toISOString();
		stubWorld({
			backups: Array.from({ length: 50 }, (_, i) =>
				backup({ id: `b${i}`, createdAt: now }),
			),
		});
		await renderPanel();
		await screen.findByRole("columnheader", { name: "When" });
		expect(historyRows()[0]).toHaveTextContent("Older runs are not loaded");
	});

	test("no strip is drawn without runs", async () => {
		stubWorld();
		await renderPanel();
		await screen.findByText(/No backups yet/);
		expect(
			screen.queryByRole("group", { name: "Backups, last 30 days" }),
		).not.toBeInTheDocument();
	});
});

describe("BackupsPanel lock", () => {
	test("Lock sends locked=true and refreshes the list to show Locked", async () => {
		let locked = false;
		const api = stubWorld({
			extra: {
				"GET /services/:id/backups": () =>
					Response.json({ items: [backup({ locked })] }),
				"PUT /backups/:id/lock": async (req) => {
					const body: { locked: boolean } = await req.json();
					locked = body.locked;
					return Response.json({});
				},
			},
		});
		await renderPanel();
		const user = userEvent.setup();
		await user.click(
			await screen.findByRole("button", { name: "Lock backup" }),
		);
		const unlock = await screen.findByRole("button", { name: "Unlock backup" });
		expect(unlock).toHaveTextContent("Locked");
		expect(unlock).toHaveAttribute(
			"title",
			"Locked: retention never removes it",
		);
		expect(api.calls.find((c) => c.method === "PUT")).toMatchObject({
			path: "/backups/b1/lock",
			body: { locked: true },
		});

		await user.click(unlock);
		expect(
			await screen.findByRole("button", { name: "Lock backup" }),
		).toBeInTheDocument();
		const puts = api.calls.filter((c) => c.method === "PUT");
		expect(puts.map((c) => c.body)).toEqual([
			{ locked: true },
			{ locked: false },
		]);
	});

	test("a rejected lock is reported and the row is unchanged", async () => {
		stubWorld({
			backups: [backup()],
			extra: {
				"PUT /backups/:id/lock": {
					status: 409,
					body: {
						status: 409,
						code: "conflict",
						title: "Conflict",
						detail: "backup is being deleted",
					},
				},
			},
		});
		await renderPanel();
		await userEvent
			.setup()
			.click(await screen.findByRole("button", { name: "Lock backup" }));
		expect(
			await screen.findByText("Could not change the lock"),
		).toBeInTheDocument();
		expect(screen.getByText("backup is being deleted")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Lock backup" }),
		).toBeInTheDocument();
	});
});

describe("BackupsPanel delete", () => {
	test("confirming deletes the backup, tells the user and refetches", async () => {
		let items = [backup({ sizeBytes: 5 * 1024 * 1024 })];
		const api = stubWorld({
			extra: {
				"GET /services/:id/backups": () => Response.json({ items }),
				"DELETE /backups/:id": () => {
					items = [];
					return new Response(null, { status: 202 });
				},
			},
		});
		await renderPanel();
		const user = userEvent.setup();
		await user.click(await screen.findByRole("button", { name: "Delete" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent("Delete this backup?");
		expect(dialog).toHaveTextContent("5 MiB");
		expect(dialog).toHaveTextContent("Offsite bucket");
		expect(dialog).toHaveTextContent("cannot be undone");
		// Nothing is sent until the user confirms.
		expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);

		await user.click(
			within(dialog).getByRole("button", { name: "Delete backup" }),
		);
		expect(
			await screen.findByText("Deleting the stored backup"),
		).toBeInTheDocument();
		await waitFor(() =>
			expect(
				screen.queryByRole("columnheader", { name: "When" }),
			).not.toBeInTheDocument(),
		);
		expect(screen.getByText(/No backups yet/)).toBeInTheDocument();
		const del = api.calls.filter((c) => c.method === "DELETE");
		expect(del).toHaveLength(1);
		expect(del[0]?.path).toBe("/backups/b1");
		expect(api.unmatched).toEqual([]);
	});

	test("cancelling sends nothing and keeps the row", async () => {
		const api = stubWorld({ backups: [backup()] });
		await renderPanel();
		const user = userEvent.setup();
		await user.click(await screen.findByRole("button", { name: "Delete" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
		);
		expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
		expect(
			screen.getByRole("columnheader", { name: "When" }),
		).toBeInTheDocument();
	});

	test("a refused delete keeps the dialog open with the server's reason", async () => {
		stubWorld({
			backups: [backup()],
			extra: {
				"DELETE /backups/:id": problem(409, {
					title: "Conflict",
					detail: "backup is locked",
				}),
			},
		});
		await renderPanel();
		const user = userEvent.setup();
		await user.click(await screen.findByRole("button", { name: "Delete" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Delete backup" }),
		);
		expect(
			await within(dialog).findByText("backup is locked"),
		).toBeInTheDocument();
		expect(
			await screen.findByText("Could not delete the backup"),
		).toBeInTheDocument();
		expect(
			screen.queryByText("Deleting the stored backup"),
		).not.toBeInTheDocument();
		expect(screen.getByRole("alertdialog")).toBeInTheDocument();
	});
});

describe("BackupsPanel schedules", () => {
	test("describes the schedule in words with retention, destination and next run", async () => {
		stubWorld({
			schedules: [
				schedule({
					nextRunAt: new Date(Date.now() + 3 * 3600_000).toISOString(),
					lastRunAt: hours(21),
					retentionCount: 7,
					retentionDays: 1,
					timezone: "Europe/Berlin",
				}),
			],
		});
		await renderPanel();
		expect(await screen.findByText("Every day at 03:00")).toBeInTheDocument();
		const detail = screen.getByText("0 3 * * *").closest("span");
		expect(detail).toHaveTextContent("Europe/Berlin");
		expect(detail).toHaveTextContent("Offsite bucket");
		expect(detail).toHaveTextContent("Keep the newest 7, delete after 1 day");
		expect(screen.getByText(/Next run/)).toBeInTheDocument();
		expect(screen.getByText(/last run/)).toBeInTheDocument();
		expect(screen.queryByText("Paused")).not.toBeInTheDocument();
		expect(
			screen.getByRole("switch", { name: "Pause schedule" }),
		).toBeChecked();
		expect(screen.queryByText(/No schedule\. Add one/)).not.toBeInTheDocument();
	});

	test("a disabled schedule reads Paused even if it has a next run", async () => {
		stubWorld({
			schedules: [
				schedule({
					enabled: false,
					nextRunAt: new Date(Date.now() + 3600_000).toISOString(),
					retentionCount: 0,
					retentionDays: 0,
				}),
			],
		});
		await renderPanel();
		expect(await screen.findByText("Paused")).toBeInTheDocument();
		expect(screen.queryByText(/Next run/)).not.toBeInTheDocument();
		expect(screen.getByText(/Keep everything/)).toBeInTheDocument();
		expect(
			screen.getByRole("switch", { name: "Resume schedule" }),
		).not.toBeChecked();
	});

	test("the switch pauses then resumes via PATCH", async () => {
		let enabled = true;
		const api = stubWorld({
			extra: {
				"GET /services/:id/backup-schedules": () =>
					Response.json({ items: [schedule({ enabled })] }),
				"PATCH /backup-schedules/:id": async (req) => {
					const body: { enabled: boolean } = await req.json();
					enabled = body.enabled;
					return Response.json({});
				},
			},
		});
		await renderPanel();
		const user = userEvent.setup();
		await user.click(
			await screen.findByRole("switch", { name: "Pause schedule" }),
		);
		await user.click(
			await screen.findByRole("switch", { name: "Resume schedule" }),
		);
		await screen.findByRole("switch", { name: "Pause schedule" });
		const patches = api.calls.filter((c) => c.method === "PATCH");
		expect(patches.map((c) => [c.path, c.body])).toEqual([
			["/backup-schedules/sch1", { enabled: false }],
			["/backup-schedules/sch1", { enabled: true }],
		]);
	});

	test("a rejected toggle is reported", async () => {
		stubWorld({
			schedules: [schedule()],
			extra: {
				"PATCH /backup-schedules/:id": problem(500, {
					title: "Boom",
					detail: "scheduler offline",
				}),
			},
		});
		await renderPanel();
		await userEvent
			.setup()
			.click(await screen.findByRole("switch", { name: "Pause schedule" }));
		expect(
			await screen.findByText("Could not change the schedule"),
		).toBeInTheDocument();
		expect(screen.getByText("scheduler offline")).toBeInTheDocument();
	});

	test("Remove confirms, deletes the schedule and keeps its backups", async () => {
		let schedules = [schedule()];
		const api = stubWorld({
			backups: [backup()],
			extra: {
				"GET /services/:id/backup-schedules": () =>
					Response.json({ items: schedules }),
				"DELETE /backup-schedules/:id": () => {
					schedules = [];
					return new Response(null, { status: 204 });
				},
			},
		});
		await renderPanel();
		const user = userEvent.setup();
		await user.click(await screen.findByRole("button", { name: "Remove" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent("Remove this schedule?");
		expect(dialog).toHaveTextContent("Backups it already made are kept");
		expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
		await user.click(
			within(dialog).getByRole("button", { name: "Remove schedule" }),
		);
		expect(await screen.findByText("Schedule removed")).toBeInTheDocument();
		expect(
			await screen.findByText(/No schedule\. Add one/),
		).toBeInTheDocument();
		expect(
			api.calls.filter((c) => c.method === "DELETE").map((c) => c.path),
		).toEqual(["/backup-schedules/sch1"]);
		// The run history survives.
		expect(
			screen.getByRole("columnheader", { name: "When" }),
		).toBeInTheDocument();
	});

	test("a failed removal keeps the dialog open with the reason", async () => {
		stubWorld({
			schedules: [schedule()],
			extra: {
				"DELETE /backup-schedules/:id": problem(500, {
					title: "Boom",
					detail: "database is busy",
				}),
			},
		});
		await renderPanel();
		const user = userEvent.setup();
		await user.click(await screen.findByRole("button", { name: "Remove" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Remove schedule" }),
		);
		expect(
			await within(dialog).findByText("database is busy"),
		).toBeInTheDocument();
		expect(
			await screen.findByText("Could not remove the schedule"),
		).toBeInTheDocument();
	});

	test("Edit opens the schedule dialog filled with that schedule", async () => {
		stubWorld({
			schedules: [
				schedule({
					cron: "*/15 * * * *",
					timezone: "Asia/Tokyo",
					retentionCount: 3,
					retentionDays: 14,
				}),
			],
		});
		await renderPanel();
		await userEvent
			.setup()
			.click(await screen.findByRole("button", { name: "Edit" }));
		const dialog = await screen.findByRole("dialog");
		expect(within(dialog).getByText("Edit schedule")).toBeInTheDocument();
		expect(within(dialog).getByLabelText("Cron expression")).toHaveValue(
			"*/15 * * * *",
		);
		expect(within(dialog).getByLabelText("Timezone")).toHaveValue("Asia/Tokyo");
		expect(within(dialog).getByLabelText("Keep the newest")).toHaveValue("3");
		expect(within(dialog).getByLabelText("Delete after (days)")).toHaveValue(
			"14",
		);
	});

	test("Add schedule opens an empty create dialog even after editing another", async () => {
		stubWorld({ schedules: [schedule({ cron: "@hourly" })] });
		await renderPanel();
		const user = userEvent.setup();
		await user.click(await screen.findByRole("button", { name: "Edit" }));
		let dialog = await screen.findByRole("dialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);

		await user.click(screen.getByRole("button", { name: /Add schedule/ }));
		dialog = await screen.findByRole("dialog");
		expect(within(dialog).getByText("Schedule backups")).toBeInTheDocument();
		expect(within(dialog).getByLabelText("Cron expression")).toHaveValue(
			"0 3 * * *",
		);
	});
});

describe("BackupsPanel back up now", () => {
	test("starts a backup on the only destination and refreshes the list", async () => {
		let items: BackupView[] = [];
		const api = stubWorld({
			extra: {
				"GET /services/:id/backups": () => Response.json({ items }),
				"POST /services/:id/backups": () => {
					items = [backup({ id: "new", status: "queued" })];
					return Response.json(backup({ id: "new", status: "queued" }), {
						status: 202,
					});
				},
			},
		});
		await renderPanel();
		const user = userEvent.setup();
		await user.click(
			await screen.findByRole("button", { name: /Back up now/ }),
		);
		const dialog = await screen.findByRole("dialog");
		await user.click(
			within(dialog).getByRole("button", { name: /Back up now/ }),
		);
		expect(await screen.findByText("Backup started")).toBeInTheDocument();
		expect(await screen.findByText("Queued")).toBeInTheDocument();
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);
		const post = api.calls.find((c) => c.method === "POST");
		expect(post).toMatchObject({
			path: "/services/s1/backups",
			body: { destinationId: "d1" },
		});
	});
});

describe("BackupsPanel restores", () => {
	test("backup restores and dump restores are merged, newest first, with their status and error", async () => {
		stubWorld({
			restores: [
				{
					id: "r1",
					backupId: "b1",
					sourceServiceId: "s1",
					status: "succeeded",
					mode: "new",
					createdAt: "2026-01-02T00:00:00Z",
					actor: "ada@example.com",
				},
				{
					id: "r2",
					backupId: "b1",
					sourceServiceId: "s1",
					status: "failed",
					mode: "in_place",
					createdAt: "2026-01-04T00:00:00Z",
					error: "disk full",
				},
			],
			dumpRestores: [
				{
					id: "d1",
					status: "restoring",
					mode: "in_place",
					createdAt: "2026-01-03T00:00:00Z",
					fileName: "orders.dump",
				},
				{
					id: "d2",
					status: "awaiting_upload",
					mode: "new",
					createdAt: "2026-01-01T00:00:00Z",
				},
			],
		});
		await renderPanel();
		expect(
			await screen.findByText("Recent restores into or from this database."),
		).toBeInTheDocument();
		const section =
			screen
				.getByText("Recent restores into or from this database.")
				.closest("section") ?? document.body;
		const entries = within(section).getAllByText(
			/In place|Into a new database/,
		);
		expect(
			entries.map((e) => e.textContent?.replace(/\s+/g, " ").trim()),
		).toEqual([
			expect.stringContaining("In place"), // r2 failed, 01-04
			expect.stringContaining("From orders.dump · In place"), // 01-03
			expect.stringContaining("Into a new database"), // r1 01-02
			expect.stringContaining("From a dump file · Into a new database"), // d2 01-01
		]);
		expect(entries[0]).toHaveTextContent("In place");
		expect(entries[2]).toHaveTextContent("ada@example.com");
		expect(screen.getByText("disk full")).toBeInTheDocument();
		expect(
			screen.getByText("Failed", { selector: "span" }),
		).toBeInTheDocument();
		expect(screen.getByText("Restored")).toBeInTheDocument();
		expect(screen.getByText("Restoring")).toBeInTheDocument();
		expect(screen.getByText("Waiting for the file")).toBeInTheDocument();
	});
});
