import { describe, expect, mock, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import type {
	BackupView,
	DestinationView,
	ScheduleView,
} from "#/api/gen/types.gen";
import { type ApiRoutes, stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { backup, destination, schedule } from "./backups-panel.fixture";
import { BackupNowDialog, RestoreDialog, ScheduleDialog } from "./dialogs";

/** The reply that accepts a restore: the run has just been created. */
const restoreAccepted = (mode: "new" | "in_place" = "new") => ({
	status: 202,
	body: make("RestoreView", { mode, status: "provisioning" }),
});

const two: DestinationView[] = [
	destination({ id: "d1", name: "Offsite bucket" }),
	destination({ id: "d2", name: "Server disk", kind: "local", offSite: false }),
];

function stubDestinations(
	items: DestinationView[] = two,
	extra: ApiRoutes = {},
) {
	return stubApi({
		"GET /backup-destinations": { body: { items } },
		...extra,
	});
}

/** Opens a destination select and picks the option with `name`. */
async function pick(
	user: UserEvent,
	trigger: HTMLElement,
	name: string | RegExp,
) {
	await user.click(trigger);
	await user.click(await screen.findByRole("option", { name }));
}

describe("BackupNowDialog", () => {
	test("a closed dialog loads nothing", async () => {
		const api = stubDestinations();
		await renderWithProviders(
			<BackupNowDialog open={false} onOpenChange={() => {}} serviceId="s1" />,
		);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	test("starts a backup on the first destination by default", async () => {
		const api = stubDestinations(two, {
			"POST /services/:id/backups": {
				status: 202,
				body: backup({ status: "queued" }),
			},
		});
		const onOpenChange = mock((_open: boolean) => {});
		await renderWithProviders(
			<BackupNowDialog open onOpenChange={onOpenChange} serviceId="s1" />,
		);
		const dialog = await screen.findByRole("dialog");
		await within(dialog).findByText("Offsite bucket");
		await userEvent
			.setup()
			.click(within(dialog).getByRole("button", { name: "Back up now" }));
		expect(await screen.findByText("Backup started")).toBeInTheDocument();
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([
			{
				method: "POST",
				path: "/services/s1/backups",
				query: expect.anything(),
				body: { destinationId: "d1" },
			},
		]);
	});

	test("a chosen destination is the one sent, and local ones are marked", async () => {
		const api = stubDestinations(two, {
			"POST /services/:id/backups": { status: 202, body: backup() },
		});
		await renderWithProviders(
			<BackupNowDialog open onOpenChange={() => {}} serviceId="s9" />,
		);
		const user = userEvent.setup();
		const dialog = await screen.findByRole("dialog");
		await pick(
			user,
			await within(dialog).findByRole("combobox", { name: "Destination" }),
			"Server disk (not off-site)",
		);
		await user.click(
			within(dialog).getByRole("button", { name: "Back up now" }),
		);
		await screen.findByText("Backup started");
		const post = api.calls.find((c) => c.method === "POST");
		expect(post?.path).toBe("/services/s9/backups");
		expect(post?.body).toEqual({ destinationId: "d2" });
	});

	test("cannot start without any destination", async () => {
		stubDestinations([]);
		await renderWithProviders(
			<BackupNowDialog open onOpenChange={() => {}} serviceId="s1" />,
		);
		const dialog = await screen.findByRole("dialog");
		expect(
			within(dialog).getByRole("button", { name: "Back up now" }),
		).toBeDisabled();
		expect(
			await within(dialog).findByText("Choose a destination"),
		).toBeInTheDocument();
	});

	test("a refused start reports the reason and leaves the dialog open", async () => {
		stubDestinations(two, {
			"POST /services/:id/backups": problem(409, {
				title: "Conflict",
				detail: "a backup is already running",
			}),
		});
		const onOpenChange = mock((_open: boolean) => {});
		await renderWithProviders(
			<BackupNowDialog open onOpenChange={onOpenChange} serviceId="s1" />,
		);
		const dialog = await screen.findByRole("dialog");
		await within(dialog).findByText("Offsite bucket");
		await userEvent
			.setup()
			.click(within(dialog).getByRole("button", { name: "Back up now" }));
		expect(
			await screen.findByText("Could not start the backup"),
		).toBeInTheDocument();
		expect(screen.getByText("a backup is already running")).toBeInTheDocument();
		expect(screen.queryByText("Backup started")).not.toBeInTheDocument();
		expect(onOpenChange).not.toHaveBeenCalled();
	});

	test("Cancel closes without a request", async () => {
		const api = stubDestinations();
		const onOpenChange = mock((_open: boolean) => {});
		await renderWithProviders(
			<BackupNowDialog open onOpenChange={onOpenChange} serviceId="s1" />,
		);
		const dialog = await screen.findByRole("dialog");
		await userEvent
			.setup()
			.click(within(dialog).getByRole("button", { name: "Cancel" }));
		expect(onOpenChange).toHaveBeenCalledWith(false);
		expect(api.calls.some((c) => c.method === "POST")).toBe(false);
	});
});

describe("ScheduleDialog", () => {
	async function openDialog(
		props: { schedule?: ScheduleView | null } = {},
		extra: ApiRoutes = {},
		items: DestinationView[] = two,
	) {
		const api = stubDestinations(items, extra);
		const onOpenChange = mock((_open: boolean) => {});
		await renderWithProviders(
			<ScheduleDialog
				open
				onOpenChange={onOpenChange}
				serviceId="s1"
				schedule={props.schedule ?? null}
			/>,
		);
		const dialog = await screen.findByRole("dialog");
		await waitFor(() =>
			expect(
				within(dialog).getByRole("combobox", { name: "Destination" }),
			).not.toHaveTextContent("Choose a destination"),
		);
		return { api, onOpenChange, dialog, user: userEvent.setup() };
	}

	test("a new schedule starts daily at 03:00 and sends the defaults", async () => {
		const { api, onOpenChange, dialog, user } = await openDialog(
			{},
			{
				"POST /services/:id/backup-schedules": {
					status: 201,
					body: schedule(),
				},
			},
		);
		expect(within(dialog).getByText("Schedule backups")).toBeInTheDocument();
		expect(within(dialog).getByLabelText("Cron expression")).toHaveValue(
			"0 3 * * *",
		);
		expect(within(dialog).getByLabelText("Timezone")).toHaveValue("UTC");
		expect(within(dialog).getByLabelText("Keep the newest")).toHaveValue("7");
		expect(within(dialog).getByLabelText("Delete after (days)")).toHaveValue(
			"0",
		);
		expect(dialog).toHaveTextContent("Every day at 03:00.");

		await user.click(
			within(dialog).getByRole("button", { name: "Add schedule" }),
		);
		expect(await screen.findByText("Schedule added")).toBeInTheDocument();
		expect(screen.getByText("Every day at 03:00")).toBeInTheDocument();
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		const post = api.calls.find((c) => c.method === "POST");
		expect(post?.path).toBe("/services/s1/backup-schedules");
		expect(post?.body).toEqual({
			cron: "0 3 * * *",
			timezone: "UTC",
			retentionCount: 7,
			retentionDays: 0,
			destinationId: "d1",
		});
	});

	test("presets fill the cron expression and typing a preset's cron selects it", async () => {
		const { dialog, user } = await openDialog();
		const cron = within(dialog).getByLabelText("Cron expression");
		const preset = (name: string) =>
			within(dialog).getByRole("button", { name });
		await user.click(preset("Hourly"));
		expect(cron).toHaveValue("@hourly");
		expect(dialog).toHaveTextContent("Every hour.");
		await user.click(preset("Weekly Sun 03:00"));
		expect(cron).toHaveValue("0 3 * * 0");
		expect(dialog).toHaveTextContent("Every Sunday at 03:00.");

		await user.clear(cron);
		await user.type(cron, "*/10 * * * *");
		expect(preset("Custom")).toHaveAttribute("aria-pressed", "true");
		expect(dialog).toHaveTextContent("Every 10 minutes.");
		await user.clear(cron);
		await user.type(cron, "@hourly");
		expect(preset("Hourly")).toHaveAttribute("aria-pressed", "true");

		// Choosing Custom keeps whatever is typed.
		await user.click(preset("Custom"));
		expect(cron).toHaveValue("@hourly");
	});

	test("sends the trimmed cron, a blank timezone as UTC and the chosen destination and retention", async () => {
		const { api, dialog, user } = await openDialog(
			{},
			{
				"POST /services/:id/backup-schedules": {
					status: 201,
					body: schedule(),
				},
			},
		);
		await pick(
			user,
			within(dialog).getByRole("combobox", { name: "Destination" }),
			/Server disk/,
		);
		const cron = within(dialog).getByLabelText("Cron expression");
		await user.clear(cron);
		await user.type(cron, "  30 2 * * 1  ");
		await user.clear(within(dialog).getByLabelText("Timezone"));
		await user.clear(within(dialog).getByLabelText("Keep the newest"));
		await user.type(within(dialog).getByLabelText("Keep the newest"), "0");
		await user.clear(within(dialog).getByLabelText("Delete after (days)"));
		await user.type(within(dialog).getByLabelText("Delete after (days)"), "14");
		await user.click(
			within(dialog).getByRole("button", { name: "Add schedule" }),
		);
		await screen.findByText("Schedule added");
		expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
			cron: "30 2 * * 1",
			timezone: "UTC",
			retentionCount: 0,
			retentionDays: 14,
			destinationId: "d2",
		});
	});

	test.each([
		["Keep the newest", "-1"],
		["Keep the newest", "abc"],
		["Keep the newest", ""],
		["Delete after (days)", "-5"],
		["Delete after (days)", "x"],
		["Cron expression", ""],
	])("%s = %j blocks saving", async (label, value) => {
		const { api, dialog, user } = await openDialog();
		const field = within(dialog).getByLabelText(label);
		await user.clear(field);
		if (value) await user.type(field, value);
		const add = within(dialog).getByRole("button", { name: "Add schedule" });
		expect(add).toBeDisabled();
		await user.click(add);
		expect(api.calls.some((c) => c.method === "POST")).toBe(false);
	});

	test("without destinations the schedule cannot be saved", async () => {
		const { dialog } = await openDialogNoDestinations();
		expect(
			within(dialog).getByRole("button", { name: "Add schedule" }),
		).toBeDisabled();
	});

	async function openDialogNoDestinations() {
		stubDestinations([]);
		await renderWithProviders(
			<ScheduleDialog
				open
				onOpenChange={() => {}}
				serviceId="s1"
				schedule={null}
			/>,
		);
		const dialog = await screen.findByRole("dialog");
		await within(dialog).findByText("Choose a destination");
		return { dialog };
	}

	test("editing fills the form from the schedule and PATCHes it", async () => {
		const existing = schedule({
			id: "sch7",
			cron: "0 3 * * 0",
			timezone: "Europe/Berlin",
			retentionCount: 4,
			retentionDays: 90,
			destinationId: "d2",
		});
		const { api, onOpenChange, dialog, user } = await openDialog(
			{ schedule: existing },
			{ "PATCH /backup-schedules/:id": { body: existing } },
		);
		expect(within(dialog).getByText("Edit schedule")).toBeInTheDocument();
		expect(
			within(dialog).getByRole("button", {
				name: "Weekly Sun 03:00",
				pressed: true,
			}),
		).toBeInTheDocument();
		expect(within(dialog).getByLabelText("Cron expression")).toHaveValue(
			"0 3 * * 0",
		);
		expect(within(dialog).getByLabelText("Timezone")).toHaveValue(
			"Europe/Berlin",
		);
		expect(within(dialog).getByLabelText("Keep the newest")).toHaveValue("4");
		expect(within(dialog).getByLabelText("Delete after (days)")).toHaveValue(
			"90",
		);
		expect(
			within(dialog).getByRole("combobox", { name: "Destination" }),
		).toHaveTextContent("Server disk");

		await user.clear(within(dialog).getByLabelText("Keep the newest"));
		await user.type(within(dialog).getByLabelText("Keep the newest"), "10");
		await user.click(within(dialog).getByRole("button", { name: "Save" }));
		expect(await screen.findByText("Schedule saved")).toBeInTheDocument();
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		const patch = api.calls.find((c) => c.method === "PATCH");
		expect(patch?.path).toBe("/backup-schedules/sch7");
		expect(patch?.body).toEqual({
			cron: "0 3 * * 0",
			timezone: "Europe/Berlin",
			retentionCount: 10,
			retentionDays: 90,
			destinationId: "d2",
		});
		expect(api.calls.some((c) => c.method === "POST")).toBe(false);
	});

	test("a custom stored cron opens on the Custom preset", async () => {
		const { dialog } = await openDialog({
			schedule: schedule({ cron: "15 4 * * *" }),
		});
		expect(
			within(dialog).getByRole("button", { name: "Custom", pressed: true }),
		).toBeInTheDocument();
	});

	test("a rejected save shows the reason in the dialog and keeps it open", async () => {
		const { onOpenChange, dialog, user } = await openDialog(
			{},
			{
				"POST /services/:id/backup-schedules": problem(422, {
					title: "Invalid",
					detail: "cron fires more often than every 5 minutes",
				}),
			},
		);
		await user.click(
			within(dialog).getByRole("button", { name: "Add schedule" }),
		);
		expect(
			await within(dialog).findByText(
				"cron fires more often than every 5 minutes",
			),
		).toBeInTheDocument();
		expect(onOpenChange).not.toHaveBeenCalled();
		expect(screen.queryByText("Schedule added")).not.toBeInTheDocument();
		expect(
			within(dialog).getByRole("button", { name: "Add schedule" }),
		).toBeEnabled();
	});

	test("Cancel closes without a request", async () => {
		const { api, onOpenChange, dialog, user } = await openDialog();
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		expect(onOpenChange).toHaveBeenCalledWith(false);
		expect(api.calls.some((c) => c.method !== "GET")).toBe(false);
	});
});

describe("RestoreDialog", () => {
	const servers = [
		make("Server", { id: "srv1", name: "alpha", status: "online" }),
		make("Server", { id: "srv2", name: "beta", status: "online" }),
		make("Server", { id: "srv3", name: "gamma", status: "offline" }),
	];

	async function openRestore(
		b: BackupView | null,
		extra: ApiRoutes = {},
		serverId: string | null = "srv1",
	) {
		const api = stubApi({
			"GET /servers": { body: make("PageServer", { items: servers }) },
			...extra,
		});
		const onClose = mock(() => {});
		await renderWithProviders(
			<RestoreDialog
				backup={b}
				serviceSlug="orders-db"
				serverId={serverId}
				onClose={onClose}
			/>,
		);
		return { api, onClose, user: userEvent.setup() };
	}

	test("closed without a backup, and loads no servers", async () => {
		const { api } = await openRestore(null);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	test("restores into a new database on the database's own server by default", async () => {
		const { api, onClose, user } = await openRestore(
			backup({
				id: "b5",
				database: "orders",
				createdAt: "2026-01-02T03:00:00Z",
			}),
			{ "POST /backups/:id/restore": restoreAccepted() },
		);
		const dialog = await screen.findByRole("dialog");
		expect(dialog).toHaveTextContent("from orders");
		expect(within(dialog).getByText("Safe by default")).toBeInTheDocument();
		await user.click(
			within(dialog).getByRole("button", { name: "Restore into new database" }),
		);
		expect(
			await screen.findByText("Restoring into a new database"),
		).toBeInTheDocument();
		await waitFor(() => expect(onClose).toHaveBeenCalled());
		const post = api.calls.find((c) => c.method === "POST");
		expect(post?.path).toBe("/backups/b5/restore");
		expect(post?.body).toEqual({ mode: "new", serverId: "srv1" });
	});

	test("a typed name is trimmed and a chosen online server replaces the default", async () => {
		const { api, user } = await openRestore(backup(), {
			"POST /backups/:id/restore": restoreAccepted(),
		});
		const dialog = await screen.findByRole("dialog");
		await user.type(within(dialog).getByLabelText("Name"), "  copy-1  ");
		await user.click(within(dialog).getByRole("combobox", { name: "Server" }));
		// Only online servers are offered.
		expect(
			await screen.findByRole("option", { name: "alpha" }),
		).toBeInTheDocument();
		expect(screen.getByRole("option", { name: "beta" })).toBeInTheDocument();
		expect(
			screen.queryByRole("option", { name: "gamma" }),
		).not.toBeInTheDocument();
		await user.click(screen.getByRole("option", { name: "beta" }));
		await user.click(
			within(dialog).getByRole("button", { name: "Restore into new database" }),
		);
		await screen.findByText("Restoring into a new database");
		expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
			mode: "new",
			name: "copy-1",
			serverId: "srv2",
		});
	});

	test("without a known server the new-database restore cannot start", async () => {
		const { user, api } = await openRestore(backup(), {}, null);
		const dialog = await screen.findByRole("dialog");
		const go = within(dialog).getByRole("button", {
			name: "Restore into new database",
		});
		expect(go).toBeDisabled();
		await user.click(go);
		expect(api.calls.some((c) => c.method === "POST")).toBe(false);
		await user.click(within(dialog).getByRole("combobox", { name: "Server" }));
		await user.click(await screen.findByRole("option", { name: "alpha" }));
		expect(go).toBeEnabled();
	});

	test("a backup on a server's disk can only be restored on that server", async () => {
		const { api, user } = await openRestore(
			backup({ destinationKind: "local", serverId: "srv2" }),
			{ "POST /backups/:id/restore": restoreAccepted() },
			"srv1",
		);
		const dialog = await screen.findByRole("dialog");
		expect(dialog).toHaveTextContent("only be restored there");
		await user.click(within(dialog).getByRole("combobox", { name: "Server" }));
		expect(
			await screen.findByRole("option", { name: "beta" }),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("option", { name: "alpha" }),
		).not.toBeInTheDocument();
		await user.keyboard("{Escape}");
		await user.click(
			within(dialog).getByRole("button", { name: "Restore into new database" }),
		);
		await screen.findByText("Restoring into a new database");
		expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
			mode: "new",
			serverId: "srv2",
		});
	});

	test("restoring over the database needs its slug typed, then sends it", async () => {
		const { api, onClose, user } = await openRestore(backup({ id: "b6" }), {
			"POST /backups/:id/restore": restoreAccepted("in_place"),
		});
		const dialog = await screen.findByRole("dialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Over this database" }),
		);
		expect(
			within(dialog).getByText("This overwrites the database"),
		).toBeInTheDocument();
		expect(within(dialog).queryByLabelText("Name")).not.toBeInTheDocument();
		const go = within(dialog).getByRole("button", {
			name: "Overwrite database",
		});
		expect(go).toBeDisabled();

		const confirm = within(dialog).getByLabelText(/to confirm/);
		await user.type(confirm, "orders");
		expect(go).toBeDisabled();
		await user.type(confirm, "-db");
		expect(go).toBeEnabled();
		await user.type(confirm, "x");
		expect(go).toBeDisabled();
		await user.type(confirm, "{Backspace}");
		await user.click(go);
		expect(await screen.findByText("Restore started")).toBeInTheDocument();
		expect(
			screen.getByText("A safety backup is taken first."),
		).toBeInTheDocument();
		await waitFor(() => expect(onClose).toHaveBeenCalled());
		const post = api.calls.find((c) => c.method === "POST");
		expect(post?.path).toBe("/backups/b6/restore");
		expect(post?.body).toEqual({ mode: "in_place", confirmSlug: "orders-db" });
	});

	test("a refused restore shows the reason and keeps the dialog open", async () => {
		const { onClose, user } = await openRestore(backup(), {
			"POST /backups/:id/restore": problem(409, {
				title: "Conflict",
				detail: "the database is not running",
			}),
		});
		const dialog = await screen.findByRole("dialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Restore into new database" }),
		);
		expect(
			await within(dialog).findByText("the database is not running"),
		).toBeInTheDocument();
		expect(onClose).not.toHaveBeenCalled();
		expect(
			screen.queryByText("Restoring into a new database"),
		).not.toBeInTheDocument();
	});

	test("Cancel closes without a request", async () => {
		const { api, onClose, user } = await openRestore(backup());
		const dialog = await screen.findByRole("dialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		expect(onClose).toHaveBeenCalled();
		expect(api.calls.some((c) => c.method === "POST")).toBe(false);
	});
});
