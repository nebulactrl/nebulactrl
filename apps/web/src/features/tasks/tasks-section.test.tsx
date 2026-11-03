import { afterEach, describe, expect, test } from "bun:test";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import {
	closeRunCommand,
	closeRunHistory,
	useRunCommandTarget,
	useRunHistoryTarget,
} from "./store";
import { TasksSection } from "./tasks-section";
import { problem, runView, service, taskView } from "./test-fixtures";

afterEach(() => {
	act(() => {
		closeRunCommand();
		closeRunHistory();
	});
});

const future = new Date(Date.now() + 3 * 3600_000).toISOString();

const routes = (items: unknown[]) => ({
	"GET /services/:serviceId/tasks": { body: { items } },
	"GET /services/:serviceId/config-source": problem(404, "none", "not_found"),
	"POST /task-schedules/preview": {
		body: { valid: true, description: "Every day", nextRuns: [] },
	},
});

/** Shows what the section asked the page to open. */
function Targets() {
	const command = useRunCommandTarget();
	const history = useRunHistoryTarget();
	return (
		<>
			<output data-testid="command-target">{command ?? "none"}</output>
			<output data-testid="history-target">
				{history ? `${history.serviceId}/${history.taskId ?? "all"}` : "none"}
			</output>
		</>
	);
}

async function renderSection(deployed = true) {
	return renderWithProviders(
		<>
			<TasksSection
				service={service({ currentDeploymentId: deployed ? "dep-1" : null })}
			/>
			<Targets />
		</>,
	);
}

const row = (name: string) => {
	const cell = screen.getByText(name, { selector: "span" });
	const tr = cell.closest("tr");
	if (!tr) throw new Error(`no row for ${name}`);
	return within(tr);
};

describe("TasksSection list", () => {
	test("lists each task with its command, schedule in words, state and last run", async () => {
		stubApi(
			routes([
				taskView({
					nextRunAt: future,
					lastRunAt: "2026-10-27T03:00:00Z",
					lastRun: runView({ status: "failed", exitCode: 1 }),
				}),
			]),
		);
		await renderSection();
		const nightly = row(
			await screen.findByText("nightly").then((e) => e.textContent ?? ""),
		);
		expect(nightly.getByText("rake cleanup")).toBeInTheDocument();
		expect(nightly.getByText("0 3 * * *")).toBeInTheDocument();
		expect(nightly.getByText("Every day at 03:00, UTC")).toBeInTheDocument();
		expect(
			nightly.getByRole("switch", { name: "Pause nightly" }),
		).toBeChecked();
		expect(nightly.getByText("On")).toBeInTheDocument();
		expect(
			nightly.getByRole("button", {
				name: "Failed: open the run history of nightly",
			}),
		).toBeInTheDocument();
		expect(nightly.getByRole("button", { name: "Run now" })).toBeEnabled();
	});

	test("a restart task shows Restart where a command task shows its command", async () => {
		stubApi(
			routes([
				taskView({
					name: "weekly restart",
					action: "restart",
					command: [],
					timeoutSeconds: 300,
					nextRunAt: future,
				}),
			]),
		);
		await renderSection();
		const restart = row(
			await screen
				.findByText("weekly restart")
				.then((e) => e.textContent ?? ""),
		);
		expect(restart.getByText("Restart")).toBeInTheDocument();
		expect(restart.getByRole("button", { name: "Run now" })).toBeEnabled();
	});

	test("shows a paused task as paused with no next run, and a task that never ran as such", async () => {
		stubApi(routes([taskView({ enabled: false, nextRunAt: future })]));
		await renderSection();
		const nightly = row(
			await screen.findByText("nightly").then((e) => e.textContent ?? ""),
		);
		expect(
			nightly.getByRole("switch", { name: "Resume nightly" }),
		).not.toBeChecked();
		expect(nightly.getByText("Never run")).toBeInTheDocument();
		// "Paused" appears as the switch label and as the next-run cell.
		expect(nightly.getAllByText("Paused")).toHaveLength(2);
	});

	test("shows dashes for an enabled task without a next run and for a task without a command", async () => {
		stubApi(routes([taskView({ command: null })]));
		await renderSection();
		await screen.findByText("nightly");
		// One dash for the empty command, one for the missing next run.
		expect(screen.getByText("–", { selector: "code" })).toBeInTheDocument();
		expect(screen.getByText("–", { selector: "span" })).toBeInTheDocument();
	});

	test("explains an empty list", async () => {
		stubApi(routes([]));
		await renderSection();
		expect(await screen.findByText(/No tasks yet\./)).toBeInTheDocument();
		expect(screen.queryByText("not deployed")).not.toBeInTheDocument();
		expect(screen.queryByRole("table")).not.toBeInTheDocument();
	});

	test("warns on an empty list that the service is not deployed, and blocks running", async () => {
		stubApi(routes([]));
		await renderSection(false);
		expect(await screen.findByText("not deployed")).toBeInTheDocument();
		expect(screen.getByText(/deploy the service first\./)).toBeInTheDocument();
		expect(screen.getByRole("button", { name: /Run command/ })).toBeDisabled();
	});

	test("disables Run now of a task while the service is not deployed", async () => {
		stubApi(routes([taskView()]));
		await renderSection(false);
		await screen.findByText("nightly");
		expect(screen.getByRole("button", { name: "Run now" })).toBeDisabled();
		expect(screen.getByRole("button", { name: "Run now" })).toHaveAttribute(
			"title",
			"Deploy the service first",
		);
	});

	test("offers a retry when the tasks did not load and shows them after it", async () => {
		let attempts = 0;
		stubApi({
			...routes([]),
			"GET /services/:serviceId/tasks": () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{ status: 500, title: "x", detail: "boom", code: "internal" },
							{ status: 500 },
						)
					: Response.json({ items: [taskView()] });
			},
		});
		const user = userEvent.setup();
		await renderSection();
		expect(await screen.findByText("Tasks did not load")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Try again" }));
		expect(await screen.findByText("nightly")).toBeInTheDocument();
		expect(screen.queryByText("Tasks did not load")).not.toBeInTheDocument();
	});
});

describe("TasksSection actions", () => {
	test("Run command… and Run history open their screens for this service", async () => {
		stubApi(routes([]));
		const user = userEvent.setup();
		await renderSection();
		await screen.findByText(/No tasks yet\./);
		await user.click(screen.getByRole("button", { name: /Run command/ }));
		expect(screen.getByTestId("command-target")).toHaveTextContent("svc-1");
		await user.click(screen.getByRole("button", { name: /Run history/ }));
		expect(screen.getByTestId("history-target")).toHaveTextContent("svc-1/all");
	});

	test("the last-run pill opens the history of that task", async () => {
		stubApi(routes([taskView({ lastRun: runView() })]));
		const user = userEvent.setup();
		await renderSection();
		await user.click(
			await screen.findByRole("button", {
				name: "Succeeded: open the run history of nightly",
			}),
		);
		expect(screen.getByTestId("history-target")).toHaveTextContent(
			"svc-1/task-1",
		);
	});

	test("Add task opens a blank dialog", async () => {
		stubApi(routes([taskView({ name: "Report", schedule: "@hourly" })]));
		const user = userEvent.setup();
		await renderSection();
		await screen.findByText("Report");
		await user.click(screen.getByRole("button", { name: "Add task" }));
		expect(
			await screen.findByRole("dialog", { name: "Add a task" }),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Name")).toHaveValue("");
		expect(screen.getByLabelText("Command")).toHaveValue("");
	});

	test("Edit opens the dialog filled from the row, and a later Add starts blank again", async () => {
		stubApi(routes([taskView({ name: "Report", schedule: "@hourly" })]));
		const user = userEvent.setup();
		await renderSection();
		await screen.findByText("Report");
		await user.click(screen.getByRole("button", { name: "Edit" }));
		expect(
			await screen.findByRole("dialog", { name: "Edit task" }),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Name")).toHaveValue("Report");
		expect(screen.getByLabelText("Cron expression")).toHaveValue("@hourly");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);

		await user.click(screen.getByRole("button", { name: "Add task" }));
		expect(
			await screen.findByRole("dialog", { name: "Add a task" }),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Name")).toHaveValue("");
	});

	test("pausing a task patches enabled=false and flips the switch before the answer arrives", async () => {
		const release = Promise.withResolvers<void>();
		let enabled = true;
		const api = stubApi({
			...routes([]),
			"GET /services/:serviceId/tasks": () =>
				Response.json({ items: [taskView({ enabled })] }),
			"PATCH /tasks/:taskId": async () => {
				await release.promise;
				enabled = false;
				return Response.json(taskView({ enabled: false }));
			},
		});
		const user = userEvent.setup();
		await renderSection();
		await user.click(
			await screen.findByRole("switch", { name: "Pause nightly" }),
		);
		expect(
			await screen.findByRole("switch", { name: "Resume nightly" }),
		).not.toBeChecked();
		expect(api.calls.find((c) => c.method === "PATCH")).toMatchObject({
			path: "/tasks/task-1",
			body: { enabled: false },
		});
		release.resolve();
		await waitFor(() =>
			expect(
				screen.getByRole("switch", { name: "Resume nightly" }),
			).not.toBeChecked(),
		);
		await waitFor(() =>
			expect(
				api.calls.filter(
					(c) => c.method === "GET" && c.path.endsWith("/tasks"),
				),
			).toHaveLength(2),
		);
	});

	test("resuming a paused task patches enabled=true", async () => {
		const api = stubApi({
			...routes([taskView({ enabled: false })]),
			"PATCH /tasks/:taskId": { body: taskView() },
		});
		const user = userEvent.setup();
		await renderSection();
		await user.click(
			await screen.findByRole("switch", { name: "Resume nightly" }),
		);
		await waitFor(() =>
			expect(api.calls.find((c) => c.method === "PATCH")?.body).toEqual({
				enabled: true,
			}),
		);
	});

	test("a failed pause puts the switch back and says why", async () => {
		stubApi({
			...routes([taskView()]),
			"PATCH /tasks/:taskId": problem(500, "scheduler unavailable", "internal"),
		});
		const user = userEvent.setup();
		await renderSection();
		await user.click(
			await screen.findByRole("switch", { name: "Pause nightly" }),
		);
		expect(
			await screen.findByText("Could not change the task"),
		).toBeInTheDocument();
		expect(screen.getByText("scheduler unavailable")).toBeInTheDocument();
		expect(screen.getByRole("switch", { name: "Pause nightly" })).toBeChecked();
	});

	test("Run now starts the task, says so and opens its run history", async () => {
		const api = stubApi({
			...routes([taskView()]),
			"POST /tasks/:taskId/run": {
				status: 202,
				body: runView({ status: "queued" }),
			},
		});
		const user = userEvent.setup();
		await renderSection();
		await user.click(await screen.findByRole("button", { name: "Run now" }));
		expect(await screen.findByText("Started nightly")).toBeInTheDocument();
		expect(
			screen.getByText("Its output is in the run history."),
		).toBeInTheDocument();
		expect(
			api.calls.find((c) => c.method === "POST" && c.path.endsWith("/run"))
				?.path,
		).toBe("/tasks/task-1/run");
		expect(screen.getByTestId("history-target")).toHaveTextContent(
			"svc-1/task-1",
		);
	});

	test("Run now explains why a task could not start", async () => {
		stubApi({
			...routes([taskView()]),
			"POST /tasks/:taskId/run": problem(409, "x", "server_offline"),
		});
		const user = userEvent.setup();
		await renderSection();
		await user.click(await screen.findByRole("button", { name: "Run now" }));
		expect(
			await screen.findByText("Could not start the task"),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"The service's server is offline, so nothing can run on it right now.",
			),
		).toBeInTheDocument();
		expect(screen.getByTestId("history-target")).toHaveTextContent("none");
	});

	test("Delete asks first, then deletes the task and says its runs stay", async () => {
		const api = stubApi({
			...routes([taskView()]),
			"DELETE /tasks/:taskId": { status: 204 },
		});
		const user = userEvent.setup();
		await renderSection();
		await user.click(await screen.findByRole("button", { name: "Delete" }));
		const confirm = await screen.findByRole("alertdialog");
		expect(within(confirm).getByText("Delete nightly?")).toBeInTheDocument();
		expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
		await user.click(
			within(confirm).getByRole("button", { name: "Delete task" }),
		);
		expect(await screen.findByText("Task deleted")).toBeInTheDocument();
		expect(
			screen.getByText("Its past runs stay in the run history."),
		).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "DELETE")?.path).toBe(
			"/tasks/task-1",
		);
	});

	test("cancelling the delete confirmation leaves the task alone", async () => {
		const api = stubApi(routes([taskView()]));
		const user = userEvent.setup();
		await renderSection();
		await user.click(await screen.findByRole("button", { name: "Delete" }));
		const confirm = await screen.findByRole("alertdialog");
		await user.click(within(confirm).getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
		);
		expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
		expect(screen.getByText("nightly")).toBeInTheDocument();
	});

	test("a failed delete says why", async () => {
		stubApi({
			...routes([taskView()]),
			"DELETE /tasks/:taskId": problem(500, "database is locked", "internal"),
		});
		const user = userEvent.setup();
		await renderSection();
		await user.click(await screen.findByRole("button", { name: "Delete" }));
		const confirm = await screen.findByRole("alertdialog");
		await user.click(
			within(confirm).getByRole("button", { name: "Delete task" }),
		);
		expect(
			await screen.findByText("Could not delete the task"),
		).toBeInTheDocument();
		expect(
			(await screen.findAllByText("database is locked")).length,
		).toBeGreaterThan(0);
	});
});
