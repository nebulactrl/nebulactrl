import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	InstanceBackupView,
	InstanceRunView,
	InstanceUpgradeView,
} from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import {
	backupView,
	problem,
	runView,
	s3Destination,
} from "./instance-backup.fixture";
import { InstanceBackupSection } from "./instance-backup-section";

const noUpgrade: InstanceUpgradeView = {
	agentAutoUpgrade: true,
	behind: 0,
	controlPlaneVersion: "1.4.0",
	servers: [],
	upgradable: 0,
};

/** The three reads the section makes; `settings` is mutable so a test can change it between refetches. */
function setup(
	settings: InstanceBackupView,
	runs: InstanceRunView[] = [],
	extra: Parameters<typeof stubApi>[0] = {},
) {
	const state = { settings };
	const api = stubApi({
		"GET /instance/backup": () => Response.json(state.settings),
		"GET /instance/backup/runs": () => Response.json({ items: runs }),
		"GET /instance/upgrade": { body: noUpgrade },
		...extra,
	});
	return { api, state };
}

const writes = (api: ReturnType<typeof stubApi>) =>
	api.calls.filter((c) => c.method !== "GET");

async function renderSection() {
	const user = userEvent.setup();
	const view = await renderWithProviders(<InstanceBackupSection />);
	await screen.findByText("Back up every day");
	return { user, ...view };
}

const unconfigured = (over: Partial<InstanceBackupView> = {}) =>
	backupView({
		enabled: false,
		destination: s3Destination({
			bucket: undefined,
			prefix: undefined,
			accessKeySet: false,
			secretKeySet: false,
			configured: false,
			where: "",
		}),
		...over,
	});

describe("InstanceBackupSection: loading", () => {
	test("an unreachable control plane shows an error with a retry that recovers", async () => {
		let attempts = 0;
		const api = stubApi({
			"GET /instance/backup": () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{ status: 500, title: "x", detail: "boom", code: "internal" },
							{ status: 500 },
						)
					: Response.json(backupView());
			},
			"GET /instance/backup/runs": { body: { items: [] } },
			"GET /instance/upgrade": { body: noUpgrade },
		});
		const user = userEvent.setup();
		await renderWithProviders(<InstanceBackupSection />);
		expect(
			await screen.findByText("Backup settings did not load"),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"The control plane did not answer, so nothing was changed.",
			),
		).toBeInTheDocument();
		expect(screen.queryByText("Back up every day")).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Try again" }));
		expect(await screen.findByText("Back up every day")).toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});

	test("the history is requested with a limit of 20", async () => {
		const { api } = setup(backupView());
		await renderSection();
		await waitFor(() =>
			expect(
				api.calls
					.find((c) => c.path === "/instance/backup/runs")
					?.query.get("limit"),
			).toBe("20"),
		);
	});
});

describe("InstanceBackupSection: configured and enabled", () => {
	test("summarises the schedule, retention, next run, last backup and destination", async () => {
		const { api } = setup(
			backupView({
				nextRunAt: "2026-10-28T03:30:00Z",
				lastRun: runView({ status: "succeeded" }),
				destination: s3Destination({
					lastTestOk: true,
					lastTestAt: "2026-10-27T10:00:00Z",
				}),
			}),
		);
		await renderSection();
		expect(
			screen.getByText("Every day at 03:30 UTC. Keeps the newest 14 backups."),
		).toBeInTheDocument();
		expect(screen.getByText(/Next backup/)).toBeInTheDocument();
		expect(screen.getByText(/2026-10-28 03:30:00 UTC/)).toBeInTheDocument();
		expect(screen.getByText(/Last backup: succeeded/)).toBeInTheDocument();
		expect(screen.getByText("S3-compatible bucket")).toBeInTheDocument();
		expect(screen.getByText("s3://nebula-backups/prod")).toBeInTheDocument();
		expect(screen.queryByText("not off-site")).not.toBeInTheDocument();
		expect(screen.getByText("Works")).toBeInTheDocument();
		expect(screen.getByRole("switch")).toBeChecked();
		expect(screen.getByRole("button", { name: "Edit" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Test" })).toBeEnabled();
		expect(screen.getByRole("button", { name: "Back up now" })).toBeEnabled();
		expect(api.unmatched).toEqual([]);
	});

	test("singular retention reads 'backup', not 'backups'", async () => {
		setup(backupView({ retentionCount: 1 }));
		await renderSection();
		expect(
			screen.getByText("Every day at 03:30 UTC. Keeps the newest 1 backup."),
		).toBeInTheDocument();
	});

	test("an untested destination says so; a failed test shows its message", async () => {
		setup(backupView());
		const first = await renderSection();
		expect(screen.getByText("Not tested")).toBeInTheDocument();
		first.unmount();

		setup(
			backupView({
				destination: s3Destination({
					lastTestOk: false,
					lastTestMessage: "AccessDenied: key cannot write",
				}),
			}),
		);
		await renderSection();
		expect(screen.getByText("Failed")).toBeInTheDocument();
		expect(
			screen.getByText("AccessDenied: key cannot write"),
		).toBeInTheDocument();
		expect(screen.queryByText("Not tested")).not.toBeInTheDocument();
	});

	test("shows no 'next backup' line when disabled and a 'no backup yet' line without runs", async () => {
		setup(backupView({ enabled: false, nextRunAt: "2026-10-28T03:30:00Z" }));
		await renderSection();
		expect(screen.queryByText(/Next backup/)).not.toBeInTheDocument();
		expect(screen.getByText("No backup has run yet.")).toBeInTheDocument();
		expect(
			screen.getByText("Off. Turn it on to back up on a schedule."),
		).toBeInTheDocument();
	});

	test("the unacknowledged key reminder and the pre-upgrade note appear in the section", async () => {
		setup(backupView({ keyAcknowledgedAt: undefined }), [], {
			"GET /instance/upgrade": {
				body: {
					...noUpgrade,
					preUpgradeBackup: {
						status: "succeeded",
						at: "2026-10-27T03:30:00Z",
						toVersion: "1.4.0",
					},
				},
			},
		});
		await renderSection();
		expect(screen.getByTestId("secret-key-reminder")).toBeInTheDocument();
		expect(
			await screen.findByText("Backed up before the upgrade (to 1.4.0)"),
		).toBeInTheDocument();
	});
});

describe("InstanceBackupSection: unconfigured and unavailable", () => {
	test("without a destination backups cannot be enabled, tested or run", async () => {
		setup(unconfigured());
		await renderSection();
		expect(screen.getByText("Not set up yet.")).toBeInTheDocument();
		expect(
			screen.getByText("Off. Set up a destination first."),
		).toBeInTheDocument();
		expect(screen.getByRole("switch")).toHaveAttribute("aria-disabled", "true");
		expect(screen.getByRole("button", { name: "Test" })).toBeDisabled();
		expect(screen.getByRole("button", { name: "Back up now" })).toBeDisabled();
		expect(screen.getByRole("button", { name: "Set up" })).toBeEnabled();
		expect(screen.queryByRole("button", { name: "Edit" })).toBeNull();
		expect(
			screen.getByText(
				"nebulactrl-server restore --from s3://<bucket> --at latest",
			),
		).toBeInTheDocument();
	});

	test("a directory destination without NEBULACTRL_BACKUP_DIR explains why it is unavailable", async () => {
		setup(
			unconfigured({
				localDirEnabled: false,
				destination: s3Destination({
					kind: "local",
					bucket: undefined,
					configured: false,
					offSite: false,
					where: "",
				}),
			}),
		);
		await renderSection();
		expect(
			screen.getByText(
				"NEBULACTRL_BACKUP_DIR is not set on the control plane, so a directory is unavailable.",
			),
		).toBeInTheDocument();
		expect(screen.getByText("not off-site")).toBeInTheDocument();
		expect(screen.getByText("Directory on this host")).toBeInTheDocument();
	});

	test("missing pg_dump shows the host's message and blocks enabling and running", async () => {
		setup(
			backupView({
				enabled: false,
				toolsAvailable: false,
				toolsMessage: "pg_dump 16 not found in PATH",
			}),
		);
		await renderSection();
		expect(
			screen.getByText("Backups cannot run on this host"),
		).toBeInTheDocument();
		expect(
			screen.getByText("pg_dump 16 not found in PATH"),
		).toBeInTheDocument();
		expect(screen.getByRole("switch")).toHaveAttribute("aria-disabled", "true");
		expect(screen.getByRole("button", { name: "Back up now" })).toBeDisabled();
		// The destination is fine: it can still be tested.
		expect(screen.getByRole("button", { name: "Test" })).toBeEnabled();
	});

	test("missing pg_dump without a message falls back to the generic one", async () => {
		setup(backupView({ toolsAvailable: false }));
		await renderSection();
		expect(
			screen.getByText(
				"The tool that dumps the database is not installed on the control-plane host.",
			),
		).toBeInTheDocument();
	});

	test("an enabled schedule can still be switched off while tools are missing", async () => {
		setup(backupView({ enabled: true, toolsAvailable: false }));
		await renderSection();
		expect(screen.getByRole("switch")).not.toHaveAttribute(
			"aria-disabled",
			"true",
		);
	});
});

describe("InstanceBackupSection: running and failed", () => {
	test("a running backup disables Back up now and says it is running", async () => {
		const api = setup(backupView({ lastRun: runView({ status: "running" }) }), [
			runView({ status: "running", sizeBytes: 0 }),
		]).api;
		await renderSection();
		const button = screen.getByRole("button", { name: /Backing up/ });
		expect(button).toBeDisabled();
		expect(screen.getByText(/Last backup: running/)).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Back up now" })).toBeNull();
		expect(writes(api)).toEqual([]);
	});

	test("a failed last run is shown with its error in the history, and a new run is allowed", async () => {
		setup(
			backupView({
				lastRun: runView({ status: "failed", error: "pg_dump exited 1" }),
			}),
			[runView({ status: "failed", error: "pg_dump exited 1", sizeBytes: 0 })],
		);
		await renderSection();
		expect(screen.getByText(/Last backup: failed/)).toBeInTheDocument();
		const table = await screen.findByRole("table");
		expect(within(table).getByText("Failed")).toBeInTheDocument();
		expect(within(table).getByText("pg_dump exited 1")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Back up now" })).toBeEnabled();
	});
});

describe("InstanceBackupSection: history", () => {
	test("lists each run with trigger, status, destination and size", async () => {
		setup(backupView(), [
			runView({
				id: "r1",
				trigger: "schedule",
				status: "succeeded",
				sizeBytes: 2048,
				destination: "s3://nebula-backups/prod/a.dump",
			}),
			runView({
				id: "r2",
				trigger: "manual",
				status: "failed",
				error: "disk full",
				destKind: "local",
				destination: "/var/backups/b.dump",
				sizeBytes: 999999,
			}),
			runView({
				id: "r3",
				trigger: "pre_upgrade",
				status: "succeeded",
				objectDeleted: true,
				destKind: "builtin",
				destination: "builtin://c.dump",
				sizeBytes: 5 * 1024 * 1024,
			}),
			runView({ id: "r4", status: "running", sizeBytes: 0 }),
		]);
		await renderSection();
		const table = await screen.findByRole("table");
		const rows = within(table).getAllByRole("row").slice(1);
		expect(rows).toHaveLength(4);
		const [scheduled, failed, preUpgrade, running] = rows;
		if (!scheduled || !failed || !preUpgrade || !running) {
			throw new Error("expected four history rows");
		}

		expect(within(scheduled).getByText("Scheduled")).toBeInTheDocument();
		expect(within(scheduled).getByText("Succeeded")).toBeInTheDocument();
		expect(
			within(scheduled).getByText("s3://nebula-backups/prod/a.dump"),
		).toBeInTheDocument();
		expect(within(scheduled).getByText("2 KiB")).toBeInTheDocument();
		expect(within(scheduled).queryByText("removed by retention")).toBeNull();

		expect(within(failed).getByText("Manual")).toBeInTheDocument();
		expect(within(failed).getByText("Failed")).toBeInTheDocument();
		expect(within(failed).getByText("disk full")).toBeInTheDocument();
		// A failed run has no size to show.
		expect(within(failed).getByText("–")).toBeInTheDocument();
		expect(within(failed).queryByText(/KiB|MiB/)).toBeNull();

		expect(within(preUpgrade).getByText("Before upgrade")).toBeInTheDocument();
		expect(
			within(preUpgrade).getByText("removed by retention"),
		).toBeInTheDocument();
		expect(within(preUpgrade).getByText("5 MiB")).toBeInTheDocument();

		expect(within(running).getByText("Running")).toBeInTheDocument();
		expect(within(running).getByText("–")).toBeInTheDocument();
	});

	test("with no runs the history explains when the first one happens", async () => {
		setup(backupView(), []);
		await renderSection();
		expect(
			await screen.findByText(
				/No backups yet\. The first one runs at the next/,
			),
		).toBeInTheDocument();
		expect(screen.queryByRole("table")).toBeNull();
	});
});

describe("InstanceBackupSection: enable toggle", () => {
	test("turning it on PATCHes enabled: true and confirms", async () => {
		const { api, state } = setup(backupView({ enabled: false }), [], {
			"PATCH /instance/backup": () => {
				state.settings = backupView({ enabled: true });
				return Response.json(state.settings);
			},
		});
		const { user } = await renderSection();
		await user.click(screen.getByRole("switch"));
		expect(await screen.findByText("Daily backups are on")).toBeInTheDocument();
		expect(writes(api)).toHaveLength(1);
		expect(writes(api)[0]).toMatchObject({
			method: "PATCH",
			path: "/instance/backup",
			body: { enabled: true },
		});
		await waitFor(() => expect(screen.getByRole("switch")).toBeChecked());
	});

	test("turning it off PATCHes enabled: false", async () => {
		const { api, state } = setup(backupView({ enabled: true }), [], {
			"PATCH /instance/backup": () => {
				state.settings = backupView({ enabled: false });
				return Response.json(state.settings);
			},
		});
		const { user } = await renderSection();
		await user.click(screen.getByRole("switch"));
		expect(
			await screen.findByText("Daily backups are off"),
		).toBeInTheDocument();
		expect(writes(api)[0]?.body).toEqual({ enabled: false });
		await waitFor(() => expect(screen.getByRole("switch")).not.toBeChecked());
	});

	test("a rejected change shows the error and leaves the switch as it was", async () => {
		setup(backupView({ enabled: false }), [], {
			"PATCH /instance/backup": problem(409, "destination was not tested"),
		});
		const { user } = await renderSection();
		await user.click(screen.getByRole("switch"));
		expect(await screen.findByText("Could not change it")).toBeInTheDocument();
		expect(screen.getByText("destination was not tested")).toBeInTheDocument();
		expect(screen.getByRole("switch")).not.toBeChecked();
	});
});

describe("InstanceBackupSection: schedule and retention", () => {
	test("shows the stored time and retention", async () => {
		setup(backupView({ schedule: "5 4 * * *", retentionCount: 30 }));
		await renderSection();
		expect(screen.getByLabelText("Time of day (UTC)")).toHaveValue("04:05");
		expect(screen.getByLabelText("Backups to keep")).toHaveValue("30");
		expect(
			screen.getByText("24 hour clock, UTC. Default 03:30."),
		).toBeInTheDocument();
	});

	test("saving sends the daily cron for the time and the retention count", async () => {
		const { api } = setup(backupView(), [], {
			"PATCH /instance/backup": { body: backupView() },
		});
		const { user } = await renderSection();
		const time = screen.getByLabelText("Time of day (UTC)");
		await user.clear(time);
		await user.click(time);
		await user.paste("4:05");
		const keep = screen.getByLabelText("Backups to keep");
		await user.clear(keep);
		await user.click(keep);
		await user.paste("7");
		await user.click(screen.getByRole("button", { name: "Save schedule" }));

		expect(await screen.findByText("Schedule saved")).toBeInTheDocument();
		expect(writes(api)).toHaveLength(1);
		expect(writes(api)[0]).toMatchObject({
			method: "PATCH",
			path: "/instance/backup",
			body: { schedule: "5 4 * * *", retentionCount: 7 },
		});
	});

	test("a blank time leaves the schedule out of the body", async () => {
		const { api } = setup(backupView({ schedule: "0 */6 * * *" }), [], {
			"PATCH /instance/backup": { body: backupView() },
		});
		const { user } = await renderSection();
		// A custom cron is not editable as a time: the field starts empty and says so.
		expect(screen.getByLabelText("Time of day (UTC)")).toHaveValue("");
		expect(screen.getByLabelText("Time of day (UTC)")).toHaveAttribute(
			"placeholder",
			"cron 0 */6 * * *",
		);
		expect(
			screen.getByText(
				"The API set a custom schedule (0 */6 * * *). Enter a time to switch to a daily one.",
			),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Save schedule" }));
		await screen.findByText("Schedule saved");
		expect(writes(api)[0]?.body).toEqual({ retentionCount: 14 });
	});

	test.each([["24:00"], ["12:60"], ["3pm"], ["03:5"]])(
		"rejects %p as a time without sending anything",
		async (value) => {
			const { api } = setup(backupView());
			const { user } = await renderSection();
			const time = screen.getByLabelText("Time of day (UTC)");
			await user.clear(time);
			await user.click(time);
			await user.paste(value);
			await user.click(screen.getByRole("button", { name: "Save schedule" }));
			expect(
				await screen.findByText(
					"Enter the time as HH:MM, 24 hour, for example 03:30.",
				),
			).toBeInTheDocument();
			expect(writes(api)).toEqual([]);
		},
	);

	test.each([["0"], ["366"], [""]])(
		"rejects a retention of %p as out of range",
		async (value) => {
			const { api } = setup(backupView());
			const { user } = await renderSection();
			const keep = screen.getByLabelText("Backups to keep");
			await user.clear(keep);
			if (value) {
				await user.click(keep);
				await user.paste(value);
			}
			await user.click(screen.getByRole("button", { name: "Save schedule" }));
			expect(
				await screen.findByText("Keep between 1 and 365 backups."),
			).toBeInTheDocument();
			expect(writes(api)).toEqual([]);
		},
	);

	test("accepts the retention bounds 1 and 365", async () => {
		const { api } = setup(backupView(), [], {
			"PATCH /instance/backup": { body: backupView() },
		});
		const { user } = await renderSection();
		const keep = screen.getByLabelText("Backups to keep");
		await user.clear(keep);
		await user.click(keep);
		await user.paste("365");
		await user.click(screen.getByRole("button", { name: "Save schedule" }));
		await screen.findByText("Schedule saved");
		expect(writes(api)[0]?.body).toMatchObject({ retentionCount: 365 });
	});

	test("the retention field drops anything that is not a digit", async () => {
		setup(backupView());
		const { user } = await renderSection();
		const keep = screen.getByLabelText("Backups to keep");
		await user.clear(keep);
		await user.click(keep);
		await user.paste("1a2-b");
		expect(keep).toHaveValue("12");
	});

	test("a server error is shown under the form and no success toast appears", async () => {
		setup(backupView(), [], {
			"PATCH /instance/backup": problem(422, "schedule too frequent"),
		});
		const { user } = await renderSection();
		await user.click(screen.getByRole("button", { name: "Save schedule" }));
		expect(
			await screen.findByText("schedule too frequent"),
		).toBeInTheDocument();
		expect(screen.queryByText("Schedule saved")).not.toBeInTheDocument();
	});

	test("a validation error clears after a successful save", async () => {
		setup(backupView(), [], {
			"PATCH /instance/backup": { body: backupView() },
		});
		const { user } = await renderSection();
		const keep = screen.getByLabelText("Backups to keep");
		await user.clear(keep);
		await user.click(screen.getByRole("button", { name: "Save schedule" }));
		await screen.findByText("Keep between 1 and 365 backups.");
		await user.click(keep);
		await user.paste("5");
		await user.click(screen.getByRole("button", { name: "Save schedule" }));
		await screen.findByText("Schedule saved");
		expect(
			screen.queryByText("Keep between 1 and 365 backups."),
		).not.toBeInTheDocument();
	});
});

describe("InstanceBackupSection: back up now", () => {
	test("posts a run, confirms, and refetches the settings", async () => {
		const { api, state } = setup(backupView(), [], {
			"POST /instance/backup/runs": () => {
				state.settings = backupView({
					lastRun: runView({ status: "running" }),
				});
				return Response.json(runView({ status: "running" }), { status: 202 });
			},
		});
		const { user } = await renderSection();
		await user.click(screen.getByRole("button", { name: "Back up now" }));
		expect(await screen.findByText("Backup started")).toBeInTheDocument();
		expect(screen.getByText("It runs in the background.")).toBeInTheDocument();
		expect(writes(api)).toHaveLength(1);
		expect(writes(api)[0]).toMatchObject({
			method: "POST",
			path: "/instance/backup/runs",
		});
		expect(
			await screen.findByRole("button", { name: /Backing up/ }),
		).toBeDisabled();
	});

	test("a rejected run reports the reason and stays available", async () => {
		setup(backupView(), [], {
			"POST /instance/backup/runs": problem(409, "a backup is already running"),
		});
		const { user } = await renderSection();
		await user.click(screen.getByRole("button", { name: "Back up now" }));
		expect(
			await screen.findByText("Could not start the backup"),
		).toBeInTheDocument();
		expect(screen.getByText("a backup is already running")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Back up now" })).toBeEnabled();
	});
});

describe("InstanceBackupSection: test destination", () => {
	test("a passing test posts to /test and says the destination works", async () => {
		const { api, state } = setup(backupView(), [], {
			"POST /instance/backup/test": () => {
				state.settings = backupView({
					destination: s3Destination({ lastTestOk: true }),
				});
				return Response.json(state.settings);
			},
		});
		const { user } = await renderSection();
		await user.click(screen.getByRole("button", { name: "Test" }));
		expect(await screen.findByText("Destination works")).toBeInTheDocument();
		expect(
			screen.getByText("Wrote, read back and deleted a probe object."),
		).toBeInTheDocument();
		expect(writes(api)[0]).toMatchObject({
			method: "POST",
			path: "/instance/backup/test",
		});
		expect(await screen.findByText("Works")).toBeInTheDocument();
	});

	test("a failing test shows the destination's message", async () => {
		const failed = backupView({
			destination: s3Destination({
				lastTestOk: false,
				lastTestMessage: "NoSuchBucket",
			}),
		});
		const { state } = setup(backupView(), [], {
			"POST /instance/backup/test": () => {
				state.settings = failed;
				return Response.json(failed);
			},
		});
		const { user } = await renderSection();
		await user.click(screen.getByRole("button", { name: "Test" }));
		expect(
			await screen.findByText("Destination test failed"),
		).toBeInTheDocument();
		// The message shows in the toast and in the destination card.
		await waitFor(() =>
			expect(screen.getAllByText("NoSuchBucket").length).toBeGreaterThanOrEqual(
				2,
			),
		);
		expect(screen.queryByText("Destination works")).not.toBeInTheDocument();
	});

	test("an HTTP error is reported as a failure to test, not as a failed test", async () => {
		setup(backupView(), [], {
			"POST /instance/backup/test": problem(500, "probe crashed"),
		});
		const { user } = await renderSection();
		await user.click(screen.getByRole("button", { name: "Test" }));
		expect(
			await screen.findByText("Could not test the destination"),
		).toBeInTheDocument();
		expect(screen.getByText("probe crashed")).toBeInTheDocument();
	});
});

describe("InstanceBackupSection: destination", () => {
	test("Edit opens the destination dialog with the stored values", async () => {
		setup(backupView());
		const { user } = await renderSection();
		await user.click(screen.getByRole("button", { name: "Edit" }));
		const dialog = await screen.findByRole("dialog", {
			name: "Backup destination",
		});
		expect(within(dialog).getByLabelText("Bucket")).toHaveValue(
			"nebula-backups",
		);
	});

	test("Set up opens the dialog for an unconfigured destination", async () => {
		setup(unconfigured());
		const { user } = await renderSection();
		await user.click(screen.getByRole("button", { name: "Set up" }));
		const dialog = await screen.findByRole("dialog", {
			name: "Backup destination",
		});
		expect(within(dialog).getByLabelText("Bucket")).toHaveValue("");
	});

	test("the built-in storage is flagged as not off-site, in the card and the restore note", async () => {
		setup(
			backupView({
				notOffSite: true,
				destination: s3Destination({
					kind: "builtin",
					bucket: undefined,
					offSite: false,
					where: "built-in storage (system bucket)",
				}),
			}),
		);
		await renderSection();
		expect(screen.getAllByText("Not off-site").length).toBeGreaterThanOrEqual(
			1,
		);
		expect(
			screen.getByText(/runs on the same host as the control plane/),
		).toBeInTheDocument();
		expect(screen.getByText("not off-site")).toBeInTheDocument();
		expect(screen.getByText("Built-in storage")).toBeInTheDocument();
		expect(
			screen.getByText(
				/The built-in storage is not reachable from outside this host/,
			),
		).toBeInTheDocument();
		expect(screen.queryByText(/nebulactrl-server restore/)).toBeNull();
	});

	test("a built-in destination not flagged by the API shows no warning alert", async () => {
		setup(
			backupView({
				notOffSite: false,
				destination: s3Destination({
					kind: "builtin",
					offSite: true,
					where: "built-in",
				}),
			}),
		);
		await renderSection();
		expect(screen.queryByText(/runs on the same host/)).not.toBeInTheDocument();
	});
});

describe("InstanceBackupSection: restore", () => {
	test("shows the restore command for an S3 bucket and prefix, and copies it on click", async () => {
		setup(backupView());
		const { user } = await renderSection();
		const command =
			"nebulactrl-server restore --from s3://nebula-backups/prod --at latest";
		expect(screen.getByText(command)).toBeInTheDocument();
		const copy = screen.getByRole("button", {
			name: `Copy to clipboard: ${command}`,
		});
		await user.click(copy);
		expect(await navigator.clipboard.readText()).toBe(command);
		expect(await within(copy).findByRole("status")).toHaveTextContent("Copied");
	});

	test("an S3 destination without a prefix restores from the bare bucket", async () => {
		setup(backupView({ destination: s3Destination({ prefix: undefined }) }));
		await renderSection();
		expect(
			screen.getByText(
				"nebulactrl-server restore --from s3://nebula-backups --at latest",
			),
		).toBeInTheDocument();
	});

	test("a directory destination restores from its path without the 'directory ' label", async () => {
		setup(
			backupView({
				destination: s3Destination({
					kind: "local",
					bucket: undefined,
					offSite: false,
					where: "directory /var/lib/nebulactrl/backups/nightly",
				}),
			}),
		);
		await renderSection();
		expect(
			screen.getByText(
				"nebulactrl-server restore --from /var/lib/nebulactrl/backups/nightly --at latest",
			),
		).toBeInTheDocument();
	});

	test("explains that the restore needs the original secret key", async () => {
		setup(backupView());
		await renderSection();
		expect(
			screen.getByText(/into an empty database, with the original/),
		).toBeInTheDocument();
		expect(
			screen.getByText(/docs\/operations\/backup-restore\.md/),
		).toBeInTheDocument();
	});
});
