import { describe, expect, mock, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { VolumeBackupView } from "#/api/gen/types.gen";
import { type Overrides, withOverrides } from "#/test/fixture";
import { renderWithProviders } from "#/test/render";
import { HistoryTable } from "./history";

function backup(over: Overrides<VolumeBackupView> = {}): VolumeBackupView {
	return withOverrides(
		{
			id: "b1",
			serviceId: "svc",
			destinationId: "d1",
			destinationName: "Offsite S3",
			destinationKind: "s3",
			status: "succeeded",
			trigger: "manual",
			mode: "freeze",
			consistency: "freeze",
			createdAt: "2026-01-12T03:30:00Z",
			freezeMs: 600,
			stageMs: 100,
			sizeBytes: 2048,
			bytesAdded: 1024,
			files: 1234,
			locked: false,
			objectDeleted: false,
			restorable: true,
			snapshotId: "a1b2c3d4e5f6",
			volumes: ["data"],
			warnings: null,
		},
		over,
	);
}

function actions() {
	return {
		onRestore: mock(),
		onBrowse: mock(),
		onLock: mock(),
		onDelete: mock(),
	};
}

/** The table row that holds `text`. */
function rowOf(text: string | RegExp): HTMLElement {
	const row = screen.getByText(text).closest("tr");
	if (!row) throw new Error("no row");
	return row;
}

describe("HistoryTable", () => {
	test("lists runs with their column headings", async () => {
		await renderWithProviders(
			<HistoryTable
				items={[backup(), backup({ id: "b2", trigger: "schedule" })]}
				actions={actions()}
			/>,
		);
		for (const name of ["When", "Status", "Snapshot", "Size"]) {
			expect(screen.getByRole("columnheader", { name })).toBeInTheDocument();
		}
		// header row + two runs
		expect(screen.getAllByRole("row")).toHaveLength(3);
		expect(screen.getByText("Manual")).toBeInTheDocument();
		expect(screen.getByText("Scheduled")).toBeInTheDocument();
	});

	test("renders no run rows for an empty history", async () => {
		await renderWithProviders(<HistoryTable items={[]} actions={actions()} />);
		expect(screen.getAllByRole("row")).toHaveLength(1);
	});

	test("an unknown trigger is shown as it came", async () => {
		await renderWithProviders(
			<HistoryTable
				items={[backup({ trigger: "api" as VolumeBackupView["trigger"] })]}
				actions={actions()}
			/>,
		);
		expect(screen.getByText("api")).toBeInTheDocument();
	});

	test("a succeeded run shows status, summary, mode, volumes, file count, snapshot, destination and size", async () => {
		await renderWithProviders(
			<HistoryTable items={[backup()]} actions={actions()} />,
		);
		const row = within(rowOf("Succeeded"));
		expect(
			row.getByText("Done — paused 0.6 s, uploaded 1 KiB (deduplicated)"),
		).toBeInTheDocument();
		expect(
			row.getByText("Brief freeze · Volume data · 1,234 files"),
		).toBeInTheDocument();
		const snapshot = row.getByText("a1b2c3d4");
		expect(snapshot).toHaveAttribute("title", "a1b2c3d4e5f6");
		expect(row.getByText("Offsite S3")).toBeInTheDocument();
		expect(row.queryByText("not off-site")).not.toBeInTheDocument();
		expect(row.getByText("2 KiB")).toBeInTheDocument();
	});

	test("lists several volumes of one run", async () => {
		await renderWithProviders(
			<HistoryTable
				items={[backup({ volumes: ["data", "uploads"], files: 0 })]}
				actions={actions()}
			/>,
		);
		expect(
			screen.getByText("Brief freeze · 2 volumes: data, uploads"),
		).toBeInTheDocument();
	});

	test("a local destination is flagged as not off-site", async () => {
		await renderWithProviders(
			<HistoryTable
				items={[backup({ destinationKind: "local", destinationName: "Disk" })]}
				actions={actions()}
			/>,
		);
		expect(screen.getByText("Disk")).toBeInTheDocument();
		expect(screen.getByText("not off-site")).toBeInTheDocument();
	});

	test("a running run is in flight: no size, no snapshot, no restore actions, no delete", async () => {
		await renderWithProviders(
			<HistoryTable
				items={[
					backup({
						status: "running",
						snapshotId: undefined,
						restorable: false,
						sizeBytes: 0,
						files: 0,
					}),
				]}
				actions={actions()}
			/>,
		);
		const row = within(rowOf("Running"));
		expect(row.getByText("Backing up…")).toBeInTheDocument();
		expect(row.getByText("Brief freeze · Volume data")).toBeInTheDocument();
		// snapshot and size are placeholders: two dashes
		expect(row.getAllByText("–")).toHaveLength(2);
		expect(row.queryByRole("button")).not.toBeInTheDocument();
	});

	test("a queued run waits and offers no delete", async () => {
		await renderWithProviders(
			<HistoryTable
				items={[backup({ status: "queued", restorable: false })]}
				actions={actions()}
			/>,
		);
		const row = within(rowOf("Queued"));
		expect(row.getByText("Waiting to start")).toBeInTheDocument();
		expect(row.queryByRole("button")).not.toBeInTheDocument();
	});

	test("a failed run shows its error, no mode line, and can be deleted but not restored", async () => {
		await renderWithProviders(
			<HistoryTable
				items={[
					backup({
						status: "failed",
						error: "restic: repository locked",
						restorable: false,
						snapshotId: undefined,
					}),
				]}
				actions={actions()}
			/>,
		);
		const row = within(rowOf("Failed"));
		expect(row.getByText("restic: repository locked")).toBeInTheDocument();
		expect(row.queryByText(/Brief freeze/)).not.toBeInTheDocument();
		expect(row.getByText("–", { selector: "span" })).toBeInTheDocument();
		expect(
			row.getByRole("button", { name: "Delete backup" }),
		).toBeInTheDocument();
		expect(
			row.queryByRole("button", { name: "Restore" }),
		).not.toBeInTheDocument();
	});

	test("a failed run without an error says Failed in the summary", async () => {
		await renderWithProviders(
			<HistoryTable
				items={[backup({ status: "failed", restorable: false })]}
				actions={actions()}
			/>,
		);
		// pill label and summary
		expect(screen.getAllByText("Failed")).toHaveLength(2);
	});

	test("a skipped run shows why", async () => {
		await renderWithProviders(
			<HistoryTable
				items={[
					backup({
						status: "skipped",
						error: "Service is stopped",
						restorable: false,
					}),
				]}
				actions={actions()}
			/>,
		);
		const row = within(rowOf("Skipped"));
		expect(row.getByText("Service is stopped")).toBeInTheDocument();
	});

	test("an expired run says it was not delivered", async () => {
		await renderWithProviders(
			<HistoryTable
				items={[backup({ status: "expired", restorable: false })]}
				actions={actions()}
			/>,
		);
		expect(screen.getByText("Not delivered")).toBeInTheDocument();
		expect(
			screen.getByText("Not delivered: server offline. No changes were made."),
		).toBeInTheDocument();
	});

	test("warnings and a continued hook failure are listed under the run", async () => {
		await renderWithProviders(
			<HistoryTable
				items={[
					backup({
						hookOutcome: "failed_continued",
						warnings: ["file vanished: /data/tmp", "1 file unreadable"],
					}),
				]}
				actions={actions()}
			/>,
		);
		expect(
			screen.getByText("A command failed; the backup went on as you allowed."),
		).toBeInTheDocument();
		expect(screen.getByText("file vanished: /data/tmp")).toBeInTheDocument();
		expect(screen.getByText("1 file unreadable")).toBeInTheDocument();
	});

	test("the requested mode is shown when the agent reported none", async () => {
		await renderWithProviders(
			<HistoryTable
				items={[
					backup({ consistency: undefined, mode: "stop", freezeMs: 12000 }),
				]}
				actions={actions()}
			/>,
		);
		expect(screen.getByText(/Stop during backup/)).toBeInTheDocument();
		expect(screen.getByText(/stopped 12 s/)).toBeInTheDocument();
	});

	test("a restorable run offers Restore, Browse files, Lock and Delete that call back with the run", async () => {
		const a = actions();
		const b = backup();
		const user = userEvent.setup();
		await renderWithProviders(<HistoryTable items={[b]} actions={a} />);
		await user.click(screen.getByRole("button", { name: "Restore" }));
		expect(a.onRestore).toHaveBeenCalledTimes(1);
		expect(a.onRestore.mock.calls[0]?.[0]).toBe(b);
		await user.click(screen.getByRole("button", { name: /Browse files/ }));
		expect(a.onBrowse).toHaveBeenCalledWith(b);
		await user.click(screen.getByRole("button", { name: "Lock backup" }));
		expect(a.onLock).toHaveBeenCalledWith(b);
		await user.click(screen.getByRole("button", { name: "Delete backup" }));
		expect(a.onDelete).toHaveBeenCalledWith(b);
		expect(a.onRestore).toHaveBeenCalledTimes(1);
	});

	test("a locked run offers Unlock and says retention keeps it", async () => {
		await renderWithProviders(
			<HistoryTable items={[backup({ locked: true })]} actions={actions()} />,
		);
		const unlock = screen.getByRole("button", { name: "Unlock backup" });
		expect(unlock).toHaveTextContent("Locked");
		expect(unlock).toHaveAttribute(
			"title",
			"Locked: retention never removes it",
		);
		expect(
			screen.queryByRole("button", { name: "Lock backup" }),
		).not.toBeInTheDocument();
	});

	test("an unlocked run's lock button explains what locking does", async () => {
		await renderWithProviders(
			<HistoryTable items={[backup()]} actions={actions()} />,
		);
		expect(screen.getByRole("button", { name: "Lock backup" })).toHaveAttribute(
			"title",
			"Lock to keep it forever",
		);
	});

	test("a succeeded run that is not restorable has no restore, browse or lock", async () => {
		await renderWithProviders(
			<HistoryTable
				items={[backup({ restorable: false })]}
				actions={actions()}
			/>,
		);
		for (const name of [/^Restore$/, /Browse files/, /Lock backup/]) {
			expect(screen.queryByRole("button", { name })).not.toBeInTheDocument();
		}
		expect(
			screen.getByRole("button", { name: "Delete backup" }),
		).toBeInTheDocument();
	});

	test("a run whose snapshot was already removed cannot be deleted again", async () => {
		await renderWithProviders(
			<HistoryTable
				items={[backup({ objectDeleted: true, restorable: false })]}
				actions={actions()}
			/>,
		);
		expect(
			screen.queryByRole("button", { name: "Delete backup" }),
		).not.toBeInTheDocument();
	});

	test("the run's time is available as an absolute timestamp", async () => {
		await renderWithProviders(
			<HistoryTable items={[backup()]} actions={actions()} />,
		);
		const when = screen.getByText("Manual").previousElementSibling;
		expect(when).toHaveAttribute("title");
		expect(when?.getAttribute("title")).toContain("2026");
	});
});
