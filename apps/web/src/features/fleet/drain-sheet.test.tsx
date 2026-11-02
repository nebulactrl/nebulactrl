import { describe, expect, jest, test } from "bun:test";
import { act, fireEvent, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	DrainPlan,
	DrainView,
	Move,
	PlanMove,
	Server,
} from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { DrainSheet, type DrainSheetServer } from "./drain-sheet";

const problem = (status: number, detail: string) => ({
	status,
	body: { status, title: "Error", detail, code: "bad_request" },
});

const sheetServer = (
	overrides: Partial<DrainSheetServer> = {},
): DrainSheetServer => ({
	id: "s1",
	name: "alpha",
	status: "online",
	drainState: "none",
	drainMessage: "",
	...overrides,
});

const listed = (
	id: string,
	name: string,
	status: Server["status"] = "online",
	drainState: Server["drainState"] = "none",
) =>
	make("Server", {
		id,
		name,
		status,
		drainState,
		drainMessage: "",
		serviceCount: 0,
		acceptWorkloads: true,
	});

const servers = {
	"GET /servers": {
		body: make("PageServer", {
			items: [
				listed("s1", "alpha"),
				listed("s2", "beta"),
				listed("s3", "gamma", "offline"),
				listed("s4", "delta", "revoked"),
				listed("s5", "epsilon", "pending"),
			],
		}),
	},
};

const planMove = (overrides: Partial<PlanMove> = {}): PlanMove => ({
	serviceId: "svc1",
	serviceName: "api",
	projectName: "Shop",
	environmentName: "Production",
	stateful: false,
	targetServerId: "s2",
	targetServerName: "beta",
	blocking: null,
	warnings: null,
	...overrides,
});

const plan = (
	moves: PlanMove[] | null,
	capacity: DrainPlan["capacity"] = { targetFits: true, reasons: null },
): DrainPlan => ({ moves, capacity });

const move = (overrides: Partial<Move> = {}): Move => ({
	serviceId: "svc1",
	serviceName: "api",
	projectName: "Shop",
	environmentName: "Production",
	stateful: false,
	sourceServerId: "s1",
	targetServerId: "s2",
	targetServerName: "beta",
	deploymentId: "",
	message: "",
	status: "pending",
	...overrides,
});

const drainView = (overrides: Partial<DrainView> = {}): DrainView => ({
	id: "d1",
	serverId: "s1",
	state: "running",
	message: "",
	actorLabel: "ada",
	cancelRequested: false,
	createdAt: "2026-01-01T00:00:00Z",
	finishedAt: null,
	moves: [],
	...overrides,
});

const drainReply = (drain: DrainView | null) => ({
	"GET /servers/:id/drain": { body: { drain } },
});

async function openSheet(
	server: DrainSheetServer,
	open = true,
	onOpenChange: (open: boolean) => void = () => {},
) {
	return renderWithProviders(
		<DrainSheet server={server} open={open} onOpenChange={onOpenChange} />,
	);
}

const holdButton = () =>
	screen.getByRole("button", { name: /Hold to drain alpha/ });

/** Presses and holds the drain key; the component completes the hold after 1.3s of real time. */
function holdKey() {
	fireEvent.keyDown(holdButton(), { key: "Enter" });
}

/** Waits for a result of the full hold (1.3s) with room for a loaded machine. */
const HOLD_WAIT = { timeout: 6000 };
const HOLD_TEST_TIMEOUT = 15_000;

describe("DrainSheet: planning", () => {
	test("does nothing while closed", async () => {
		const api = stubApi({ ...servers, ...drainReply(null) });
		await openSheet(sheetServer(), false);
		expect(screen.queryByText("Drain alpha")).not.toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	test("plans on open with automatic targets and lists the moves", async () => {
		const api = stubApi({
			...servers,
			...drainReply(null),
			"POST /servers/:id/drain/plan": {
				body: plan([
					planMove(),
					planMove({
						serviceId: "svc2",
						serviceName: "web",
						targetServerId: "s6",
						targetServerName: "zeta",
					}),
				]),
			},
		});
		await openSheet(sheetServer());
		expect(screen.getByText("Drain alpha")).toBeInTheDocument();
		expect(
			screen.getByRole("status", { name: "Planning the moves" }),
		).toBeInTheDocument();
		const list = await screen.findByRole("list", { name: "Planned moves" });
		const rows = within(list).getAllByRole("listitem");
		expect(rows).toHaveLength(2);
		expect(within(rows[0] as HTMLElement).getByText("api")).toBeInTheDocument();
		expect(
			within(rows[0] as HTMLElement).getByText("Shop / Production"),
		).toBeInTheDocument();
		expect(
			within(rows[0] as HTMLElement).getByText("beta"),
		).toBeInTheDocument();
		expect(
			within(rows[1] as HTMLElement).getByText("zeta"),
		).toBeInTheDocument();
		expect(
			screen.getByText("The targets have room for everything."),
		).toBeInTheDocument();
		expect(api.calls.find((c) => c.path.endsWith("/plan"))).toMatchObject({
			method: "POST",
			path: "/servers/s1/drain/plan",
			body: {},
		});
		// The sheet starts empty-handed: no start request before the hold.
		expect(
			api.calls.filter(
				(c) => c.path === "/servers/s1/drain" && c.method === "POST",
			),
		).toEqual([]);
	});

	test("a server with nothing placed says draining only closes it", async () => {
		stubApi({
			...servers,
			...drainReply(null),
			"POST /servers/:id/drain/plan": { body: plan(null) },
		});
		await openSheet(sheetServer());
		expect(
			await screen.findByText(/No services are placed on this server/),
		).toBeInTheDocument();
		// Nothing to confirm: the hold is available at once.
		expect(holdButton()).toBeEnabled();
		expect(
			screen.queryByText("The targets have room for everything."),
		).not.toBeInTheDocument();
	});

	test("shows why a plan could not be made", async () => {
		stubApi({
			...servers,
			...drainReply(null),
			"POST /servers/:id/drain/plan": problem(409, "No other server is online"),
		});
		await openSheet(sheetServer());
		expect(await screen.findByText("No plan")).toBeInTheDocument();
		expect(screen.getByText("No other server is online")).toBeInTheDocument();
		expect(holdButton()).toBeDisabled();
	});

	test("a blocking problem is shown on its move and keeps the hold disabled", async () => {
		stubApi({
			...servers,
			...drainReply(null),
			"POST /servers/:id/drain/plan": {
				body: plan([
					planMove({
						targetServerId: "",
						targetServerName: "",
						blocking: [
							{ code: "pinned", message: "api is pinned to this server" },
						],
					}),
				]),
			},
		});
		await openSheet(sheetServer());
		expect(
			await screen.findByText("api is pinned to this server"),
		).toBeInTheDocument();
		expect(screen.getByText("no target")).toBeInTheDocument();
		expect(holdButton()).toBeDisabled();
	});

	test("warns when a target lacks memory and lists the reasons", async () => {
		stubApi({
			...servers,
			...drainReply(null),
			"POST /servers/:id/drain/plan": {
				body: plan([planMove()], {
					targetFits: false,
					reasons: ["beta has 1 GiB free, api needs 2 GiB"],
				}),
			},
		});
		await openSheet(sheetServer());
		expect(
			await screen.findByText("Not enough memory on a target"),
		).toBeInTheDocument();
		expect(
			screen.getByText("beta has 1 GiB free, api needs 2 GiB"),
		).toBeInTheDocument();
		expect(
			screen.queryByText("The targets have room for everything."),
		).not.toBeInTheDocument();
	});

	test("lists capacity notes when the targets fit but have remarks", async () => {
		stubApi({
			...servers,
			...drainReply(null),
			"POST /servers/:id/drain/plan": {
				body: plan([planMove()], {
					targetFits: true,
					reasons: ["beta will be 90% full"],
				}),
			},
		});
		await openSheet(sheetServer());
		expect(
			await screen.findByText("beta will be 90% full"),
		).toBeInTheDocument();
		expect(
			screen.queryByText("The targets have room for everything."),
		).not.toBeInTheDocument();
	});

	test("a stateful service needs the volume confirmation before the hold unlocks", async () => {
		stubApi({
			...servers,
			...drainReply(null),
			"POST /servers/:id/drain/plan": {
				body: plan([
					planMove({
						serviceName: "pg",
						stateful: true,
						warnings: [
							"Volumes are not copied to the target.",
							"Connections drop during the move.",
						],
					}),
				]),
			},
		});
		const user = userEvent.setup();
		await openSheet(sheetServer());
		const confirm = await screen.findByRole("checkbox", {
			name: /Volumes are not copied\./,
		});
		expect(screen.getByText("stateful")).toBeInTheDocument();
		// The volume warning is an alert; other warnings are plain notes.
		expect(screen.getByRole("alert")).toHaveTextContent(
			"Volumes are not copied to the target.",
		);
		expect(
			screen.getByText("Connections drop during the move."),
		).toBeInTheDocument();
		expect(
			screen.getByText(/pg\s+starts\s+with empty volumes/),
		).toBeInTheDocument();
		expect(holdButton()).toBeDisabled();
		await user.click(confirm);
		expect(confirm).toBeChecked();
		expect(holdButton()).toBeEnabled();
		await user.click(confirm);
		expect(holdButton()).toBeDisabled();
	});

	test("several stateful services are named together", async () => {
		stubApi({
			...servers,
			...drainReply(null),
			"POST /servers/:id/drain/plan": {
				body: plan([
					planMove({ serviceName: "pg", stateful: true }),
					planMove({ serviceId: "svc2", serviceName: "redis", stateful: true }),
				]),
			},
		});
		await openSheet(sheetServer());
		expect(
			await screen.findByText(/pg, redis\s+start\s+with empty volumes/),
		).toBeInTheDocument();
	});

	test(
		"holding the key starts the drain with the planned moves and confirmation",
		async () => {
			const api = stubApi({
				...servers,
				...drainReply(null),
				"POST /servers/:id/drain/plan": {
					body: plan([
						planMove({ serviceName: "pg", stateful: true }),
						planMove({
							serviceId: "svc2",
							serviceName: "web",
							targetServerId: "",
							targetServerName: "",
						}),
					]),
				},
				"POST /servers/:id/drain": { status: 202, body: drainView() },
			});
			const user = userEvent.setup();
			await openSheet(sheetServer());
			await user.click(
				await screen.findByRole("checkbox", { name: /Volumes are not copied/ }),
			);
			holdKey();
			expect(
				await screen.findByText("Draining alpha", {}, HOLD_WAIT),
			).toBeInTheDocument();
			expect(
				api.calls.find(
					(c) => c.method === "POST" && c.path === "/servers/s1/drain",
				),
			).toMatchObject({
				// A move without a target is not sent.
				body: {
					moves: [{ serviceId: "svc1", targetServerId: "s2" }],
					confirmStateful: true,
				},
			});
		},
		HOLD_TEST_TIMEOUT,
	);

	test("releasing the key early starts nothing", async () => {
		const api = stubApi({
			...servers,
			...drainReply(null),
			"POST /servers/:id/drain/plan": { body: plan([planMove()]) },
		});
		await openSheet(sheetServer());
		await screen.findByRole("list", { name: "Planned moves" });
		jest.useFakeTimers();
		try {
			fireEvent.keyDown(holdButton(), { key: "Enter" });
			act(() => {
				jest.advanceTimersByTime(600);
			});
			fireEvent.keyUp(holdButton(), { key: "Enter" });
			act(() => {
				jest.advanceTimersByTime(2000);
			});
		} finally {
			jest.useRealTimers();
		}
		expect(
			api.calls.filter(
				(c) => c.method === "POST" && c.path === "/servers/s1/drain",
			),
		).toEqual([]);
	});

	test(
		"a refused start shows the server's reason",
		async () => {
			stubApi({
				...servers,
				...drainReply(null),
				"POST /servers/:id/drain/plan": { body: plan([planMove()]) },
				"POST /servers/:id/drain": problem(409, "The plan changed; plan again"),
			});
			await openSheet(sheetServer());
			await screen.findByRole("list", { name: "Planned moves" });
			holdKey();
			expect(
				await screen.findByText("The plan changed; plan again", {}, HOLD_WAIT),
			).toBeInTheDocument();
			expect(screen.queryByText("Draining alpha")).not.toBeInTheDocument();
		},
		HOLD_TEST_TIMEOUT,
	);

	test("picking a target re-plans with it; unavailable servers cannot be picked", async () => {
		const api = stubApi({
			...servers,
			...drainReply(null),
			"POST /servers/:id/drain/plan": (request) =>
				request
					.clone()
					.json()
					.then((body: { targetServerId?: string }) =>
						Response.json(
							plan([
								planMove({
									targetServerId: body.targetServerId ?? "s2",
									targetServerName: body.targetServerId ? "gamma-ish" : "beta",
								}),
							]),
						),
					),
		});
		const user = userEvent.setup();
		await openSheet(sheetServer());
		await screen.findByRole("list", { name: "Planned moves" });
		await user.click(
			screen.getByRole("combobox", { name: /Send services to/ }),
		);
		// Revoked and pending servers are not offered; an offline one is shown closed.
		expect(
			screen.queryByRole("option", { name: /delta/ }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole("option", { name: /epsilon/ }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole("option", { name: /alpha/ }),
		).not.toBeInTheDocument();
		expect(screen.getByRole("option", { name: /gamma/ })).toHaveAttribute(
			"aria-disabled",
			"true",
		);
		await user.click(screen.getByRole("option", { name: /beta/ }));
		expect(await screen.findByText("gamma-ish")).toBeInTheDocument();
		const plans = api.calls.filter((c) => c.path.endsWith("/plan"));
		expect(plans.map((c) => c.body)).toEqual([{}, { targetServerId: "s2" }]);
	});
});

describe("DrainSheet: progress", () => {
	const running = (overrides: Partial<DrainView> = {}) =>
		drainReply(
			drainView({
				moves: [
					move({ status: "done" }),
					move({
						serviceId: "svc2",
						serviceName: "web",
						status: "deploying",
						deploymentStatus: "health_check",
					}),
					move({ serviceId: "svc3", serviceName: "cron", status: "pending" }),
					move({
						serviceId: "svc4",
						serviceName: "bad",
						status: "failed",
						message: "image pull failed",
					}),
				],
				...overrides,
			}),
		);

	test("shows the moves one by one with their state", async () => {
		const api = stubApi({ ...servers, ...running() });
		await openSheet(sheetServer({ drainState: "draining" }));
		const list = await screen.findByRole("list", { name: "Moves" });
		const rows = within(list).getAllByRole("listitem");
		expect(rows).toHaveLength(4);
		expect(rows[0]).toHaveTextContent("Moved to beta");
		expect(rows[1]).toHaveTextContent("Deploying to beta (health check)");
		expect(rows[2]).toHaveTextContent("Waiting");
		expect(rows[3]).toHaveTextContent("Failed");
		expect(
			within(rows[3] as HTMLElement).getByText("image pull failed"),
		).toBeInTheDocument();
		expect(screen.getByText("Failed at bad")).toBeInTheDocument();
		expect(screen.getByText("25%")).toBeInTheDocument();
		// No plan is made while a drain is running.
		expect(api.calls.some((c) => c.path.endsWith("/plan"))).toBe(false);
		expect(
			screen.queryByRole("button", { name: /Hold to drain/ }),
		).not.toBeInTheDocument();
	});

	test("labels the move in flight and the count done", async () => {
		stubApi({
			...servers,
			...running({
				moves: [
					move({ status: "done" }),
					move({ serviceId: "svc2", serviceName: "web", status: "moving" }),
				],
			}),
		});
		await openSheet(sheetServer({ drainState: "draining" }));
		expect(
			await screen.findByText("Moving web… 1 of 2 done"),
		).toBeInTheDocument();
		expect(screen.getByText("50%")).toBeInTheDocument();
	});

	test("Cancel drain posts the cancel and confirms", async () => {
		const api = stubApi({
			...servers,
			...running(),
			"POST /servers/:id/drain/cancel": {
				body: drainView({ cancelRequested: true }),
			},
		});
		const user = userEvent.setup();
		await openSheet(sheetServer({ drainState: "draining" }));
		await user.click(
			await screen.findByRole("button", { name: "Cancel drain" }),
		);
		expect(await screen.findByText("Cancel requested")).toBeInTheDocument();
		expect(
			api.calls.find(
				(c) => c.method === "POST" && c.path === "/servers/s1/drain/cancel",
			),
		).toBeDefined();
	});

	test("a requested cancel disables the button and says so", async () => {
		stubApi({ ...servers, ...running({ cancelRequested: true }) });
		await openSheet(sheetServer({ drainState: "draining" }));
		expect(
			await screen.findByRole("button", { name: "Canceling…" }),
		).toBeDisabled();
		expect(
			screen.getByText(
				/Cancel requested: the drain stops after the move in flight/,
			),
		).toBeInTheDocument();
	});

	test("a refused cancel shows the reason", async () => {
		stubApi({
			...servers,
			...running(),
			"POST /servers/:id/drain/cancel": problem(
				409,
				"The drain already finished",
			),
		});
		const user = userEvent.setup();
		await openSheet(sheetServer({ drainState: "draining" }));
		await user.click(
			await screen.findByRole("button", { name: "Cancel drain" }),
		);
		expect(
			await screen.findByText("The drain already finished"),
		).toBeInTheDocument();
	});

	test("shows 'Starting…' until the drain record arrives", async () => {
		let release: (() => void) | undefined;
		stubApi({
			...servers,
			"GET /servers/:id/drain": () =>
				new Promise<Response>((resolve) => {
					release = () => resolve(Response.json({ drain: drainView() }));
				}),
		});
		await openSheet(sheetServer({ drainState: "draining" }));
		expect(screen.getByText("Starting…")).toBeInTheDocument();
		expect(screen.getByText("0%")).toBeInTheDocument();
		await act(async () => release?.());
		expect(await screen.findByText("0 of 0 moved")).toBeInTheDocument();
		expect(screen.getByText("100%")).toBeInTheDocument();
	});
});

describe("DrainSheet: failed", () => {
	const failed = sheetServer({
		drainState: "failed",
		drainMessage: "move of web failed",
	});
	const failedDrain = drainReply(
		drainView({
			state: "failed",
			moves: [
				move({ status: "done" }),
				move({
					serviceId: "svc2",
					serviceName: "web",
					status: "failed",
					message: "boom",
				}),
			],
		}),
	);

	test("explains where the drain stopped and shows its moves", async () => {
		stubApi({ ...servers, ...failedDrain });
		await openSheet(failed);
		expect(await screen.findByText("Drain stopped")).toBeInTheDocument();
		expect(screen.getByText(/move of web failed/)).toBeInTheDocument();
		expect(
			await screen.findByRole("list", { name: "Moves" }),
		).toBeInTheDocument();
		expect(screen.getByText("boom")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Retry (re-plan)" }),
		).toBeInTheDocument();
	});

	test("falls back to the drain's own message, then to a generic one", async () => {
		stubApi({
			...servers,
			...drainReply(drainView({ state: "failed", message: "disk full" })),
		});
		const { unmount } = await openSheet(sheetServer({ drainState: "failed" }));
		expect(await screen.findByText(/disk full/)).toBeInTheDocument();
		unmount();
		stubApi({ ...servers, ...drainReply(null) });
		await openSheet(sheetServer({ drainState: "failed" }));
		expect(await screen.findByText(/A move failed\./)).toBeInTheDocument();
	});

	test("Retry re-plans what is left", async () => {
		const api = stubApi({
			...servers,
			...failedDrain,
			"POST /servers/:id/drain/plan": { body: plan([planMove()]) },
		});
		const user = userEvent.setup();
		await openSheet(failed);
		await user.click(
			await screen.findByRole("button", { name: "Retry (re-plan)" }),
		);
		expect(await screen.findByText("Planning the rest")).toBeInTheDocument();
		expect(
			await screen.findByRole("list", { name: "Planned moves" }),
		).toBeInTheDocument();
		expect(screen.queryByText("Drain stopped")).not.toBeInTheDocument();
		expect(api.calls.filter((c) => c.path.endsWith("/plan"))).toHaveLength(1);
	});

	test("Undrain reopens the server to services", async () => {
		const api = stubApi({
			...servers,
			...failedDrain,
			"POST /servers/:id/undrain": { body: listed("s1", "alpha") },
		});
		const user = userEvent.setup();
		await openSheet(failed);
		await user.click(await screen.findByRole("button", { name: "Undrain" }));
		expect(
			await screen.findByText("alpha accepts services again"),
		).toBeInTheDocument();
		expect(
			api.calls.find(
				(c) => c.method === "POST" && c.path === "/servers/s1/undrain",
			),
		).toBeDefined();
	});

	test("a refused undrain shows the reason", async () => {
		stubApi({
			...servers,
			...failedDrain,
			"POST /servers/:id/undrain": problem(409, "A drain is still running"),
		});
		const user = userEvent.setup();
		await openSheet(failed);
		await user.click(await screen.findByRole("button", { name: "Undrain" }));
		expect(
			await screen.findByText("A drain is still running"),
		).toBeInTheDocument();
	});
});

describe("DrainSheet: drained", () => {
	const drained = sheetServer({ drainState: "drained" });

	test("says the server is empty and lists the completed moves", async () => {
		stubApi({
			...servers,
			...drainReply(
				drainView({ state: "completed", moves: [move({ status: "done" })] }),
			),
		});
		await openSheet(drained);
		expect(await screen.findByText("alpha is drained")).toBeInTheDocument();
		expect(
			await screen.findByRole("list", { name: "Moves" }),
		).toHaveTextContent("Moved to beta");
		expect(
			screen.queryByRole("button", { name: /Hold to drain/ }),
		).not.toBeInTheDocument();
	});

	test("hides the moves of a drain that did not complete", async () => {
		stubApi({
			...servers,
			...drainReply(
				drainView({ state: "canceled", moves: [move({ status: "done" })] }),
			),
		});
		await openSheet(drained);
		await screen.findByText("alpha is drained");
		expect(
			screen.queryByRole("list", { name: "Moves" }),
		).not.toBeInTheDocument();
	});

	test("Undrain posts and confirms", async () => {
		const api = stubApi({
			...servers,
			...drainReply(null),
			"POST /servers/:id/undrain": { body: listed("s1", "alpha") },
		});
		const user = userEvent.setup();
		await openSheet(drained);
		await user.click(await screen.findByRole("button", { name: "Undrain" }));
		expect(
			await screen.findByText("alpha accepts services again"),
		).toBeInTheDocument();
		expect(api.calls.some((c) => c.path === "/servers/s1/undrain")).toBe(true);
	});

	test("Close asks the parent to close the sheet", async () => {
		stubApi({ ...servers, ...drainReply(null) });
		const closed: boolean[] = [];
		const user = userEvent.setup();
		await openSheet(drained, true, (open) => closed.push(open));
		await screen.findByText("alpha is drained");
		// The sheet's corner close and the footer's Close both ask the parent.
		const buttons = screen.getAllByRole("button", { name: "Close" });
		expect(buttons.length).toBeGreaterThan(0);
		for (const button of buttons) {
			closed.length = 0;
			await user.click(button);
			expect(closed).toEqual([false]);
		}
	});
});
