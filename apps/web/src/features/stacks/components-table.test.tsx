import { afterEach, describe, expect, spyOn, test } from "bun:test";
import { useLocation } from "@tanstack/react-router";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Service } from "#/api/models";
import {
	closeFiles,
	closeShell,
	useFilesReplica,
	useFilesTarget,
	useShellReplica,
	useShellTarget,
} from "#/features/terminal/store";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { ComponentsTable } from "./components-table";
import { COMPONENTS, makeObserved, makeStack, replica } from "./test-fixtures";

function Probe() {
	const location = useLocation();
	return (
		<div>
			<output aria-label="search">{JSON.stringify(location.search)}</output>
			<output aria-label="shell">
				{String(useShellTarget())}/{String(useShellReplica())}
			</output>
			<output aria-label="files">
				{String(useFilesTarget())}/{String(useFilesReplica())}
			</output>
		</div>
	);
}

/** The item at `index`; fails the test with a clear message when it is missing. */
function at<T>(items: readonly T[], index: number): T {
	const item = items[index];
	if (item === undefined) throw new Error(`expected an item at index ${index}`);
	return item;
}

afterEach(() => {
	act(() => {
		closeShell();
		closeFiles();
	});
});

/** The metrics of a service; without `perReplica` its samples carry no per-replica figures. */
function metrics(
	replicas: { index: number; cpuPercent: number; memBytes: number }[],
	perReplica = true,
) {
	return make("MetricsOutputBody", {
		points: [
			{
				ts: "2026-01-01T00:00:00Z",
				cpuPercent: 1,
				cpuMax: 1,
				cpuPerReplica: 1,
				memBytes: 1,
				memMax: 1,
				memLimitBytes: 0,
				replicaCount: replicas.length,
				...(perReplica
					? { replicas: [{ index: 99, cpuPercent: 50, memBytes: 1 }] }
					: {}),
			},
			{
				ts: "2026-01-01T00:00:05Z",
				cpuPercent: 1,
				cpuMax: 1,
				cpuPerReplica: 1,
				memBytes: 1,
				memMax: 1,
				memLimitBytes: 0,
				replicaCount: replicas.length,
				...(perReplica ? { replicas } : {}),
			},
		],
		stepSeconds: 5,
		retentionDays: 7,
	});
}

async function renderTable(service: Service, path = "/x") {
	return renderWithProviders(
		<>
			<ComponentsTable service={service} />
			<Probe />
		</>,
		{ path },
	);
}

function row(name: string): HTMLElement {
	const found = screen
		.getAllByRole("row")
		.find((candidate) => candidate.getAttribute("data-component") === name);
	if (!found) throw new Error(`no row for ${name}`);
	return found;
}

describe("ComponentsTable", () => {
	test("explains the empty state while the stack has no components", async () => {
		const api = stubApi({});
		await renderTable(makeStack({ components: [] }));
		expect(
			screen.getByText(
				/No components yet\. The Compose file's services appear/,
			),
		).toBeInTheDocument();
		expect(screen.queryByRole("table")).not.toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});

	test("a service that is not a stack has no component rows", async () => {
		stubApi({});
		await renderTable(makeStack({ kind: "app" }));
		expect(screen.queryByRole("table")).not.toBeInTheDocument();
	});

	test("one row per component with state in words, image, restarts and no usage before a deployment", async () => {
		const api = stubApi({});
		await renderTable(
			makeStack({
				observed: makeObserved({
					replicas: [
						replica(0, "db"),
						replica(1, "web", {
							state: "restarting",
							health: "none",
							restartCount: 1,
						}),
					],
				}),
			}),
		);
		expect(
			screen.getByRole("table", { name: "Components" }),
		).toBeInTheDocument();
		const db = row("db");
		expect(db).toHaveTextContent("Healthy");
		expect(db).toHaveTextContent("postgres:16");
		expect(db).toHaveTextContent("0 restarts");
		const web = row("web");
		expect(web).toHaveTextContent("Restarting");
		expect(web).toHaveTextContent("1 restart");
		expect(web).not.toHaveTextContent("1 restarts");
		expect(row("worker")).toHaveTextContent("Not started");
		// Without a current deployment the metrics are not even requested.
		expect(db).toHaveTextContent("—");
		expect(api.calls).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});

	test("shows the digest the release runs next to the image", async () => {
		stubApi({});
		await renderTable(
			makeStack({
				currentRelease: {
					id: "rel-1",
					imageRef: "",
					source: {
						type: "compose",
						components: {
							db: `postgres@sha256:${"ab12cd34ef56".padEnd(64, "0")}`,
						},
					},
				},
			}),
		);
		expect(row("db")).toHaveTextContent("postgres:16 · sha256:ab12cd34ef56");
		expect(row("web")).not.toHaveTextContent("sha256");
	});

	test("CPU and memory come from the newest metrics sample, per component index", async () => {
		const api = stubApi({
			"GET /services/:serviceId/metrics": {
				body: metrics([
					{ index: 0, cpuPercent: 12.34, memBytes: 212 * 1024 * 1024 },
					{ index: 1, cpuPercent: 0, memBytes: 2 * 1024 ** 3 },
				]),
			},
		});
		await renderTable(
			makeStack({
				currentDeploymentId: "dep-1",
				observed: makeObserved({
					replicas: [replica(0, "db"), replica(1, "web")],
				}),
			}),
		);
		await waitFor(() => expect(row("db")).toHaveTextContent("12.3%"));
		expect(row("db")).toHaveTextContent("212 MiB");
		expect(row("web")).toHaveTextContent("0.0%");
		expect(row("web")).toHaveTextContent("2 GiB");
		// Not reported by the agent: no sample is invented for it.
		expect(row("worker")).toHaveTextContent("—");
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]?.query.get("range")).toBe("1h");
	});

	test("shows an em dash when the newest sample has no per-replica figures", async () => {
		stubApi({
			"GET /services/:serviceId/metrics": { body: metrics([], false) },
		});
		await renderTable(
			makeStack({
				currentDeploymentId: "dep-1",
				observed: makeObserved({ replicas: [replica(0, "db")] }),
			}),
		);
		await waitFor(() => expect(row("db")).toBeInTheDocument());
		expect(row("db")).toHaveTextContent("—");
	});

	test("restarts are told apart by words only when there are some", async () => {
		stubApi({});
		await renderTable(
			makeStack({
				observed: makeObserved({
					replicas: [replica(0, "db", { restartCount: 4 })],
				}),
			}),
		);
		expect(row("db")).toHaveTextContent("4 restarts");
	});

	test("the Logs shortcut opens the Logs tab filtered to the component", async () => {
		const user = userEvent.setup();
		stubApi({});
		await renderTable(makeStack(), "/x?service=stack-1&tab=overview");
		await user.click(
			within(row("web")).getByRole("button", { name: "Logs of web" }),
		);
		await waitFor(() =>
			expect(
				JSON.parse(screen.getByLabelText("search").textContent ?? ""),
			).toEqual({ service: "stack-1", tab: "logs", component: "web" }),
		);
	});

	test("the shell and files shortcuts attach to the component's replica", async () => {
		const user = userEvent.setup();
		stubApi({});
		await renderTable(
			makeStack({
				observed: makeObserved({
					replicas: [replica(0, "db"), replica(1, "web")],
				}),
			}),
		);
		await user.click(
			within(row("web")).getByRole("button", { name: "Open a shell in web" }),
		);
		expect(screen.getByLabelText("shell")).toHaveTextContent("stack-1/1");
		expect(screen.getByLabelText("files")).toHaveTextContent("null/null");
		await user.click(
			within(row("db")).getByRole("button", { name: "Browse files of db" }),
		);
		expect(screen.getByLabelText("files")).toHaveTextContent("stack-1/0");
	});

	test("a component that is not running cannot be shelled into or browsed", async () => {
		stubApi({});
		await renderTable(
			makeStack({
				observed: makeObserved({
					replicas: [replica(0, "db", { state: "exited", health: "none" })],
				}),
			}),
		);
		const db = within(row("db"));
		expect(
			db.getByRole("button", { name: "Open a shell in db" }),
		).toBeDisabled();
		expect(
			db.getByRole("button", { name: "Browse files of db" }),
		).toBeDisabled();
		expect(
			db.getByRole("button", { name: "Open a shell in db" }),
		).toHaveAttribute("title", "db is not running");
		// Logs stay available for a stopped component.
		expect(db.getByRole("button", { name: "Logs of db" })).toBeEnabled();
	});

	test("a running component's shortcuts are enabled", async () => {
		stubApi({});
		await renderTable(
			makeStack({
				observed: makeObserved({ replicas: [replica(0, "db")] }),
			}),
		);
		expect(
			within(row("db")).getByRole("button", { name: "Open a shell in db" }),
		).toBeEnabled();
	});

	test("the component named by ?component= is selected and scrolled into view", async () => {
		const scroll = spyOn(
			Element.prototype,
			"scrollIntoView",
		).mockImplementation(() => {});
		try {
			stubApi({});
			await renderTable(makeStack(), "/x?component=web");
			expect(row("web")).toHaveAttribute("aria-selected", "true");
			expect(row("db")).not.toHaveAttribute("aria-selected");
			expect(scroll).toHaveBeenCalledTimes(1);
			expect(scroll.mock.contexts[0]).toBe(row("web"));
		} finally {
			scroll.mockRestore();
		}
	});

	test("without ?component= nothing is selected or scrolled", async () => {
		const scroll = spyOn(
			Element.prototype,
			"scrollIntoView",
		).mockImplementation(() => {});
		try {
			stubApi({});
			await renderTable(makeStack(), "/x");
			expect(
				screen
					.getAllByRole("row")
					.filter((r) => r.hasAttribute("aria-selected")),
			).toHaveLength(0);
			expect(scroll).not.toHaveBeenCalled();
		} finally {
			scroll.mockRestore();
		}
	});

	test("lists a reported container whose component the file no longer declares", async () => {
		stubApi({});
		await renderTable(
			makeStack({
				components: [at(COMPONENTS, 0)],
				observed: makeObserved({
					replicas: [replica(0, "db"), replica(5, "old-cache")],
				}),
			}),
		);
		expect(row("old-cache")).toHaveTextContent("Healthy");
	});
});
