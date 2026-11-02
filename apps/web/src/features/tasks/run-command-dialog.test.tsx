import { afterEach, describe, expect, test } from "bun:test";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { RunCommandDialog } from "./run-command-dialog";
import { closeRunCommand, openRunCommand } from "./store";
import { emptyLogs, problem, runView, serviceBody } from "./test-fixtures";

afterEach(() => {
	act(() => closeRunCommand());
});

const base = {
	"GET /services/:serviceId": { body: serviceBody() },
	"GET /task-runs/:runId/logs": emptyLogs,
};

const started = runView({
	id: "run-9",
	name: "rake db:migrate",
	source: "one_off",
	taskId: undefined,
	status: "running",
	command: ["rake", "db:migrate"],
	finishedAt: undefined,
	exitCode: undefined,
});

async function openDialog(serviceId = "svc-1") {
	await act(async () => openRunCommand(serviceId));
}

describe("RunCommandDialog", () => {
	test("renders nothing until a command is requested", async () => {
		const api = stubApi(base);
		await renderWithProviders(<RunCommandDialog />);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	test("opens for a service and names it once it has loaded", async () => {
		const api = stubApi(base);
		await renderWithProviders(<RunCommandDialog />);
		await openDialog();
		expect(
			await screen.findByRole("dialog", { name: "Run a command in api" }),
		).toBeInTheDocument();
		expect(api.calls.map((c) => c.path)).toContain("/services/svc-1");
		expect(screen.getByLabelText("Command")).toHaveValue("");
		expect(screen.getByLabelText("Timeout (seconds)")).toHaveValue("1800");
	});

	test("keeps a generic title while the service has not loaded", async () => {
		stubApi({
			...base,
			"GET /services/:serviceId": problem(404, "no such service", "not_found"),
		});
		await renderWithProviders(<RunCommandDialog />);
		await openDialog();
		expect(
			await screen.findByRole("dialog", { name: "Run a command" }),
		).toBeInTheDocument();
	});

	test("cannot run an empty command or a timeout that is not a whole number of seconds", async () => {
		const user = userEvent.setup();
		stubApi(base);
		await renderWithProviders(<RunCommandDialog />);
		await openDialog();
		const run = await screen.findByRole("button", { name: "Run command" });
		expect(run).toBeDisabled();
		await user.type(screen.getByLabelText("Command"), "ls");
		expect(run).toBeEnabled();
		for (const bad of ["0", "abc", "1.5", "-5", ""]) {
			await user.clear(screen.getByLabelText("Timeout (seconds)"));
			if (bad) await user.type(screen.getByLabelText("Timeout (seconds)"), bad);
			expect(run).toBeDisabled();
		}
		await user.clear(screen.getByLabelText("Timeout (seconds)"));
		await user.type(screen.getByLabelText("Timeout (seconds)"), "30");
		expect(run).toBeEnabled();
	});

	test("posts the command as an argv with the timeout and shows the live run", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			...base,
			"POST /services/:serviceId/run": { status: 202, body: started },
			"GET /task-runs/:runId": {
				body: {
					...started,
					status: "succeeded",
					exitCode: 0,
					finishedAt: "2026-10-27T03:00:11Z",
				},
			},
		});
		await renderWithProviders(<RunCommandDialog />);
		await openDialog();
		await user.type(
			await screen.findByLabelText("Command"),
			'sh -c "echo hi there"',
		);
		await user.clear(screen.getByLabelText("Timeout (seconds)"));
		await user.type(screen.getByLabelText("Timeout (seconds)"), "90");
		await user.click(screen.getByRole("button", { name: "Run command" }));

		expect(await screen.findByText("Run another")).toBeInTheDocument();
		const post = api.calls.find((c) => c.method === "POST");
		expect(post).toMatchObject({
			path: "/services/svc-1/run",
			body: { command: ["sh", "-c", "echo hi there"], timeoutSeconds: 90 },
		});
		// The started run shows its command, then follows it to its end.
		expect(screen.getByText("rake db:migrate")).toBeInTheDocument();
		expect(await screen.findByText("Succeeded")).toBeInTheDocument();
		expect(screen.getByText("exit 0")).toBeInTheDocument();
		expect(screen.queryByLabelText("Command")).not.toBeInTheDocument();
		expect(api.calls.some((c) => c.path === "/task-runs/run-9/logs")).toBe(
			true,
		);
		expect(api.unmatched).toEqual([]);
	});

	test("shows an active run as running with a pulsing status", async () => {
		const user = userEvent.setup();
		stubApi({
			...base,
			"POST /services/:serviceId/run": { status: 202, body: started },
			"GET /task-runs/:runId": { body: started },
		});
		await renderWithProviders(<RunCommandDialog />);
		await openDialog();
		await user.type(await screen.findByLabelText("Command"), "rake db:migrate");
		await user.click(screen.getByRole("button", { name: "Run command" }));
		expect(await screen.findByText("running…")).toBeInTheDocument();
		expect(screen.getByText("Running")).toBeInTheDocument();
		expect(screen.getByText("Live")).toBeInTheDocument();
	});

	test("shows the failure reason and says when a finished run has no exit code", async () => {
		const user = userEvent.setup();
		const timedOut = {
			...started,
			status: "timed_out",
			error: "stopped after 90s",
			finishedAt: "2026-10-27T03:01:31Z",
		};
		stubApi({
			...base,
			"POST /services/:serviceId/run": { status: 202, body: timedOut },
			"GET /task-runs/:runId": { body: timedOut },
		});
		await renderWithProviders(<RunCommandDialog />);
		await openDialog();
		await user.type(await screen.findByLabelText("Command"), "sleep 999");
		await user.click(screen.getByRole("button", { name: "Run command" }));
		expect(await screen.findByText("Timed out")).toBeInTheDocument();
		expect(screen.getByText("stopped after 90s")).toBeInTheDocument();
		expect(screen.getByText("no exit code")).toBeInTheDocument();
	});

	test("explains a service that is not deployed and keeps the form", async () => {
		const user = userEvent.setup();
		stubApi({
			...base,
			"POST /services/:serviceId/run": problem(
				409,
				"no deployment",
				"not_deployed",
			),
		});
		await renderWithProviders(<RunCommandDialog />);
		await openDialog();
		await user.type(await screen.findByLabelText("Command"), "ls");
		await user.click(screen.getByRole("button", { name: "Run command" }));
		expect(
			await screen.findByText(
				"Deploy the service first: a command runs in the image of its current release.",
			),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Command")).toHaveValue("ls");
		expect(screen.getByRole("button", { name: "Run command" })).toBeEnabled();
	});

	test("explains an offline server", async () => {
		const user = userEvent.setup();
		stubApi({
			...base,
			"POST /services/:serviceId/run": problem(
				409,
				"agent offline",
				"server_offline",
			),
		});
		await renderWithProviders(<RunCommandDialog />);
		await openDialog();
		await user.type(await screen.findByLabelText("Command"), "ls");
		await user.click(screen.getByRole("button", { name: "Run command" }));
		expect(
			await screen.findByText(
				"The service's server is offline, so nothing can run on it right now.",
			),
		).toBeInTheDocument();
	});

	test("clears an old error when it is submitted again", async () => {
		const user = userEvent.setup();
		let attempts = 0;
		stubApi({
			...base,
			"POST /services/:serviceId/run": () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{
								status: 400,
								title: "x",
								detail: "bad command",
								code: "bad_request",
							},
							{ status: 400 },
						)
					: Response.json(started);
			},
			"GET /task-runs/:runId": { body: started },
		});
		await renderWithProviders(<RunCommandDialog />);
		await openDialog();
		await user.type(await screen.findByLabelText("Command"), "ls");
		await user.click(screen.getByRole("button", { name: "Run command" }));
		expect(await screen.findByText("bad command")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Run command" }));
		expect(await screen.findByText("Run another")).toBeInTheDocument();
		expect(screen.queryByText("bad command")).not.toBeInTheDocument();
	});

	test("Run another returns to the form with the last command still there", async () => {
		const user = userEvent.setup();
		stubApi({
			...base,
			"POST /services/:serviceId/run": { status: 202, body: started },
			"GET /task-runs/:runId": { body: started },
		});
		await renderWithProviders(<RunCommandDialog />);
		await openDialog();
		await user.type(await screen.findByLabelText("Command"), "rake db:migrate");
		await user.click(screen.getByRole("button", { name: "Run command" }));
		await user.click(
			await screen.findByRole("button", { name: "Run another" }),
		);
		expect(screen.getByLabelText("Command")).toHaveValue("rake db:migrate");
		expect(screen.getByRole("button", { name: "Run command" })).toBeEnabled();
		expect(screen.queryByText("running…")).not.toBeInTheDocument();
	});

	test("Close and Cancel both close the dialog", async () => {
		const user = userEvent.setup();
		stubApi({
			...base,
			"POST /services/:serviceId/run": { status: 202, body: started },
			"GET /task-runs/:runId": { body: started },
		});
		await renderWithProviders(<RunCommandDialog />);
		await openDialog();
		await user.click(await screen.findByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);

		await openDialog();
		await user.type(await screen.findByLabelText("Command"), "ls");
		await user.click(screen.getByRole("button", { name: "Run command" }));
		const dialog = await screen.findByRole("dialog");
		await user.click(
			await within(dialog).findByRole("button", { name: "Close" }),
		);
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);
	});

	test("a reopened dialog starts blank again", async () => {
		const user = userEvent.setup();
		stubApi(base);
		await renderWithProviders(<RunCommandDialog />);
		await openDialog();
		await user.type(await screen.findByLabelText("Command"), "ls -la");
		await user.clear(screen.getByLabelText("Timeout (seconds)"));
		await user.type(screen.getByLabelText("Timeout (seconds)"), "5");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);
		await openDialog("svc-2");
		expect(await screen.findByLabelText("Command")).toHaveValue("");
		expect(screen.getByLabelText("Timeout (seconds)")).toHaveValue("1800");
	});
});
