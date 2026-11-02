import { afterEach, describe, expect, test } from "bun:test";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { RunsDrawer } from "./runs-drawer";
import { closeRunHistory, openRunHistory } from "./store";
import {
	emptyLogs,
	problem,
	runView,
	serviceBody,
	taskView,
} from "./test-fixtures";

afterEach(() => {
	act(() => closeRunHistory());
});

const base = (runs: unknown[]) => ({
	"GET /services/:serviceId": { body: serviceBody() },
	"GET /services/:serviceId/tasks": { body: { items: [taskView()] } },
	"GET /services/:serviceId/task-runs": { body: { items: runs } },
	"GET /task-runs/:runId/logs": emptyLogs,
});

async function openDrawer(serviceId = "svc-1", taskId?: string) {
	await act(async () => openRunHistory(serviceId, taskId));
}

const list = () => screen.findByRole("list", { name: "Runs, newest first" });

describe("RunsDrawer", () => {
	test("renders nothing and asks for nothing while closed", async () => {
		const api = stubApi(base([]));
		await renderWithProviders(<RunsDrawer />);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	test("opens for a whole service: asks for its latest 50 runs, names the service", async () => {
		const api = stubApi(base([runView()]));
		await renderWithProviders(<RunsDrawer />);
		await openDrawer();
		expect(
			await screen.findByRole("dialog", { name: "Run history" }),
		).toBeInTheDocument();
		await list();
		expect(await screen.findByText(/api: scheduled runs/)).toBeInTheDocument();
		const runs = api.calls.find((c) => c.path === "/services/svc-1/task-runs");
		expect(runs?.query.get("limit")).toBe("50");
		expect(runs?.query.has("taskId")).toBe(false);
		expect(api.calls.some((c) => c.path === "/services/svc-1/tasks")).toBe(
			false,
		);
	});

	test("opens for one task: titled with its name, filtered to its runs", async () => {
		const api = stubApi(base([runView()]));
		await renderWithProviders(<RunsDrawer />);
		await openDrawer("svc-1", "task-1");
		expect(
			await screen.findByRole("dialog", { name: "Runs of nightly" }),
		).toBeInTheDocument();
		await list();
		const runs = api.calls.find((c) => c.path === "/services/svc-1/task-runs");
		expect(runs?.query.get("taskId")).toBe("task-1");
		expect(runs?.query.get("limit")).toBe("50");
	});

	test("says a task has not run yet", async () => {
		stubApi(base([]));
		await renderWithProviders(<RunsDrawer />);
		await openDrawer("svc-1", "task-1");
		expect(
			await screen.findByText(/This task has not run yet\./),
		).toBeInTheDocument();
	});

	test("says nothing has run yet for a service", async () => {
		stubApi(base([]));
		await renderWithProviders(<RunsDrawer />);
		await openDrawer();
		expect(
			await screen.findByText(/Nothing has run yet\./),
		).toBeInTheDocument();
	});

	test("offers a retry when the history did not load", async () => {
		let attempts = 0;
		stubApi({
			...base([]),
			"GET /services/:serviceId/task-runs": () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{ status: 500, title: "x", detail: "boom", code: "internal" },
							{ status: 500 },
						)
					: Response.json({ items: [runView()] });
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<RunsDrawer />);
		await openDrawer();
		expect(
			await screen.findByText("Run history did not load"),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Try again" }));
		expect(await list()).toBeInTheDocument();
		expect(
			screen.queryByText("Run history did not load"),
		).not.toBeInTheDocument();
	});

	test("lists each run with its state, source, exit code and error", async () => {
		stubApi(
			base([
				runView({
					id: "run-a",
					name: "nightly",
					status: "failed",
					exitCode: 2,
					error: "exit status 2",
				}),
				runView({
					id: "run-b",
					name: "rake db:migrate",
					source: "one_off",
					taskId: undefined,
					status: "running",
					exitCode: undefined,
					finishedAt: undefined,
				}),
				runView({
					id: "run-c",
					name: "report",
					status: "skipped",
					startedAt: undefined,
					finishedAt: undefined,
					exitCode: undefined,
					error: "previous run still active",
				}),
			]),
		);
		await renderWithProviders(<RunsDrawer />);
		await openDrawer();
		const rows = within(await list()).getAllByRole("listitem");
		expect(rows).toHaveLength(3);
		expect(
			within(rows[0] as HTMLElement).getByText("Failed"),
		).toBeInTheDocument();
		expect(
			within(rows[0] as HTMLElement).getByText("Scheduled"),
		).toBeInTheDocument();
		expect(
			within(rows[0] as HTMLElement).getByText("exit 2"),
		).toBeInTheDocument();
		expect(
			within(rows[0] as HTMLElement).getByText("exit status 2"),
		).toBeInTheDocument();
		expect(
			within(rows[1] as HTMLElement).getByText("Running"),
		).toBeInTheDocument();
		expect(
			within(rows[1] as HTMLElement).getByText("Command"),
		).toBeInTheDocument();
		expect(
			within(rows[1] as HTMLElement).getByText("exit –"),
		).toBeInTheDocument();
		expect(
			within(rows[2] as HTMLElement).getByText("Skipped"),
		).toBeInTheDocument();
		expect(
			within(rows[2] as HTMLElement).getByText(
				"Skipped: previous run still active",
			),
		).toBeInTheDocument();
		expect(
			within(rows[2] as HTMLElement).getByText(/created/),
		).toBeInTheDocument();
		expect(
			within(rows[0] as HTMLElement).getByText(/started/),
		).toBeInTheDocument();
	});

	test("selects the newest run first and switches the detail when another is chosen", async () => {
		stubApi(
			base([
				runView({ id: "run-a", name: "newest", exitCode: 0 }),
				runView({
					id: "run-b",
					name: "older",
					status: "failed",
					exitCode: 3,
					actorLabel: "ada@example.com",
					source: "manual",
				}),
			]),
		);
		const user = userEvent.setup();
		await renderWithProviders(<RunsDrawer />);
		await openDrawer();
		const rows = within(await list()).getAllByRole("button");
		expect(rows[0]).toHaveAttribute("aria-current", "true");
		expect(rows[1]).not.toHaveAttribute("aria-current");
		const detail = () => screen.getByText("Exit code").nextElementSibling;
		expect(detail()).toHaveTextContent("0");
		expect(
			screen.getByRole("log", { name: "Output of newest" }),
		).toBeInTheDocument();

		await user.click(rows[1] as HTMLElement);
		expect(rows[1]).toHaveAttribute("aria-current", "true");
		expect(rows[0]).not.toHaveAttribute("aria-current");
		expect(detail()).toHaveTextContent("3");
		expect(screen.getByText("Run now · ada@example.com")).toBeInTheDocument();
		expect(
			screen.getByRole("log", { name: "Output of older" }),
		).toBeInTheDocument();
	});

	test("detail shows source, start, duration, timeout and the command to copy", async () => {
		stubApi(
			base([
				runView({ timeoutSeconds: 600, command: ["sh", "-c", "rake cleanup"] }),
			]),
		);
		await renderWithProviders(<RunsDrawer />);
		await openDrawer();
		await list();
		expect(screen.getByText("Source").nextElementSibling).toHaveTextContent(
			"Scheduled",
		);
		expect(screen.getByText("Duration").nextElementSibling).toHaveTextContent(
			"10",
		);
		expect(screen.getByText("Timeout").nextElementSibling).toHaveTextContent(
			"600s",
		);
		expect(screen.getByText('sh -c "rake cleanup"')).toBeInTheDocument();
	});

	test("a queued run has not started and cannot be run again until it finishes", async () => {
		stubApi(
			base([
				runView({
					status: "queued",
					startedAt: undefined,
					finishedAt: undefined,
					exitCode: undefined,
				}),
			]),
		);
		await renderWithProviders(<RunsDrawer />);
		await openDrawer();
		await list();
		expect(screen.getByText("Started").nextElementSibling).toHaveTextContent(
			"not started",
		);
		expect(screen.getByText("Exit code").nextElementSibling).toHaveTextContent(
			"–",
		);
		const again = screen.getByRole("button", { name: "Run again" });
		expect(again).toBeDisabled();
		expect(again).toHaveAttribute("title", "Wait for this run to finish");
		expect(screen.getByText("Live")).toBeInTheDocument();
	});

	test("a skipped run shows why and has no output", async () => {
		stubApi(
			base([
				runView({
					status: "skipped",
					startedAt: undefined,
					finishedAt: undefined,
					exitCode: undefined,
					error: "previous run still active",
				}),
			]),
		);
		await renderWithProviders(<RunsDrawer />);
		await openDrawer();
		await list();
		expect(
			screen.getByText("A skipped run never started, so it has no output."),
		).toBeInTheDocument();
		expect(screen.queryByRole("log")).not.toBeInTheDocument();
		expect(screen.getAllByText(/Skipped: /).length).toBeGreaterThanOrEqual(2);
	});

	test("a failed run shows its error in the detail", async () => {
		stubApi(
			base([
				runView({
					status: "timed_out",
					error: "stopped after 1800s",
					exitCode: undefined,
				}),
			]),
		);
		await renderWithProviders(<RunsDrawer />);
		await openDrawer();
		await list();
		expect(
			screen.getAllByText("stopped after 1800s").length,
		).toBeGreaterThanOrEqual(2);
		expect(screen.getAllByText("Timed out").length).toBeGreaterThanOrEqual(2);
	});

	test("Run again of a task run starts the task and shows the new run", async () => {
		let listed = 0;
		const api = stubApi({
			...base([]),
			"GET /services/:serviceId/task-runs": () => {
				listed += 1;
				return Response.json({
					items:
						listed === 1
							? [runView({ id: "run-a", name: "first", exitCode: 0 })]
							: [
									runView({ id: "run-a", name: "first", exitCode: 0 }),
									runView({ id: "run-new", name: "again", exitCode: 7 }),
								],
				});
			},
			"POST /tasks/:taskId/run": {
				status: 202,
				body: runView({ id: "run-new", name: "again", exitCode: 7 }),
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<RunsDrawer />);
		await openDrawer();
		await list();
		await user.click(screen.getByRole("button", { name: "Run again" }));
		expect(
			await screen.findByText("again is running again."),
		).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "POST")?.path).toBe(
			"/tasks/task-1/run",
		);
		await waitFor(() =>
			expect(
				screen.getByText("Exit code").nextElementSibling,
			).toHaveTextContent("7"),
		);
		expect(
			screen.getByRole("log", { name: "Output of again" }),
		).toBeInTheDocument();
	});

	test("Run again of a one-off command posts the same command and timeout", async () => {
		const api = stubApi({
			...base([
				runView({
					id: "run-a",
					name: "rake db:migrate",
					source: "one_off",
					taskId: undefined,
					command: ["rake", "db:migrate"],
					timeoutSeconds: 120,
				}),
			]),
			"POST /services/:serviceId/run": {
				status: 202,
				body: runView({ id: "run-new", source: "one_off", taskId: undefined }),
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<RunsDrawer />);
		await openDrawer();
		await list();
		await user.click(screen.getByRole("button", { name: "Run again" }));
		await waitFor(() =>
			expect(api.calls.find((c) => c.method === "POST")).toMatchObject({
				path: "/services/svc-1/run",
				body: { command: ["rake", "db:migrate"], timeoutSeconds: 120 },
			}),
		);
		expect(
			await screen.findByText("nightly is running again."),
		).toBeInTheDocument();
	});

	test("a preview seed run cannot be run again", async () => {
		stubApi(base([runView({ source: "preview_seed", taskId: undefined })]));
		await renderWithProviders(<RunsDrawer />);
		await openDrawer();
		await list();
		expect(screen.getByText("Source").nextElementSibling).toHaveTextContent(
			"Preview seed",
		);
		expect(
			screen.queryByRole("button", { name: "Run again" }),
		).not.toBeInTheDocument();
	});

	test("a run that could not start is explained", async () => {
		stubApi({
			...base([runView()]),
			"POST /tasks/:taskId/run": problem(409, "x", "not_deployed"),
		});
		const user = userEvent.setup();
		await renderWithProviders(<RunsDrawer />);
		await openDrawer();
		await list();
		await user.click(screen.getByRole("button", { name: "Run again" }));
		expect(
			await screen.findByText("Could not start the run"),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"Deploy the service first: a command runs in the image of its current release.",
			),
		).toBeInTheDocument();
	});

	test("Escape closes the drawer", async () => {
		stubApi(base([runView()]));
		const user = userEvent.setup();
		await renderWithProviders(<RunsDrawer />);
		await openDrawer();
		await list();
		await user.keyboard("{Escape}");
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);
	});
});
