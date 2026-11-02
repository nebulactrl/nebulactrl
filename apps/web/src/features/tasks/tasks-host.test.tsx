import { afterEach, describe, expect, test } from "bun:test";
import { act, screen, waitFor } from "@testing-library/react";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import {
	closeRunCommand,
	closeRunHistory,
	openRunCommand,
	openRunHistory,
} from "./store";
import { TasksHost } from "./tasks-host";
import { emptyLogs, runView, serviceBody } from "./test-fixtures";

afterEach(() => {
	act(() => {
		closeRunCommand();
		closeRunHistory();
	});
});

const routes = {
	"GET /services/:serviceId": { body: serviceBody() },
	"GET /services/:serviceId/task-runs": { body: { items: [runView()] } },
	"GET /task-runs/:runId/logs": emptyLogs,
};

describe("TasksHost", () => {
	test("shows nothing until something opens it", async () => {
		const api = stubApi(routes);
		await renderWithProviders(<TasksHost />);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	test("openRunCommand shows the run-command dialog", async () => {
		stubApi(routes);
		await renderWithProviders(<TasksHost />);
		await act(async () => openRunCommand("svc-1"));
		expect(
			await screen.findByRole("dialog", { name: "Run a command in api" }),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Command")).toBeInTheDocument();
	});

	test("openRunHistory shows the run-history drawer", async () => {
		stubApi(routes);
		await renderWithProviders(<TasksHost />);
		await act(async () => openRunHistory("svc-1"));
		expect(
			await screen.findByRole("dialog", { name: "Run history" }),
		).toBeInTheDocument();
		expect(
			await screen.findByRole("list", { name: "Runs, newest first" }),
		).toBeInTheDocument();
	});

	test("the dialog and the drawer open independently", async () => {
		stubApi(routes);
		await renderWithProviders(<TasksHost />);
		await act(async () => openRunHistory("svc-1"));
		await screen.findByRole("list", { name: "Runs, newest first" });
		await act(async () => closeRunHistory());
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);
		await act(async () => openRunCommand("svc-1"));
		expect(await screen.findByLabelText("Command")).toBeInTheDocument();
		expect(
			screen.queryByRole("list", { name: "Runs, newest first" }),
		).not.toBeInTheDocument();
	});
});
