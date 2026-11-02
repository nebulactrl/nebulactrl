import { describe, expect, test } from "bun:test";
import { useSearch } from "@tanstack/react-router";
import {
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { EventView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { DeploymentDetail, EventMark } from "./deployment-detail";
import {
	deployment,
	eventView,
	logStreamResponse,
	openLogStreamResponse,
} from "./test-fixtures";

/** Shows the `debug` search param, which the Debug button writes into the address. */
function DebugParam() {
	const search = useSearch({ strict: false }) as { debug?: string };
	return <output aria-label="debug param">{search.debug ?? ""}</output>;
}

/** The API a detail view reads for deployment `id`: its events, log stream and the servers. */
function routes(events: EventView[], id: string) {
	return {
		[`GET /deployments/${id}/events`]: { body: { items: events } },
		[`GET /deployments/${id}/logs`]: () => logStreamResponse([]),
		"GET /servers": { body: make("PageServer", { items: [] }) },
	};
}

describe("EventMark", () => {
	test("each state draws its own glyph", () => {
		const { container, rerender } = render(<EventMark status="succeeded" />);
		const svgClass = () =>
			container.querySelector("svg")?.getAttribute("class") ?? "";
		expect(svgClass()).toContain("text-status-live-on");
		rerender(<EventMark status="failed" />);
		expect(svgClass()).toContain("text-status-failed-on");
		rerender(<EventMark status="warning" />);
		expect(svgClass()).toContain("text-status-degraded-on");
		rerender(<EventMark status="info" />);
		expect(svgClass()).toContain("text-ink-4");
	});

	test("a started step spins", () => {
		render(<EventMark status="started" />);
		expect(
			screen.getByRole("presentation", { hidden: true }),
		).toBeInTheDocument();
	});
});

describe("DeploymentDetail steps", () => {
	test("folds each step's start and result into one row with its duration and message", async () => {
		stubApi(
			routes(
				[
					eventView({
						id: 1,
						step: "status",
						status: "queued",
						createdAt: "2026-10-31T10:00:00Z",
					}),
					eventView({
						id: 2,
						step: "pull",
						status: "started",
						createdAt: "2026-10-31T10:00:00Z",
					}),
					eventView({
						id: 3,
						step: "pull",
						status: "succeeded",
						message: "pulled image",
						createdAt: "2026-10-31T10:00:02Z",
					}),
					eventView({
						id: 4,
						step: "predeploy",
						status: "started",
						createdAt: "2026-10-31T10:00:03Z",
					}),
					eventView({
						id: 5,
						step: "predeploy",
						status: "failed",
						message: "migration exited 1",
						createdAt: "2026-10-31T10:01:05Z",
					}),
					eventView({
						id: 6,
						step: "status",
						status: "rolled_back",
						message: "restored previous",
						createdAt: "2026-10-31T10:01:06Z",
					}),
				],
				"log-fold",
			),
		);
		await renderWithProviders(
			<DeploymentDetail deployment={deployment({ id: "log-fold" })} />,
		);

		const steps = await screen.findByRole("list", { name: "Deployment steps" });
		const rows = within(steps).getAllByRole("listitem");
		// queued is a phase the rows already say: dropped. pull, predeploy, outcome.
		expect(rows).toHaveLength(3);
		expect(
			within(rows[0] as HTMLElement).getByText("pull"),
		).toBeInTheDocument();
		expect(
			within(rows[0] as HTMLElement).getByText("2.0 s"),
		).toBeInTheDocument();
		expect(
			within(rows[0] as HTMLElement).getByText("pulled image"),
		).toBeInTheDocument();
		expect(
			within(rows[1] as HTMLElement).getByText("Pre-deploy"),
		).toBeInTheDocument();
		expect(
			within(rows[1] as HTMLElement).getByText("1 min 2 s"),
		).toBeInTheDocument();
		expect(
			within(rows[1] as HTMLElement).getByText("migration exited 1"),
		).toBeInTheDocument();
		expect(
			within(rows[2] as HTMLElement).getByText("rolled back"),
		).toBeInTheDocument();
		expect(
			within(rows[2] as HTMLElement).getByText("restored previous"),
		).toBeInTheDocument();
	});

	test("a step that has started but not finished reads 'running' and has no duration", async () => {
		stubApi(
			routes(
				[
					eventView({
						id: 1,
						step: "build",
						status: "started",
						createdAt: "2026-10-31T10:00:00Z",
					}),
				],
				"log-run-step",
			),
		);
		await renderWithProviders(
			<DeploymentDetail
				deployment={deployment({ id: "log-run-step", status: "building" })}
			/>,
		);

		const row = (await screen.findAllByRole("listitem"))[0] as HTMLElement;
		expect(within(row).getByText(/running/)).toBeInTheDocument();
		expect(within(row).queryByText(/ s$/)).not.toBeInTheDocument();
	});

	test("shows 'Loading steps…' until the events arrive", async () => {
		const gate = Promise.withResolvers<void>();
		stubApi({
			...routes([], "log-load"),
			"GET /deployments/log-load/events": async () => {
				await gate.promise;
				return Response.json({ items: [eventView({ id: 1, step: "pull" })] });
			},
		});
		await renderWithProviders(
			<DeploymentDetail deployment={deployment({ id: "log-load" })} />,
		);

		expect(screen.getByText("Loading steps…")).toBeInTheDocument();
		gate.resolve();
		expect(
			await screen.findByRole("list", { name: "Deployment steps" }),
		).toBeInTheDocument();
		expect(screen.queryByText("Loading steps…")).not.toBeInTheDocument();
	});

	test("says so when no step was recorded", async () => {
		stubApi(routes([], "log-none"));
		await renderWithProviders(
			<DeploymentDetail deployment={deployment({ id: "log-none" })} />,
		);
		expect(
			await screen.findByText("No steps recorded yet."),
		).toBeInTheDocument();
	});

	test("placement events are notices above the steps, not step rows", async () => {
		stubApi(
			routes(
				[
					eventView({
						id: 1,
						step: "placement",
						status: "info",
						message: "No build server available; building on edge-1",
					}),
					eventView({ id: 2, step: "pull", status: "succeeded" }),
				],
				"log-place",
			),
		);
		await renderWithProviders(
			<DeploymentDetail deployment={deployment({ id: "log-place" })} />,
		);

		expect(
			await screen.findByText("Built on the app's server instead"),
		).toBeInTheDocument();
		expect(
			screen.getByText("No build server available; building on edge-1"),
		).toBeInTheDocument();
		const rows = within(
			screen.getByRole("list", { name: "Deployment steps" }),
		).getAllByRole("listitem");
		expect(rows).toHaveLength(1);
		expect(
			within(rows[0] as HTMLElement).queryByText("placement"),
		).not.toBeInTheDocument();
	});
});

describe("DeploymentDetail header notices", () => {
	test("a running deployment waiting for an offline server says so", async () => {
		stubApi(routes([], "log-wait"));
		await renderWithProviders(
			<DeploymentDetail
				deployment={deployment({
					id: "log-wait",
					status: "queued",
					waitingFor: {
						serverId: "srv-1",
						serverName: "edge-1",
						offlineSince: null,
					},
				})}
			/>,
		);
		expect(await screen.findByText(/Waiting for edge-1/)).toBeInTheDocument();
	});

	test("a finished deployment does not show the waiting notice", async () => {
		stubApi(routes([], "log-nowait"));
		await renderWithProviders(
			<DeploymentDetail
				deployment={deployment({
					id: "log-nowait",
					status: "succeeded",
					waitingFor: {
						serverId: "srv-1",
						serverName: "edge-1",
						offlineSince: null,
					},
				})}
			/>,
		);
		await screen.findByText("No steps recorded yet.");
		expect(screen.queryByText(/Waiting for edge-1/)).not.toBeInTheDocument();
	});

	test("a build on another server shows where it was built and deployed", async () => {
		stubApi({
			...routes([], "log-built"),
			"GET /servers": {
				body: make("PageServer", {
					items: [
						make("Server", { id: "srv-1", name: "web-02" }),
						make("Server", { id: "srv-b", name: "build-01" }),
					],
				}),
			},
		});
		await renderWithProviders(
			<DeploymentDetail
				deployment={deployment({
					id: "log-built",
					serverId: "srv-1",
					buildServerId: "srv-b",
				})}
			/>,
		);
		expect(
			await screen.findByRole("group", {
				name: "Built on build-01, deployed on web-02",
			}),
		).toBeInTheDocument();
	});
});

describe("DeploymentDetail log", () => {
	test("a finished deployment counts the stored lines", async () => {
		stubApi({
			...routes([], "log-done"),
			"GET /deployments/log-done/logs": () =>
				logStreamResponse([
					{ line: "step 1", stream: "build" },
					{ line: "step 2", stream: "build" },
					{ line: "done", stream: "deploy" },
				]),
		});
		await renderWithProviders(
			<DeploymentDetail deployment={deployment({ id: "log-done" })} />,
		);
		expect(await screen.findByText("3 lines")).toBeInTheDocument();
	});

	test("a finished deployment with no lines says nothing was recorded", async () => {
		stubApi(routes([], "log-empty"));
		await renderWithProviders(
			<DeploymentDetail deployment={deployment({ id: "log-empty" })} />,
		);
		expect(
			await screen.findByText(
				"No log lines were recorded for this deployment.",
			),
		).toBeInTheDocument();
		expect(screen.getByText("0 lines")).toBeInTheDocument();
	});

	test("a running deployment follows live once lines arrive", async () => {
		stubApi({
			...routes([], "log-live"),
			"GET /deployments/log-live/logs": () =>
				openLogStreamResponse([{ line: "compiling", stream: "build" }]),
		});
		await renderWithProviders(
			<DeploymentDetail
				deployment={deployment({ id: "log-live", status: "building" })}
			/>,
		);
		expect(await screen.findByText("following live")).toBeInTheDocument();
	});

	test("a running deployment with no line yet is connecting and waits for the first line", async () => {
		stubApi({
			...routes([], "log-conn"),
			"GET /deployments/log-conn/logs": () => openLogStreamResponse([]),
		});
		await renderWithProviders(
			<DeploymentDetail
				deployment={deployment({ id: "log-conn", status: "building" })}
			/>,
		);
		expect(await screen.findByText("connecting…")).toBeInTheDocument();
		expect(
			screen.getByText("Waiting for the first log line…"),
		).toBeInTheDocument();
	});

	test("the log is followed under the deployment id with the debug stream left out", async () => {
		const api = stubApi(routes([], "log-req"));
		await renderWithProviders(
			<DeploymentDetail deployment={deployment({ id: "log-req" })} />,
		);
		await screen.findByText("0 lines");
		const call = api.calls.find((c) => c.path === "/deployments/log-req/logs");
		expect(call?.method).toBe("GET");
		expect(call?.query.get("follow")).toBe("true");
		expect(call?.query.get("streams")).toBeNull();
	});
});

describe("DeploymentDetail debug panel entry", () => {
	test("the Debug button writes the deployment id into the address", async () => {
		stubApi(routes([], "log-dbg"));
		await renderWithProviders(
			<>
				<DeploymentDetail deployment={deployment({ id: "log-dbg" })} />
				<DebugParam />
			</>,
		);
		expect(screen.getByLabelText("debug param")).toHaveTextContent("");

		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: /Debug/ }));

		await waitFor(() =>
			expect(screen.getByLabelText("debug param")).toHaveTextContent("log-dbg"),
		);
	});

	test("pressing d opens the debug panel for this deployment", async () => {
		stubApi(routes([], "log-key"));
		await renderWithProviders(
			<>
				<DeploymentDetail deployment={deployment({ id: "log-key" })} />
				<DebugParam />
			</>,
		);
		await screen.findByText("0 lines");

		fireEvent.keyDown(document.body, { key: "d" });

		await waitFor(() =>
			expect(screen.getByLabelText("debug param")).toHaveTextContent("log-key"),
		);
	});

	test("while the debug panel is open this view lets go of its own log stream", async () => {
		const api = stubApi(routes([], "log-held"));
		await renderWithProviders(
			<DeploymentDetail deployment={deployment({ id: "log-held" })} />,
			{ path: "/?debug=log-held" },
		);
		await screen.findByText("0 lines");
		expect(api.calls.some((c) => c.path.endsWith("/logs"))).toBe(false);
	});
});
