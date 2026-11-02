import { describe, expect, mock, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { browserTimezone } from "./model";
import { TaskDialog } from "./task-dialog";
import { fill, problem, taskView } from "./test-fixtures";

const preview = {
	"POST /task-schedules/preview": {
		body: { valid: true, description: "Every day", nextRuns: [] },
	},
};

async function renderDialog(task = null as ReturnType<typeof taskView> | null) {
	const onOpenChange = mock((_open: boolean) => {});
	await renderWithProviders(
		<TaskDialog
			open
			onOpenChange={onOpenChange}
			serviceId="svc-1"
			task={task}
		/>,
	);
	return onOpenChange;
}

const pressed = (name: string) =>
	screen.getByRole("button", { name, pressed: true });

describe("TaskDialog: add", () => {
	test("starts with a daily 03:00 UTC schedule and cannot be saved until named and given a command", async () => {
		const user = userEvent.setup();
		stubApi(preview);
		await renderDialog();
		expect(
			screen.getByRole("dialog", { name: "Add a task" }),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Cron expression")).toHaveValue("0 3 * * *");
		expect(pressed("Daily 03:00")).toBeInTheDocument();
		expect(screen.getByLabelText("Time zone")).toHaveValue("UTC");
		expect(screen.getByLabelText("Timeout (seconds)")).toHaveValue("1800");
		expect(pressed("Skip the tick")).toBeInTheDocument();
		expect(screen.getByRole("switch", { name: "Enabled" })).toBeChecked();
		const save = screen.getByRole("button", { name: "Add task" });
		expect(save).toBeDisabled();
		await fill(user, screen.getByLabelText("Name"), "Nightly cleanup");
		expect(save).toBeDisabled();
		await fill(user, screen.getByLabelText("Command"), "rake cleanup");
		expect(save).toBeEnabled();
		await user.clear(screen.getByLabelText("Name"));
		expect(save).toBeDisabled();
	});

	test("posts the task with the command split into an argv, then closes with a toast", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			...preview,
			"POST /services/:serviceId/tasks": { status: 201, body: taskView() },
			"GET /services/:serviceId/tasks": { body: { items: [] } },
		});
		const onOpenChange = await renderDialog();
		await fill(user, screen.getByLabelText("Name"), "  Nightly cleanup ");
		await fill(
			user,
			screen.getByLabelText("Command"),
			'sh -c "rake cleanup && rake report"',
		);
		await user.click(screen.getByRole("button", { name: "Add task" }));
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		const post = api.calls.find(
			(c) => c.method === "POST" && c.path.endsWith("/tasks"),
		);
		expect(post).toMatchObject({
			path: "/services/svc-1/tasks",
			body: {
				name: "Nightly cleanup",
				schedule: "0 3 * * *",
				command: ["sh", "-c", "rake cleanup && rake report"],
				enabled: true,
				timeoutSeconds: 1800,
				concurrency: "forbid",
				timezone: "UTC",
			},
		});
		expect(await screen.findByText("Task added")).toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});

	test("sends the chosen preset, zone, timeout, concurrency and a paused state", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			...preview,
			"POST /services/:serviceId/tasks": { status: 201, body: taskView() },
		});
		const onOpenChange = await renderDialog();
		await fill(user, screen.getByLabelText("Name"), "Report");
		await fill(user, screen.getByLabelText("Command"), "rake report");
		await user.click(screen.getByRole("button", { name: "Weekly" }));
		expect(screen.getByLabelText("Cron expression")).toHaveValue("0 3 * * 0");
		await user.clear(screen.getByLabelText("Time zone"));
		await fill(user, screen.getByLabelText("Time zone"), "Europe/Berlin");
		await user.clear(screen.getByLabelText("Timeout (seconds)"));
		await fill(user, screen.getByLabelText("Timeout (seconds)"), "120");
		await user.click(screen.getByRole("button", { name: "Run anyway" }));
		expect(
			screen.getByText(/Allow starts a new run on every tick/),
		).toBeInTheDocument();
		await user.click(screen.getByRole("switch", { name: "Enabled" }));
		expect(screen.getByRole("switch", { name: "Paused" })).not.toBeChecked();
		await user.click(screen.getByRole("button", { name: "Add task" }));
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		expect(
			api.calls.find((c) => c.method === "POST" && c.path.endsWith("/tasks"))
				?.body,
		).toEqual({
			name: "Report",
			schedule: "0 3 * * 0",
			command: ["rake", "report"],
			enabled: false,
			timeoutSeconds: 120,
			concurrency: "allow",
			timezone: "Europe/Berlin",
		});
		expect(
			await screen.findByText("It is paused until you turn it on."),
		).toBeInTheDocument();
	});

	test("an empty time zone is sent as UTC", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			...preview,
			"POST /services/:serviceId/tasks": { status: 201, body: taskView() },
		});
		const onOpenChange = await renderDialog();
		await fill(user, screen.getByLabelText("Name"), "a");
		await fill(user, screen.getByLabelText("Command"), "true");
		await user.clear(screen.getByLabelText("Time zone"));
		await user.click(screen.getByRole("button", { name: "Add task" }));
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		expect(
			api.calls.find((c) => c.method === "POST" && c.path.endsWith("/tasks")),
		).toMatchObject({ body: { timezone: "UTC" } });
	});

	test("typing a cron expression moves the preset to the one it matches, or Custom", async () => {
		const user = userEvent.setup();
		stubApi(preview);
		await renderDialog();
		const cron = screen.getByLabelText("Cron expression");
		await user.clear(cron);
		await fill(user, cron, "@hourly");
		expect(pressed("Hourly")).toBeInTheDocument();
		await user.clear(cron);
		await fill(user, cron, "5 4 * * 1");
		expect(pressed("Custom")).toBeInTheDocument();
		// Choosing Custom keeps the text as it is.
		await user.click(screen.getByRole("button", { name: "Custom" }));
		expect(cron).toHaveValue("5 4 * * 1");
	});

	test("a blank cron expression blocks saving", async () => {
		const user = userEvent.setup();
		stubApi(preview);
		await renderDialog();
		await fill(user, screen.getByLabelText("Name"), "a");
		await fill(user, screen.getByLabelText("Command"), "true");
		await user.clear(screen.getByLabelText("Cron expression"));
		expect(screen.getByRole("button", { name: "Add task" })).toBeDisabled();
	});

	test("rejects a timeout that is not a whole number of seconds", async () => {
		const user = userEvent.setup();
		stubApi(preview);
		await renderDialog();
		await fill(user, screen.getByLabelText("Name"), "a");
		await fill(user, screen.getByLabelText("Command"), "true");
		const save = screen.getByRole("button", { name: "Add task" });
		for (const bad of ["0", "ten", "1.5"]) {
			await user.clear(screen.getByLabelText("Timeout (seconds)"));
			await fill(user, screen.getByLabelText("Timeout (seconds)"), bad);
			expect(save).toBeDisabled();
		}
		await user.clear(screen.getByLabelText("Timeout (seconds)"));
		await fill(user, screen.getByLabelText("Timeout (seconds)"), "1");
		expect(save).toBeEnabled();
	});

	test("an unknown time zone shows an error and blocks saving; a real one clears it", async () => {
		const user = userEvent.setup();
		stubApi(preview);
		await renderDialog();
		await fill(user, screen.getByLabelText("Name"), "a");
		await fill(user, screen.getByLabelText("Command"), "true");
		await user.clear(screen.getByLabelText("Time zone"));
		await fill(user, screen.getByLabelText("Time zone"), "Mars/Base");
		expect(
			screen.getByText(/"Mars\/Base" is not an IANA time zone/),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Time zone")).toBeInvalid();
		expect(screen.getByRole("button", { name: "Add task" })).toBeDisabled();
		await user.clear(screen.getByLabelText("Time zone"));
		await fill(user, screen.getByLabelText("Time zone"), "Asia/Tokyo");
		expect(
			screen.queryByText(/is not an IANA time zone/),
		).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Add task" })).toBeEnabled();
	});

	test("Use mine fills in the browser's time zone", async () => {
		const user = userEvent.setup();
		stubApi(preview);
		await renderDialog();
		await user.clear(screen.getByLabelText("Time zone"));
		await user.click(
			screen.getByRole("button", { name: `Use mine (${browserTimezone()})` }),
		);
		expect(screen.getByLabelText("Time zone")).toHaveValue(browserTimezone());
	});

	test("shows the server's error, stays open and can be retried", async () => {
		const user = userEvent.setup();
		let attempts = 0;
		const api = stubApi({
			...preview,
			"POST /services/:serviceId/tasks": () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{
								status: 409,
								title: "Conflict",
								detail: "A task named a already exists",
								code: "conflict",
							},
							{ status: 409 },
						)
					: Response.json(taskView());
			},
		});
		const onOpenChange = await renderDialog();
		await fill(user, screen.getByLabelText("Name"), "a");
		await fill(user, screen.getByLabelText("Command"), "true");
		await user.click(screen.getByRole("button", { name: "Add task" }));
		expect(
			await screen.findByText("A task named a already exists"),
		).toBeInTheDocument();
		expect(onOpenChange).not.toHaveBeenCalled();
		await user.click(screen.getByRole("button", { name: "Add task" }));
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		expect(
			screen.queryByText("A task named a already exists"),
		).not.toBeInTheDocument();
		expect(
			api.calls.filter((c) => c.method === "POST" && c.path.endsWith("/tasks")),
		).toHaveLength(2);
	});

	test("Cancel closes without sending anything", async () => {
		const user = userEvent.setup();
		const api = stubApi(preview);
		const onOpenChange = await renderDialog();
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(onOpenChange).toHaveBeenCalledWith(false);
		expect(
			api.calls.filter(
				(c) => c.method !== "POST" || !c.path.includes("preview"),
			),
		).toEqual([]);
	});

	test("Enter in the name field submits a valid form", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			...preview,
			"POST /services/:serviceId/tasks": { status: 201, body: taskView() },
		});
		const onOpenChange = await renderDialog();
		await fill(user, screen.getByLabelText("Command"), "true");
		await fill(user, screen.getByLabelText("Name"), "quick");
		await user.keyboard("{Enter}");
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		expect(api.calls.some((c) => c.path === "/services/svc-1/tasks")).toBe(
			true,
		);
	});

	test("Enter does nothing while the form is invalid", async () => {
		const user = userEvent.setup();
		const api = stubApi(preview);
		await renderDialog();
		await fill(user, screen.getByLabelText("Name"), "no command");
		await user.keyboard("{Enter}");
		expect(api.calls.some((c) => c.path === "/services/svc-1/tasks")).toBe(
			false,
		);
	});
});

describe("TaskDialog: edit", () => {
	const existing = taskView({
		name: "Report",
		schedule: "5 4 * * 1",
		timezone: "Europe/Berlin",
		command: ["sh", "-c", "rake report"],
		timeoutSeconds: 600,
		concurrency: "allow",
		enabled: false,
	});

	test("opens filled in from the task, with its schedule as a custom preset", async () => {
		stubApi(preview);
		await renderDialog(existing);
		expect(
			screen.getByRole("dialog", { name: "Edit task" }),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Name")).toHaveValue("Report");
		expect(screen.getByLabelText("Cron expression")).toHaveValue("5 4 * * 1");
		expect(pressed("Custom")).toBeInTheDocument();
		expect(screen.getByLabelText("Time zone")).toHaveValue("Europe/Berlin");
		expect(screen.getByLabelText("Command")).toHaveValue('sh -c "rake report"');
		expect(screen.getByLabelText("Timeout (seconds)")).toHaveValue("600");
		expect(pressed("Run anyway")).toBeInTheDocument();
		expect(screen.getByRole("switch", { name: "Paused" })).not.toBeChecked();
		expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
	});

	test("opens a task on a preset schedule with that preset selected", async () => {
		stubApi(preview);
		await renderDialog(taskView({ schedule: "@hourly" }));
		expect(pressed("Hourly")).toBeInTheDocument();
	});

	test("patches the task by id with the edited fields and reports Task saved", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			...preview,
			"PATCH /tasks/:taskId": { body: existing },
		});
		const onOpenChange = await renderDialog(existing);
		await user.clear(screen.getByLabelText("Name"));
		await fill(user, screen.getByLabelText("Name"), "Weekly report");
		await user.click(screen.getByRole("button", { name: "Save" }));
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		const patch = api.calls.find((c) => c.method === "PATCH");
		expect(patch).toMatchObject({
			path: "/tasks/task-1",
			body: {
				name: "Weekly report",
				schedule: "5 4 * * 1",
				command: ["sh", "-c", "rake report"],
				enabled: false,
				timeoutSeconds: 600,
				concurrency: "allow",
				timezone: "Europe/Berlin",
			},
		});
		expect(
			api.calls.some((c) => c.method === "POST" && c.path.endsWith("/tasks")),
		).toBe(false);
		expect(await screen.findByText("Task saved")).toBeInTheDocument();
	});

	test("shows a failed save", async () => {
		const user = userEvent.setup();
		stubApi({
			...preview,
			"PATCH /tasks/:taskId": problem(404, "task not found", "not_found"),
		});
		const onOpenChange = await renderDialog(existing);
		await user.click(screen.getByRole("button", { name: "Save" }));
		expect(await screen.findByText("task not found")).toBeInTheDocument();
		expect(onOpenChange).not.toHaveBeenCalled();
	});
});
