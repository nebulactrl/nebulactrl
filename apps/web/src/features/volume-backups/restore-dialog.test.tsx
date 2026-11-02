import { describe, expect, mock, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { VolumeBackupView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { VolumeRestoreDialog } from "./restore-dialog";

function backup(over: Partial<VolumeBackupView> = {}): VolumeBackupView {
	return {
		id: "b1",
		serviceId: "svc",
		destinationId: "d1",
		status: "succeeded",
		trigger: "manual",
		mode: "freeze",
		createdAt: "2026-01-12T03:30:00Z",
		freezeMs: 0,
		stageMs: 0,
		sizeBytes: 10,
		bytesAdded: 10,
		files: 1,
		locked: false,
		objectDeleted: false,
		restorable: true,
		volumes: ["data", "uploads"],
		warnings: null,
		...over,
	};
}

const RESTORE = "POST /volume-backups/:backupId/restore";

/** The reply that accepts a restore of backup `b1` of service `svc`: the run has just started. */
const accepted = () => ({
	status: 202,
	body: make("VolumeRestoreView", {
		id: "r1",
		backupId: "b1",
		serviceId: "svc",
		status: "restoring",
	}),
});

describe("VolumeRestoreDialog", () => {
	test("renders nothing while there is no backup", async () => {
		await renderWithProviders(
			<VolumeRestoreDialog
				backup={null}
				serviceSlug="web"
				currentVolumes={["data"]}
				onClose={() => {}}
			/>,
		);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
	});

	test("opens on the new-volume choice: the backup time, the first volume and the safe-by-default note", async () => {
		await renderWithProviders(
			<VolumeRestoreDialog
				backup={backup()}
				serviceSlug="web"
				currentVolumes={["data", "uploads"]}
				onClose={() => {}}
			/>,
		);
		const dialog = await screen.findByRole("dialog", {
			name: "Restore volume",
		});
		expect(dialog).toHaveTextContent(
			"Backup of Jan 12, 03:30 (2026-01-12 03:30:00 UTC).",
		);
		expect(screen.getByRole("combobox", { name: "Volume" })).toHaveTextContent(
			"data",
		);
		expect(screen.getByText("Safe by default")).toBeInTheDocument();
		expect(
			screen.getByText(/Restore Jan 12, 03:30 into a new volume\./),
		).toBeInTheDocument();
		expect(screen.queryByLabelText(/Type .* to continue/)).toBeNull();
		expect(
			screen.getByRole("button", { name: "Restore into new volume" }),
		).toBeEnabled();
	});

	test("restoring into a new volume posts the volume and mode, toasts, and closes", async () => {
		const api = stubApi({ [RESTORE]: accepted() });
		const onClose = mock();
		const user = userEvent.setup();
		await renderWithProviders(
			<VolumeRestoreDialog
				backup={backup()}
				serviceSlug="web"
				currentVolumes={["data", "uploads"]}
				onClose={onClose}
			/>,
		);
		await user.click(
			await screen.findByRole("button", { name: "Restore into new volume" }),
		);
		await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]).toMatchObject({
			method: "POST",
			path: "/volume-backups/b1/restore",
			body: { volume: "data", mode: "new_volume" },
		});
		expect(
			await screen.findByText("Restoring into a new volume"),
		).toBeVisible();
		expect(api.unmatched).toEqual([]);
	});

	test("a different volume can be chosen and is the one restored", async () => {
		const api = stubApi({ [RESTORE]: accepted() });
		const onClose = mock();
		const user = userEvent.setup();
		await renderWithProviders(
			<VolumeRestoreDialog
				backup={backup()}
				serviceSlug="web"
				currentVolumes={["data", "uploads"]}
				onClose={onClose}
			/>,
		);
		await user.click(await screen.findByRole("combobox", { name: "Volume" }));
		await user.click(await screen.findByRole("option", { name: "uploads" }));
		expect(screen.getByRole("combobox", { name: "Volume" })).toHaveTextContent(
			"uploads",
		);
		await user.click(
			screen.getByRole("button", { name: "Restore into new volume" }),
		);
		await waitFor(() => expect(onClose).toHaveBeenCalled());
		expect(api.calls[0]?.body).toEqual({
			volume: "uploads",
			mode: "new_volume",
		});
	});

	test("in place needs the service slug typed before it can be submitted", async () => {
		const api = stubApi({ [RESTORE]: accepted() });
		const onClose = mock();
		const user = userEvent.setup();
		await renderWithProviders(
			<VolumeRestoreDialog
				backup={backup()}
				serviceSlug="web"
				currentVolumes={["data", "uploads"]}
				onClose={onClose}
			/>,
		);
		await user.click(
			await screen.findByRole("button", { name: "Restore in place" }),
		);
		expect(screen.getByText("This overwrites the volume")).toBeInTheDocument();
		expect(
			screen.getByText(/This overwrites the files in data\./),
		).toBeInTheDocument();
		expect(screen.queryByText("Safe by default")).not.toBeInTheDocument();
		const submit = screen.getByRole("button", { name: "Overwrite volume" });
		expect(submit).toBeDisabled();

		const confirm = screen.getByRole("textbox");
		await user.type(confirm, "we");
		expect(submit).toBeDisabled();
		await user.type(confirm, "b");
		expect(submit).toBeEnabled();
		await user.type(confirm, "x");
		expect(submit).toBeDisabled();
		await user.clear(confirm);
		await user.type(confirm, "web");
		await user.click(submit);

		await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
		expect(api.calls[0]).toMatchObject({
			method: "POST",
			path: "/volume-backups/b1/restore",
			body: { volume: "data", mode: "in_place", confirmSlug: "web" },
		});
		expect(await screen.findByText("Restore started")).toBeInTheDocument();
		expect(
			screen.getByText(/A safety backup is taken first/),
		).toBeInTheDocument();
	});

	test("the in-place sentence names the chosen volume", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<VolumeRestoreDialog
				backup={backup()}
				serviceSlug="web"
				currentVolumes={["data", "uploads"]}
				onClose={() => {}}
			/>,
		);
		await user.click(await screen.findByRole("combobox", { name: "Volume" }));
		await user.click(await screen.findByRole("option", { name: "uploads" }));
		await user.click(screen.getByRole("button", { name: "Restore in place" }));
		expect(
			screen.getByText(/This overwrites the files in uploads\./),
		).toBeInTheDocument();
	});

	test("Enter in the confirmation box does not submit an unconfirmed in-place restore", async () => {
		const api = stubApi({ [RESTORE]: accepted() });
		const user = userEvent.setup();
		await renderWithProviders(
			<VolumeRestoreDialog
				backup={backup()}
				serviceSlug="web"
				currentVolumes={["data"]}
				onClose={() => {}}
			/>,
		);
		await user.click(
			await screen.findByRole("button", { name: "Restore in place" }),
		);
		await user.type(screen.getByRole("textbox"), "nope{Enter}");
		expect(api.calls).toEqual([]);
	});

	test("volumes dropped from the service are listed as not restorable and not offered", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<VolumeRestoreDialog
				backup={backup({ volumes: ["data", "old", "older"] })}
				serviceSlug="web"
				currentVolumes={["data"]}
				onClose={() => {}}
			/>,
		);
		expect(
			await screen.findByText(
				/old, older are no longer volumes of this service and can not be restored\./,
			),
		).toBeInTheDocument();
		await user.click(screen.getByRole("combobox", { name: "Volume" }));
		expect(await screen.findByRole("option", { name: "data" })).toBeVisible();
		expect(screen.queryByRole("option", { name: "old" })).toBeNull();
		expect(screen.queryByRole("option", { name: "older" })).toBeNull();
	});

	test("a single dropped volume reads in the singular", async () => {
		await renderWithProviders(
			<VolumeRestoreDialog
				backup={backup({ volumes: ["data", "old"] })}
				serviceSlug="web"
				currentVolumes={["data"]}
				onClose={() => {}}
			/>,
		);
		expect(
			await screen.findByText(
				/old is no longer a volume of this service and can not be restored\./,
			),
		).toBeInTheDocument();
	});

	test("when no volume of the backup is left nothing can be restored", async () => {
		const api = stubApi({ [RESTORE]: accepted() });
		const user = userEvent.setup();
		await renderWithProviders(
			<VolumeRestoreDialog
				backup={backup({ volumes: ["old"] })}
				serviceSlug="web"
				currentVolumes={["data"]}
				onClose={() => {}}
			/>,
		);
		const submit = await screen.findByRole("button", {
			name: "Restore into new volume",
		});
		expect(submit).toBeDisabled();
		await user.click(submit);
		expect(api.calls).toEqual([]);
	});

	test("a failed restore shows the server's reason, keeps the dialog open and allows a retry", async () => {
		let attempts = 0;
		const api = stubApi({
			[RESTORE]: () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{
								status: 409,
								title: "Conflict",
								code: "restore_in_progress",
								detail: "A restore of this volume is already running",
							},
							{ status: 409 },
						)
					: Response.json({}, { status: 202 });
			},
		});
		const onClose = mock();
		const user = userEvent.setup();
		await renderWithProviders(
			<VolumeRestoreDialog
				backup={backup()}
				serviceSlug="web"
				currentVolumes={["data"]}
				onClose={onClose}
			/>,
		);
		const submit = await screen.findByRole("button", {
			name: "Restore into new volume",
		});
		await user.click(submit);
		expect(
			await screen.findByText("A restore of this volume is already running"),
		).toBeInTheDocument();
		expect(onClose).not.toHaveBeenCalled();
		expect(screen.getByRole("dialog")).toBeInTheDocument();

		await user.click(submit);
		await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
		expect(
			screen.queryByText("A restore of this volume is already running"),
		).not.toBeInTheDocument();
		expect(api.calls).toHaveLength(2);
	});

	test("Cancel closes without calling the API", async () => {
		const api = stubApi({});
		const onClose = mock();
		const user = userEvent.setup();
		await renderWithProviders(
			<VolumeRestoreDialog
				backup={backup()}
				serviceSlug="web"
				currentVolumes={["data"]}
				onClose={onClose}
			/>,
		);
		await user.click(await screen.findByRole("button", { name: "Cancel" }));
		expect(onClose).toHaveBeenCalledTimes(1);
		expect(api.calls).toEqual([]);
	});

	test("Escape closes the dialog", async () => {
		const onClose = mock();
		const user = userEvent.setup();
		await renderWithProviders(
			<VolumeRestoreDialog
				backup={backup()}
				serviceSlug="web"
				currentVolumes={["data"]}
				onClose={onClose}
			/>,
		);
		await screen.findByRole("dialog");
		await user.keyboard("{Escape}");
		expect(onClose).toHaveBeenCalled();
	});

	test("opening for another backup starts fresh: new-volume mode, first volume, no typed text, no old error", async () => {
		stubApi({
			[RESTORE]: () =>
				Response.json(
					{ status: 500, title: "x", code: "internal", detail: "agent down" },
					{ status: 500 },
				),
		});
		const user = userEvent.setup();
		const props = {
			serviceSlug: "web",
			currentVolumes: ["data", "uploads"],
			onClose: () => {},
		};
		const first = backup();
		const view = await renderWithProviders(
			<VolumeRestoreDialog backup={first} {...props} />,
		);
		await user.click(
			await screen.findByRole("button", { name: "Restore in place" }),
		);
		await user.type(screen.getByRole("textbox"), "web");
		await user.click(screen.getByRole("button", { name: "Overwrite volume" }));
		expect(await screen.findByText("agent down")).toBeInTheDocument();
		view.unmount();

		// a second dialog instance for another backup begins from the defaults
		await renderWithProviders(
			<VolumeRestoreDialog
				backup={backup({ id: "b2", volumes: ["uploads"] })}
				{...props}
			/>,
		);
		expect(
			await screen.findByRole("button", { name: "Restore into new volume" }),
		).toBeEnabled();
		expect(screen.getByRole("combobox", { name: "Volume" })).toHaveTextContent(
			"uploads",
		);
		expect(screen.queryByText("agent down")).not.toBeInTheDocument();
	});
});
