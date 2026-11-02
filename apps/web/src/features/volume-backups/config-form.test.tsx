import { describe, expect, setDefaultTimeout, test } from "bun:test";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { useState } from "react";
import type {
	DestinationView,
	VolumeBackupConfigView,
} from "#/api/gen/types.gen";
import { type ApiStub, stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { ConfigForm } from "./config-form";
import { LOCAL_DESTINATION_WARNING } from "./model";

// Long edit sequences through userEvent are slow on a busy machine.
setDefaultTimeout(30_000);

function dest(over: Partial<DestinationView> = {}): DestinationView {
	return {
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
		...over,
	} as DestinationView;
}

const S3 = dest();
const LOCAL = dest({
	id: "dest-2",
	name: "Local disk",
	kind: "local",
	offSite: false,
});

/** What the API returns for a service that never saved volume backup settings. */
function unconfigured(
	over: Partial<VolumeBackupConfigView> = {},
): VolumeBackupConfigView {
	return {
		configured: false,
		eligible: true,
		serviceId: "svc-1",
		cron: "",
		enabled: false,
		excludes: null,
		freezeBudgetSeconds: 5,
		keepDaily: 7,
		keepWeekly: 4,
		keepMonthly: 6,
		mode: "freeze",
		repoReady: false,
		timezone: "UTC",
		volumes: [{ id: "v1", name: "data", mountPath: "/data" }],
		...over,
	};
}

function configured(
	over: Partial<VolumeBackupConfigView> = {},
): VolumeBackupConfigView {
	return unconfigured({
		configured: true,
		destinationId: "dest-1",
		destinationKind: "s3",
		destinationName: "Offsite S3",
		repoReady: true,
		...over,
	});
}

const PUT = "PUT /services/:serviceId/volume-backup";

/** What a save answers with: the stored settings. */
const saved = () => ({ body: configured() });

/** Replace the contents of a text field (paste: no key syntax to escape). */
async function fill(user: UserEvent, field: HTMLElement, text: string) {
	await user.clear(field);
	if (text !== "") {
		await user.click(field);
		await user.paste(text);
	}
}

function putBodies(api: ApiStub): unknown[] {
	return api.calls.filter((c) => c.method === "PUT").map((c) => c.body);
}

interface Handle {
	setConfig: (next: VolumeBackupConfigView) => void;
	setDestinations: (next: DestinationView[]) => void;
	added: number;
}

function Host({
	initial,
	destinations,
	handle,
}: {
	initial: VolumeBackupConfigView;
	destinations: DestinationView[];
	handle: Handle;
}) {
	const [config, setConfig] = useState(initial);
	const [items, setItems] = useState(destinations);
	handle.setConfig = setConfig;
	handle.setDestinations = setItems;
	return (
		<ConfigForm
			serviceId="svc-1"
			serviceSlug="shop"
			config={config}
			destinations={items}
			onAddDestination={() => {
				handle.added++;
			}}
		/>
	);
}

async function renderForm(
	initial: VolumeBackupConfigView,
	destinations: DestinationView[] = [S3, LOCAL],
) {
	const handle: Handle = {
		setConfig: () => {},
		setDestinations: () => {},
		added: 0,
	};
	const view = await renderWithProviders(
		<Host initial={initial} destinations={destinations} handle={handle} />,
	);
	return { ...view, handle };
}

const saveButton = (
	name: string | RegExp = /^(Save|Turn on volume backups)$/,
) => screen.getByRole("button", { name });

describe("ConfigForm: first save", () => {
	test("offers to turn backups on, saveable as is, with the first destination chosen", async () => {
		await renderForm(unconfigured());
		expect(screen.getByRole("combobox")).toHaveTextContent("Offsite S3");
		expect(saveButton("Turn on volume backups")).toBeEnabled();
		expect(screen.queryByRole("button", { name: "Turn off" })).toBeNull();
		expect(screen.queryByRole("button", { name: "Discard" })).toBeNull();
	});

	test("saving the defaults PUTs the whole config body, manual and disabled", async () => {
		const user = userEvent.setup();
		const api = stubApi({ [PUT]: saved() });
		await renderForm(unconfigured());
		await user.click(saveButton("Turn on volume backups"));
		expect(
			await screen.findByText("Volume backup settings saved"),
		).toBeVisible();
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]).toMatchObject({
			method: "PUT",
			path: "/services/svc-1/volume-backup",
			body: {
				destinationId: "dest-1",
				mode: "freeze",
				freezeBudgetSeconds: 5,
				excludes: [],
				keepDaily: 7,
				keepWeekly: 4,
				keepMonthly: 6,
				cron: "",
				timezone: "UTC",
				enabled: false,
			},
		});
		// No hooks are sent when the command lines are empty.
		const body = api.calls[0]?.body as Record<string, unknown>;
		expect(body).not.toHaveProperty("preHook");
		expect(body).not.toHaveProperty("postHook");
		expect(api.unmatched).toEqual([]);
	});

	test("without any destination the form cannot be saved and says why", async () => {
		const api = stubApi({ [PUT]: saved() });
		await renderForm(unconfigured(), []);
		expect(screen.getByText("Choose a destination.")).toBeVisible();
		expect(saveButton("Turn on volume backups")).toBeDisabled();
		expect(api.calls).toEqual([]);
	});

	test("Add reports that a destination should be created", async () => {
		const user = userEvent.setup();
		const { handle } = await renderForm(unconfigured());
		await user.click(screen.getByRole("button", { name: "Add" }));
		expect(handle.added).toBe(1);
	});
});

describe("ConfigForm: saved config", () => {
	test("shows the stored values and keeps Save disabled until something changes", async () => {
		await renderForm(
			configured({
				mode: "stop",
				excludes: ["/data/cache", "*.tmp"],
				keepDaily: 3,
				keepWeekly: 2,
				keepMonthly: 1,
				cron: "0 3 * * *",
				timezone: "Europe/Berlin",
				enabled: true,
				preHook: {
					command: ["sqlite3", "/data/app.db", ".backup /tmp/x"],
					user: "app",
					timeoutSeconds: 90,
					continueOnError: true,
				},
			}),
		);
		expect(
			screen.getByRole("radio", { name: /Stop during backup/ }),
		).toBeChecked();
		// A stopped service has no freeze budget to set.
		expect(screen.queryByLabelText("Longest pause")).toBeNull();
		expect(screen.getByLabelText("Excludes")).toHaveValue("/data/cache\n*.tmp");
		expect(screen.getByLabelText("Daily")).toHaveValue("3");
		expect(screen.getByLabelText("Weekly")).toHaveValue("2");
		expect(screen.getByLabelText("Monthly")).toHaveValue("1");
		expect(screen.getByLabelText("Cron expression")).toHaveValue("0 3 * * *");
		expect(screen.getByLabelText("Timezone")).toHaveValue("Europe/Berlin");
		expect(
			screen.getByRole("switch", { name: "Runs on this schedule" }),
		).toBeChecked();
		expect(screen.getByLabelText("Before backup")).toHaveValue(
			"sqlite3 /data/app.db '.backup /tmp/x'",
		);
		expect(screen.getByLabelText("Run as user")).toHaveValue("app");
		expect(screen.getByLabelText("Timeout (s)")).toHaveValue("90");
		expect(screen.getByLabelText("After backup")).toHaveValue("");
		expect(screen.getByRole("button", { name: "Daily 03:00" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(saveButton("Save")).toBeDisabled();
		expect(screen.getByRole("button", { name: "Turn off" })).toBeVisible();
		expect(screen.queryByRole("button", { name: "Discard" })).toBeNull();
	});

	test("an edit enables Save and Discard; Discard restores the stored values", async () => {
		const user = userEvent.setup();
		await renderForm(configured({ keepDaily: 3 }));
		await fill(user, screen.getByLabelText("Daily"), "10");
		expect(saveButton("Save")).toBeEnabled();
		await user.click(screen.getByRole("button", { name: "Discard" }));
		expect(screen.getByLabelText("Daily")).toHaveValue("3");
		expect(saveButton("Save")).toBeDisabled();
		expect(screen.queryByRole("button", { name: "Discard" })).toBeNull();
	});

	test("saves every edited field in the request body", async () => {
		const user = userEvent.setup();
		const api = stubApi({ [PUT]: saved() });
		await renderForm(configured());

		await user.click(screen.getByRole("combobox"));
		await user.click(await screen.findByRole("option", { name: /Local disk/ }));
		await user.click(
			screen.getByRole("radio", { name: /Keep running, no freeze/ }),
		);
		await fill(
			user,
			screen.getByLabelText("Before backup"),
			`sqlite3 /data/a.db ".backup '/data/b.db'"`,
		);
		await fill(user, screen.getByLabelText("Run as user"), " app ");
		await fill(user, screen.getByLabelText("Timeout (s)"), "90");
		await user.click(screen.getByRole("button", { name: "Continue anyway" }));
		await fill(user, screen.getByLabelText("After backup"), "rm -f /data/b.db");
		await fill(
			user,
			screen.getByLabelText("Excludes"),
			"/data/cache\n  *.tmp  \n\n/data/cache",
		);
		await fill(user, screen.getByLabelText("Daily"), "14");
		await fill(user, screen.getByLabelText("Weekly"), "0");
		await fill(user, screen.getByLabelText("Monthly"), "12");
		await user.click(screen.getByRole("button", { name: "Weekly Sun 03:00" }));
		await fill(user, screen.getByLabelText("Timezone"), " Europe/Berlin ");
		await user.click(saveButton("Save"));

		await screen.findByText("Volume backup settings saved");
		expect(putBodies(api)).toEqual([
			{
				destinationId: "dest-2",
				mode: "live",
				freezeBudgetSeconds: 5,
				preHook: {
					command: ["sqlite3", "/data/a.db", ".backup '/data/b.db'"],
					user: "app",
					timeoutSeconds: 90,
					continueOnError: true,
				},
				postHook: {
					command: ["rm", "-f", "/data/b.db"],
					timeoutSeconds: 300,
					continueOnError: false,
				},
				excludes: ["/data/cache", "*.tmp"],
				keepDaily: 14,
				keepWeekly: 0,
				keepMonthly: 12,
				cron: "0 3 * * 0",
				timezone: "Europe/Berlin",
				enabled: true,
			},
		]);
		expect(api.unmatched).toEqual([]);
	});

	test("the freeze budget is sent only through the freeze mode's own field", async () => {
		const user = userEvent.setup();
		const api = stubApi({ [PUT]: saved() });
		await renderForm(configured());
		await fill(user, screen.getByLabelText("Longest pause"), "30");
		await user.click(saveButton("Save"));
		await screen.findByText("Volume backup settings saved");
		expect(putBodies(api)[0]).toMatchObject({
			mode: "freeze",
			freezeBudgetSeconds: 30,
		});
	});

	test("choosing Stop hides the pause budget and Brief freeze brings it back", async () => {
		const user = userEvent.setup();
		await renderForm(configured());
		expect(screen.getByLabelText("Longest pause")).toBeVisible();
		await user.click(screen.getByRole("radio", { name: /Stop during backup/ }));
		expect(screen.queryByLabelText("Longest pause")).toBeNull();
		await user.click(screen.getByRole("radio", { name: /Brief freeze/ }));
		expect(screen.getByLabelText("Longest pause")).toHaveValue("5");
	});

	test("a failed save shows the server's reason in the form and keeps the edits", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			[PUT]: problem(422, {
				title: "Unprocessable",
				detail: "Destination is not usable.",
			}),
		});
		await renderForm(configured({ keepDaily: 3 }));
		await fill(user, screen.getByLabelText("Daily"), "9");
		await user.click(saveButton("Save"));
		expect(await screen.findByText("Destination is not usable.")).toBeVisible();
		expect(screen.queryByText("Volume backup settings saved")).toBeNull();
		expect(screen.getByLabelText("Daily")).toHaveValue("9");
		expect(saveButton("Save")).toBeEnabled();
		expect(api.calls).toHaveLength(1);

		// The next attempt clears the old message before it goes out.
		await user.click(saveButton("Save"));
		await waitFor(() => expect(api.calls).toHaveLength(2));
	});

	test("Enter in a field submits the form only when it is valid and changed", async () => {
		const user = userEvent.setup();
		const api = stubApi({ [PUT]: saved() });
		await renderForm(configured({ keepDaily: 3 }));
		await user.type(screen.getByLabelText("Weekly"), "{Enter}");
		expect(api.calls).toEqual([]);
		await fill(user, screen.getByLabelText("Daily"), "x");
		await user.type(screen.getByLabelText("Daily"), "{Enter}");
		expect(api.calls).toEqual([]);
		await fill(user, screen.getByLabelText("Daily"), "5");
		await user.type(screen.getByLabelText("Daily"), "{Enter}");
		await waitFor(() => expect(putBodies(api)).toHaveLength(1));
	});
});

describe("ConfigForm: validation", () => {
	test.each([
		["0", "Enter 1 to 60 seconds."],
		["61", "Enter 1 to 60 seconds."],
		["abc", "Enter 1 to 60 seconds."],
		["-5", "Enter 1 to 60 seconds."],
		["", "Enter 1 to 60 seconds."],
	])("freeze budget %p is refused", async (value, message) => {
		const user = userEvent.setup();
		const api = stubApi({ [PUT]: saved() });
		await renderForm(configured());
		await fill(user, screen.getByLabelText("Longest pause"), value);
		expect(screen.getByText(message)).toBeVisible();
		expect(screen.getByLabelText("Longest pause")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		expect(saveButton("Save")).toBeDisabled();
		expect(api.calls).toEqual([]);
	});

	test.each(["1", "60"])(
		"freeze budget %p at the limit is accepted",
		async (value) => {
			const user = userEvent.setup();
			await renderForm(configured());
			await fill(user, screen.getByLabelText("Longest pause"), value);
			expect(screen.queryByText("Enter 1 to 60 seconds.")).toBeNull();
			expect(saveButton("Save")).toBeEnabled();
		},
	);

	test.each([
		["Daily", "-1"],
		["Weekly", "1.5"],
		["Monthly", "many"],
		["Daily", ""],
	])("retention %s=%p is refused", async (label, value) => {
		const user = userEvent.setup();
		await renderForm(configured());
		await fill(user, screen.getByLabelText(label), value);
		expect(
			screen.getByText("Enter 0 or more; 0 means no limit."),
		).toBeVisible();
		expect(screen.getByLabelText(label)).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		expect(saveButton("Save")).toBeDisabled();
	});

	test("zero retention means no limit and is accepted", async () => {
		const user = userEvent.setup();
		const api = stubApi({ [PUT]: saved() });
		await renderForm(configured());
		for (const label of ["Daily", "Weekly", "Monthly"])
			await fill(user, screen.getByLabelText(label), "0");
		await user.click(saveButton("Save"));
		await screen.findByText("Volume backup settings saved");
		expect(putBodies(api)[0]).toMatchObject({
			keepDaily: 0,
			keepWeekly: 0,
			keepMonthly: 0,
		});
	});

	test("a hook command with an open quote is refused", async () => {
		const user = userEvent.setup();
		const api = stubApi({ [PUT]: saved() });
		await renderForm(configured());
		await fill(user, screen.getByLabelText("Before backup"), `echo "oops`);
		expect(screen.getByText("A quote is not closed.")).toBeVisible();
		expect(saveButton("Save")).toBeDisabled();
		await fill(user, screen.getByLabelText("Before backup"), `echo "ok"`);
		expect(screen.queryByText("A quote is not closed.")).toBeNull();
		expect(saveButton("Save")).toBeEnabled();
		expect(api.calls).toEqual([]);
	});

	test("the after-backup hook is validated on its own", async () => {
		const user = userEvent.setup();
		await renderForm(configured());
		await fill(user, screen.getByLabelText("After backup"), "rm 'x");
		expect(screen.getByText("A quote is not closed.")).toBeVisible();
		expect(screen.getByLabelText("After backup")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		expect(screen.getByLabelText("Before backup")).not.toHaveAttribute(
			"aria-invalid",
		);
		expect(saveButton("Save")).toBeDisabled();
	});

	test.each(["0", "3601", "soon"])(
		"a hook timeout of %p is refused",
		async (value) => {
			const user = userEvent.setup();
			await renderForm(configured());
			await fill(user, screen.getByLabelText("Before backup"), "sync");
			await fill(user, screen.getByLabelText("Timeout (s)"), value);
			expect(screen.getByText("Enter 1 to 3600 seconds.")).toBeVisible();
			expect(saveButton("Save")).toBeDisabled();
		},
	);

	test("a hook timeout at the upper limit is accepted", async () => {
		const user = userEvent.setup();
		const api = stubApi({ [PUT]: saved() });
		await renderForm(configured());
		await fill(user, screen.getByLabelText("Before backup"), "sync");
		await fill(user, screen.getByLabelText("Timeout (s)"), "3600");
		await user.click(saveButton("Save"));
		await screen.findByText("Volume backup settings saved");
		expect(putBodies(api)[0]).toMatchObject({
			preHook: { command: ["sync"], timeoutSeconds: 3600 },
		});
	});

	test("a hook timeout is not checked while the command is empty", async () => {
		const user = userEvent.setup();
		const api = stubApi({ [PUT]: saved() });
		await renderForm(configured({ keepDaily: 3 }));
		await fill(user, screen.getByLabelText("Before backup"), "sync");
		await fill(user, screen.getByLabelText("Timeout (s)"), "0");
		expect(saveButton("Save")).toBeDisabled();
		await fill(user, screen.getByLabelText("Before backup"), "");
		await fill(user, screen.getByLabelText("Daily"), "4");
		await user.click(saveButton("Save"));
		await screen.findByText("Volume backup settings saved");
		expect(putBodies(api)[0]).not.toHaveProperty("preHook");
	});

	test("a custom schedule needs a cron expression", async () => {
		const user = userEvent.setup();
		const api = stubApi({ [PUT]: saved() });
		await renderForm(configured());
		await user.click(screen.getByRole("button", { name: "Custom" }));
		expect(screen.getByLabelText("Cron expression")).toHaveValue("");
		expect(screen.getByText("Enter a cron expression.")).toBeVisible();
		expect(screen.getByLabelText("Cron expression")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		expect(saveButton("Save")).toBeDisabled();

		await fill(user, screen.getByLabelText("Cron expression"), "*/15 * * * *");
		expect(screen.queryByText("Enter a cron expression.")).toBeNull();
		await user.click(saveButton("Save"));
		await screen.findByText("Volume backup settings saved");
		expect(putBodies(api)[0]).toMatchObject({
			cron: "*/15 * * * *",
			enabled: true,
		});
	});

	test("an unsaved invalid draft never reaches the server, even via a stray submit", async () => {
		const user = userEvent.setup();
		const api = stubApi({ [PUT]: saved() });
		await renderForm(unconfigured(), []);
		await user.type(screen.getByLabelText("Daily"), "{Enter}");
		expect(api.calls).toEqual([]);
	});
});

describe("ConfigForm: schedule", () => {
	test("Manual only hides the cron fields", async () => {
		await renderForm(configured());
		expect(screen.getByRole("button", { name: "Manual only" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(screen.queryByLabelText("Cron expression")).toBeNull();
		expect(screen.queryByLabelText("Timezone")).toBeNull();
		expect(screen.queryByRole("switch")).toBeNull();
	});

	test("a preset fills the cron expression and describes it", async () => {
		const user = userEvent.setup();
		await renderForm(configured());
		await user.click(screen.getByRole("button", { name: "Hourly" }));
		expect(screen.getByLabelText("Cron expression")).toHaveValue("@hourly");
		expect(screen.getByText(/At most one run every 5 minutes/)).toBeVisible();
		expect(
			screen.getByRole("switch", { name: "Runs on this schedule" }),
		).toBeChecked();
		await user.click(screen.getByRole("button", { name: "Daily 03:00" }));
		expect(screen.getByLabelText("Cron expression")).toHaveValue("0 3 * * *");
	});

	test("typing a preset's expression selects that preset; anything else is Custom", async () => {
		const user = userEvent.setup();
		await renderForm(configured({ cron: "@hourly", enabled: true }));
		expect(screen.getByRole("button", { name: "Hourly" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		await fill(user, screen.getByLabelText("Cron expression"), "0 3 * * *");
		expect(screen.getByRole("button", { name: "Daily 03:00" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		await fill(user, screen.getByLabelText("Cron expression"), "7 7 * * *");
		expect(screen.getByRole("button", { name: "Custom" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
	});

	test("emptying the cron field of a preset moves to Custom instead of Manual", async () => {
		const user = userEvent.setup();
		await renderForm(configured({ cron: "@hourly", enabled: true }));
		await user.clear(screen.getByLabelText("Cron expression"));
		expect(screen.getByRole("button", { name: "Custom" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		// The field stays on screen so a new expression can be typed.
		expect(screen.getByLabelText("Cron expression")).toBeVisible();
	});

	test("pausing a schedule saves it with enabled=false and the cron kept", async () => {
		const user = userEvent.setup();
		const api = stubApi({ [PUT]: saved() });
		await renderForm(configured({ cron: "@hourly", enabled: true }));
		await user.click(
			screen.getByRole("switch", { name: "Runs on this schedule" }),
		);
		expect(screen.getByRole("switch", { name: "Paused" })).not.toBeChecked();
		await user.click(saveButton("Save"));
		await screen.findByText("Volume backup settings saved");
		expect(putBodies(api)[0]).toMatchObject({
			cron: "@hourly",
			enabled: false,
		});
	});

	test("a paused stored schedule shows as paused", async () => {
		await renderForm(configured({ cron: "@hourly", enabled: false }));
		expect(screen.getByRole("switch", { name: "Paused" })).not.toBeChecked();
	});

	test("picking a preset resumes a paused schedule", async () => {
		const user = userEvent.setup();
		const api = stubApi({ [PUT]: saved() });
		await renderForm(configured({ cron: "@hourly", enabled: false }));
		await user.click(screen.getByRole("button", { name: "Daily 03:00" }));
		expect(
			screen.getByRole("switch", { name: "Runs on this schedule" }),
		).toBeChecked();
		await user.click(saveButton("Save"));
		await screen.findByText("Volume backup settings saved");
		expect(putBodies(api)[0]).toMatchObject({
			cron: "0 3 * * *",
			enabled: true,
		});
	});

	test("going back to Manual only saves an empty cron and disabled", async () => {
		const user = userEvent.setup();
		const api = stubApi({ [PUT]: saved() });
		await renderForm(configured({ cron: "0 3 * * *", enabled: true }));
		await user.click(screen.getByRole("button", { name: "Manual only" }));
		expect(screen.queryByLabelText("Cron expression")).toBeNull();
		await user.click(saveButton("Save"));
		await screen.findByText("Volume backup settings saved");
		expect(putBodies(api)[0]).toMatchObject({ cron: "", enabled: false });
	});

	test("a blank timezone is saved as UTC", async () => {
		const user = userEvent.setup();
		const api = stubApi({ [PUT]: saved() });
		await renderForm(
			configured({ cron: "@daily", enabled: true, timezone: "Asia/Tokyo" }),
		);
		await fill(user, screen.getByLabelText("Timezone"), "  ");
		await user.click(saveButton("Save"));
		await screen.findByText("Volume backup settings saved");
		expect(putBodies(api)[0]).toMatchObject({ timezone: "UTC" });
	});
});

describe("ConfigForm: destination", () => {
	test("the picker lists every destination and flags the ones that are not off-site", async () => {
		const user = userEvent.setup();
		await renderForm(configured());
		await user.click(screen.getByRole("combobox"));
		expect(
			await screen.findByRole("option", { name: "Offsite S3" }),
		).toBeVisible();
		expect(
			screen.getByRole("option", { name: "Local disk (not off-site)" }),
		).toBeVisible();
	});

	test("a local destination warns that it does not protect against losing the server", async () => {
		const user = userEvent.setup();
		await renderForm(configured());
		expect(screen.queryByText(LOCAL_DESTINATION_WARNING)).toBeNull();
		await user.click(screen.getByRole("combobox"));
		await user.click(await screen.findByRole("option", { name: /Local disk/ }));
		expect(screen.getByText(LOCAL_DESTINATION_WARNING)).toBeVisible();
		await user.click(screen.getByRole("combobox"));
		await user.click(await screen.findByRole("option", { name: "Offsite S3" }));
		expect(screen.queryByText(LOCAL_DESTINATION_WARNING)).toBeNull();
	});

	test("a stored local destination warns from the start", async () => {
		await renderForm(configured({ destinationId: "dest-2" }));
		expect(screen.getByText(LOCAL_DESTINATION_WARNING)).toBeVisible();
	});
});

describe("ConfigForm: encryption key", () => {
	test("before the first backup there is no key to download", async () => {
		await renderForm(configured({ repoReady: false }));
		expect(
			screen.getByText(
				"The key is created with the first backup. Download it then.",
			),
		).toBeVisible();
		expect(
			screen.queryByRole("button", { name: "Download recovery key" }),
		).toBeNull();
	});

	test("once the repository exists the recovery key can be requested", async () => {
		await renderForm(configured({ repoReady: true }));
		expect(
			screen.getByRole("button", { name: "Download recovery key" }),
		).toBeVisible();
		expect(
			screen.queryByText(/The key is created with the first backup/),
		).toBeNull();
	});
});

describe("ConfigForm: turning off", () => {
	test("asks first, then DELETEs the config and says backups are kept", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"DELETE /services/:serviceId/volume-backup": { status: 204 },
		});
		await renderForm(configured());
		await user.click(screen.getByRole("button", { name: "Turn off" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent("Turn off volume backups?");
		expect(dialog).toHaveTextContent("Backups already made are kept");
		expect(api.calls).toEqual([]);
		await user.click(within(dialog).getByRole("button", { name: "Turn off" }));
		expect(await screen.findByText("Volume backups turned off")).toBeVisible();
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]).toMatchObject({
			method: "DELETE",
			path: "/services/svc-1/volume-backup",
		});
	});

	test("cancelling sends nothing", async () => {
		const user = userEvent.setup();
		const api = stubApi({});
		await renderForm(configured());
		await user.click(screen.getByRole("button", { name: "Turn off" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(api.calls).toEqual([]);
	});

	test("a failure keeps the dialog open with the reason", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"DELETE /services/:serviceId/volume-backup": problem(500, {
				title: "Internal",
				detail: "Database is locked.",
			}),
		});
		await renderForm(configured());
		await user.click(screen.getByRole("button", { name: "Turn off" }));
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Turn off" }));
		expect(await screen.findByText("Could not turn them off")).toBeVisible();
		expect(screen.queryByText("Volume backups turned off")).toBeNull();
		expect(screen.getByRole("alertdialog")).toBeVisible();
		expect(api.calls).toHaveLength(1);
	});

	test("an unconfigured service has nothing to turn off", async () => {
		await renderForm(unconfigured());
		expect(screen.queryByRole("button", { name: "Turn off" })).toBeNull();
	});
});

describe("ConfigForm: following the control plane", () => {
	test("a changed stored config replaces the draft", async () => {
		const { handle } = await renderForm(configured({ keepDaily: 3 }));
		expect(screen.getByLabelText("Daily")).toHaveValue("3");
		act(() => handle.setConfig(configured({ keepDaily: 8 })));
		await waitFor(() =>
			expect(screen.getByLabelText("Daily")).toHaveValue("8"),
		);
	});

	test("an unrelated change (the next run time) does not wipe a half-made edit", async () => {
		const user = userEvent.setup();
		const { handle } = await renderForm(configured({ keepDaily: 3 }));
		await fill(user, screen.getByLabelText("Daily"), "11");
		act(() =>
			handle.setConfig(
				configured({ keepDaily: 3, nextRunAt: "2026-11-02T03:00:00Z" }),
			),
		);
		expect(screen.getByLabelText("Daily")).toHaveValue("11");
		expect(saveButton("Save")).toBeEnabled();
	});

	test("a new destination list keeps the draft; the first destination only fills an empty choice", async () => {
		const user = userEvent.setup();
		const { handle } = await renderForm(unconfigured(), [S3]);
		await fill(user, screen.getByLabelText("Weekly"), "9");
		act(() => handle.setDestinations([S3, LOCAL]));
		expect(screen.getByLabelText("Weekly")).toHaveValue("9");
		expect(screen.getByRole("combobox")).toHaveTextContent("Offsite S3");
	});

	test("the destination appears once the first one is created", async () => {
		const { handle } = await renderForm(unconfigured(), []);
		expect(screen.getByText("Choose a destination.")).toBeVisible();
		act(() => handle.setDestinations([LOCAL]));
		await waitFor(() =>
			expect(screen.queryByText("Choose a destination.")).toBeNull(),
		);
		expect(screen.getByRole("combobox")).toHaveTextContent("Local disk");
		expect(saveButton("Turn on volume backups")).toBeEnabled();
	});
});
