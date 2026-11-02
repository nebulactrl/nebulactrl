import { describe, expect, spyOn, test } from "bun:test";
import {
	act,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type ApiRoutes, stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { MAX_REPLICAS, PANEL_CAVEAT, PANEL_PURPOSE } from "./model";
import { MANUAL_NOTE } from "./promote-dialog";
import {
	member,
	postgresService,
	promotePlan,
	replicaPlan,
	replication,
	serversPage,
	wireServer,
} from "./replicas.fixture";
import { ReplicasPanel } from "./replicas-panel";

const REPLICATION = "GET /services/:id/database/replication";
const MIB = 1024 * 1024;
const ago = (ms: number) => new Date(Date.now() - ms).toISOString();
const fresh = () => ago(2_000);

const rebuilt = {
	status: 202,
	body: make("RebuildResult", { serviceId: "svc-r1", status: "rebuilding" }),
};
const removed = {
	status: 202,
	body: make("RemoveResult", { serviceId: "svc-r1", status: "removing" }),
};

const problem = (status: number, detail: string, code: string) => ({
	status,
	body: { status, title: "Error", detail, code },
});

const running = {
	id: "promo-1",
	status: "running" as const,
	mode: "switchover" as const,
	step: "freeze",
	message: "Freezing the old primary",
	startedAt: "2026-01-01T00:00:00Z",
};

const ROUTE = { path: "/p/proj-1/prod", routePath: "/p/$projectId/$envSlug" };

async function panel(
	view: Parameters<typeof replication>[0] | "pending",
	options: {
		deployed?: boolean;
		service?: ReturnType<typeof postgresService>;
	} = {},
	extra: ApiRoutes = {},
) {
	const api = stubApi({
		[REPLICATION]: { body: replication(view === "pending" ? {} : view) },
		...extra,
	});
	const result = await renderWithProviders(
		<ReplicasPanel
			service={options.service ?? postgresService()}
			deployed={options.deployed ?? true}
		/>,
		ROUTE,
	);
	return { api, ...result };
}

const replicaRow = (name: string) =>
	within(screen.getByRole("list", { name: "Replicas" }))
		.getByText(name)
		.closest("li") as HTMLElement;

/** The remove/rebuild confirm's hold key, rushed to the next tick (see features/servers/remove-server.test.tsx). */
async function hold(button: HTMLElement) {
	const real = window.setTimeout;
	const rushed = spyOn(window, "setTimeout").mockImplementation(((
		handler: TimerHandler,
		ms?: number,
		...args: unknown[]
	) =>
		real(handler, ms === 1300 ? 0 : ms, ...args)) as typeof window.setTimeout);
	try {
		fireEvent.keyDown(button, { key: "Enter" });
	} finally {
		rushed.mockRestore();
	}
	const tick = Promise.withResolvers<void>();
	window.setTimeout(tick.resolve, 0);
	await act(async () => {
		await tick.promise;
	});
}

describe("ReplicasPanel loading states", () => {
	test("shows a placeholder while the replication view loads", async () => {
		stubApi({ [REPLICATION]: () => new Promise<Response>(() => {}) });
		await renderWithProviders(
			<ReplicasPanel service={postgresService()} deployed />,
		);
		expect(
			screen.getByRole("region", { name: "Replicas" }),
		).toBeInTheDocument();
		expect(screen.queryByText("Standby replica")).not.toBeInTheDocument();
	});

	test("a failed load says so and Retry recovers", async () => {
		let attempts = 0;
		stubApi({
			[REPLICATION]: () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{
								status: 500,
								title: "x",
								detail: "database is down",
								code: "internal",
							},
							{ status: 500 },
						)
					: Response.json(replication({ role: "standalone" }));
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<ReplicasPanel service={postgresService()} deployed />,
		);
		expect(
			await screen.findByText("The replicas did not load"),
		).toBeInTheDocument();
		expect(screen.getByText(/database is down/)).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Retry" }));
		expect(await screen.findByText(PANEL_PURPOSE)).toBeInTheDocument();
		expect(
			screen.queryByText("The replicas did not load"),
		).not.toBeInTheDocument();
	});

	test("renders nothing on a control plane without the replica API", async () => {
		const api = stubApi({
			[REPLICATION]: problem(404, "no such route", "not_found"),
		});
		const { container } = await renderWithProviders(
			<ReplicasPanel service={postgresService()} deployed />,
		);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		await waitFor(() =>
			expect(screen.queryByRole("region", { name: "Replicas" })).toBeNull(),
		);
		expect(screen.queryByText("The replicas did not load")).toBeNull();
		expect(container.querySelector("section")).toBeNull();
	});
});

describe("ReplicasPanel standalone", () => {
	test("explains the feature and offers to add a replica", async () => {
		const { api } = await panel({ role: "standalone" });
		expect(await screen.findByText("Standby replica")).toBeInTheDocument();
		expect(screen.getByText(PANEL_PURPOSE)).toBeInTheDocument();
		expect(screen.getByText(PANEL_CAVEAT)).toBeInTheDocument();
		expect(screen.getByText(/None yet\./)).toHaveTextContent(
			"DATABASE_WRITE_* and DATABASE_READ_*",
		);
		expect(
			screen.getByRole("button", { name: "Add standby replica" }),
		).toBeEnabled();
		expect(api.calls[0]?.path).toBe(
			"/services/svc-primary/database/replication",
		);
	});

	test("cannot add a replica before the database is deployed", async () => {
		await panel({ role: "standalone" }, { deployed: false });
		const add = await screen.findByRole("button", {
			name: "Add standby replica",
		});
		expect(add).toBeDisabled();
		expect(add).toHaveAttribute(
			"title",
			"Deploy the database first: a replica copies the running primary.",
		);
	});

	test("Add standby replica opens the add dialog for this database", async () => {
		const user = userEvent.setup();
		await panel(
			{ role: "standalone" },
			{},
			{
				"GET /servers": { body: serversPage(wireServer("srv-a", "eu-1")) },
				"GET /mesh": { body: make("View", { nodes: [] }) },
				"POST /services/:id/database/replicas": (request: Request) =>
					request
						.clone()
						.json()
						.then(() =>
							Response.json({
								dryRun: true,
								plan: replicaPlan({ serverId: "srv-a", sameServer: true }),
							}),
						),
			},
		);
		await user.click(
			await screen.findByRole("button", { name: "Add standby replica" }),
		);
		const dialog = await screen.findByRole("dialog");
		expect(
			within(dialog).getByText("Add a standby replica · Orders DB"),
		).toBeInTheDocument();
		// The suggested name is free of this primary's replicas.
		expect(within(dialog).getByLabelText("Name")).toHaveAttribute(
			"placeholder",
			"orders-db-replica",
		);
	});
});

describe("ReplicasPanel primary", () => {
	test("a primary without replicas says so", async () => {
		await panel({ role: "primary", replicas: [] });
		expect(await screen.findByText("No replicas.")).toBeInTheDocument();
		expect(
			screen.queryByRole("list", { name: "Replicas" }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Copy writer URL" }),
		).toBeNull();
		expect(
			screen.getByRole("button", { name: "Add standby replica" }),
		).toBeEnabled();
	});

	test("lists each replica with its server, state, lag and WAL status", async () => {
		await panel({
			role: "primary",
			replicas: [
				member({
					id: "r1",
					slug: "orders-r1",
					serverName: "eu-2",
					state: "streaming",
					lagBytes: 4 * MIB,
					replayLagMs: 1500,
					walStatus: "reserved",
					observedAt: fresh(),
				}),
				member({
					id: "r2",
					slug: "orders-r2",
					serverName: "ap-1",
					state: "lagging",
					lagBytes: 100 * MIB,
					walStatus: "extended",
					observedAt: fresh(),
				}),
			],
		});
		await screen.findByRole("list", { name: "Replicas" });
		const first = within(replicaRow("orders-r1"));
		expect(first.getByText("eu-2")).toBeInTheDocument();
		expect(first.getByText("Streaming")).toBeInTheDocument();
		expect(first.getByText("WAL reserved")).toBeInTheDocument();
		expect(first.getByText(/Measured/)).toBeInTheDocument();
		expect(
			first
				.getByRole("progressbar", { name: "Replication lag" })
				.getAttribute("aria-valuetext"),
		).toStartWith("4 MiB behind · 1.5 s");
		expect(
			first.getByText("Receiving changes from the primary."),
		).toBeInTheDocument();

		const second = within(replicaRow("orders-r2"));
		expect(second.getByText("ap-1")).toBeInTheDocument();
		expect(second.getByText("Lagging")).toBeInTheDocument();
		expect(second.getByText("WAL extended")).toBeInTheDocument();
		expect(second.getByRole("progressbar").getAttribute("aria-valuenow")).toBe(
			"100",
		);

		expect(
			screen.getByRole("button", { name: "Copy writer URL" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Copy reader URL" }),
		).toBeInTheDocument();
		expect(screen.getByText(MANUAL_NOTE)).toBeInTheDocument();
	});

	test("a replica's name links to it in the environment", async () => {
		await panel({
			role: "primary",
			replicas: [member({ id: "r1", slug: "orders-r1", observedAt: fresh() })],
		});
		await screen.findByRole("list", { name: "Replicas" });
		const link = within(replicaRow("orders-r1")).getByRole("link", {
			name: "orders-r1",
		});
		const href = new URL(link.getAttribute("href") ?? "", "http://x");
		expect(href.pathname).toBe("/p/proj-1/prod");
		expect(href.searchParams.get("service")).toBe("r1");
	});

	test("the server message of a replica wins over the state's own sentence", async () => {
		await panel({
			role: "primary",
			replicas: [
				member({
					id: "r1",
					slug: "orders-r1",
					state: "cloning",
					message: "Copied 40% of 2 GiB",
				}),
			],
		});
		await screen.findByRole("list", { name: "Replicas" });
		const row = within(replicaRow("orders-r1"));
		expect(row.getByText("Copying data")).toBeInTheDocument();
		expect(row.getByText("Copied 40% of 2 GiB")).toBeInTheDocument();
		// A replica being built has no lag reading to draw.
		expect(row.queryByRole("progressbar")).not.toBeInTheDocument();
		expect(row.queryByText(/Measured/)).not.toBeInTheDocument();
	});

	test("a reading older than a minute is shown as unknown, not as a fact", async () => {
		await panel({
			role: "primary",
			replicas: [
				member({
					id: "r1",
					slug: "orders-r1",
					state: "streaming",
					lagBytes: MIB,
					observedAt: ago(10 * 60_000),
				}),
			],
		});
		await screen.findByRole("list", { name: "Replicas" });
		const row = within(replicaRow("orders-r1"));
		expect(row.getByText("Unknown")).toBeInTheDocument();
		expect(row.queryByText("Streaming")).not.toBeInTheDocument();
		expect(row.queryByRole("progressbar")).not.toBeInTheDocument();
		expect(
			row.getByText("No recent report from the server."),
		).toBeInTheDocument();
	});

	test("Promote is offered for replicas that stream, lag or are broken, not for ones still being made", async () => {
		await panel({
			role: "primary",
			replicas: [
				member({
					id: "a",
					slug: "r-streaming",
					state: "streaming",
					observedAt: fresh(),
				}),
				member({
					id: "b",
					slug: "r-lagging",
					state: "lagging",
					observedAt: fresh(),
				}),
				member({
					id: "c",
					slug: "r-broken",
					state: "broken",
					observedAt: fresh(),
				}),
				member({ id: "d", slug: "r-cloning", state: "cloning" }),
				member({ id: "e", slug: "r-rebuild", state: "needs_rebuild" }),
			],
		});
		await screen.findByRole("list", { name: "Replicas" });
		const promote = (name: string) =>
			within(replicaRow(name)).getByRole("button", { name: "Promote" });
		expect(promote("r-streaming")).toBeEnabled();
		expect(promote("r-lagging")).toBeEnabled();
		expect(promote("r-broken")).toBeEnabled();
		expect(promote("r-cloning")).toBeDisabled();
		expect(promote("r-rebuild")).toBeDisabled();
		expect(promote("r-cloning")).toHaveAttribute(
			"title",
			"Only a replica that is receiving changes, or a broken one whose primary is gone, can be promoted.",
		);
	});

	test("Rebuild is offered only for broken, needs-rebuild and failed replicas", async () => {
		await panel({
			role: "primary",
			replicas: [
				member({
					id: "a",
					slug: "r-streaming",
					state: "streaming",
					observedAt: fresh(),
				}),
				member({
					id: "b",
					slug: "r-broken",
					state: "broken",
					observedAt: fresh(),
				}),
				member({ id: "c", slug: "r-rebuild", state: "needs_rebuild" }),
				member({
					id: "d",
					slug: "r-failed",
					state: "failed",
					message: "disk full",
				}),
				member({ id: "e", slug: "r-cloning", state: "cloning" }),
			],
		});
		await screen.findByRole("list", { name: "Replicas" });
		const rebuild = (name: string) =>
			within(replicaRow(name)).queryByRole("button", { name: "Rebuild" });
		expect(rebuild("r-streaming")).toBeNull();
		expect(rebuild("r-broken")).toBeInTheDocument();
		expect(rebuild("r-rebuild")).toBeInTheDocument();
		expect(rebuild("r-failed")).toBeInTheDocument();
		expect(rebuild("r-cloning")).toBeNull();
		// A failed saga shows its message and no lag figures.
		const failed = within(replicaRow("r-failed"));
		expect(failed.getByText("Failed")).toBeInTheDocument();
		expect(failed.getByText("disk full")).toBeInTheDocument();
		expect(failed.queryByRole("progressbar")).not.toBeInTheDocument();
	});

	test("a replica that is being removed cannot be removed again", async () => {
		await panel({
			role: "primary",
			replicas: [
				member({ id: "a", slug: "r-gone", state: "removing" }),
				member({
					id: "b",
					slug: "r-ok",
					state: "streaming",
					observedAt: fresh(),
				}),
			],
		});
		await screen.findByRole("list", { name: "Replicas" });
		expect(
			screen.getByRole("button", { name: "Remove r-gone" }),
		).toBeDisabled();
		expect(screen.getByRole("button", { name: "Remove r-ok" })).toBeEnabled();
	});

	test("a primary at the replica limit cannot add another", async () => {
		await panel({
			role: "primary",
			replicas: Array.from({ length: MAX_REPLICAS }, (_, i) =>
				member({ id: `r${i}`, slug: `orders-r${i}`, observedAt: fresh() }),
			),
		});
		const add = await screen.findByRole("button", {
			name: "Add standby replica",
		});
		expect(add).toBeDisabled();
		expect(add).toHaveAttribute(
			"title",
			`A primary has at most ${MAX_REPLICAS} replicas.`,
		);
	});

	test("one fewer than the limit still allows adding", async () => {
		await panel({
			role: "primary",
			replicas: Array.from({ length: MAX_REPLICAS - 1 }, (_, i) =>
				member({ id: `r${i}`, slug: `orders-r${i}`, observedAt: fresh() }),
			),
		});
		expect(
			await screen.findByRole("button", { name: "Add standby replica" }),
		).toBeEnabled();
	});

	test("a promote in progress is announced and locks every Promote", async () => {
		await panel({
			role: "primary",
			promotion: running,
			replicas: [member({ id: "a", slug: "orders-r1", observedAt: fresh() })],
		});
		expect(
			await screen.findByText("Freezing the old primary"),
		).toBeInTheDocument();
		expect(
			within(replicaRow("orders-r1")).getByRole("button", { name: "Promote" }),
		).toBeDisabled();
	});

	test("a failed promote is reported with its message", async () => {
		await panel({
			role: "primary",
			promotion: {
				...running,
				status: "failed",
				message: "Old primary refused to stop",
			},
			replicas: [member({ id: "a", slug: "orders-r1", observedAt: fresh() })],
		});
		expect(
			await screen.findByText("The last promote failed"),
		).toBeInTheDocument();
		expect(screen.getByText("Old primary refused to stop")).toBeInTheDocument();
		// Nothing runs any more, so promoting again is possible.
		expect(
			within(replicaRow("orders-r1")).getByRole("button", { name: "Promote" }),
		).toBeEnabled();
	});

	test("a finished promote is a quiet line, not an alert", async () => {
		await panel({
			role: "primary",
			promotion: {
				...running,
				status: "succeeded",
				message: "",
				mode: "forced",
			},
			replicas: [member({ id: "a", slug: "orders-r1", observedAt: fresh() })],
		});
		expect(
			await screen.findByText("The forced promote finished."),
		).toBeInTheDocument();
		expect(
			screen.queryByText("The last promote failed"),
		).not.toBeInTheDocument();
	});
});

describe("ReplicasPanel actions from the primary's list", () => {
	test("Promote opens the promote dialog for that replica", async () => {
		const user = userEvent.setup();
		const { api } = await panel(
			{
				role: "primary",
				replicas: [
					member({ id: "r1", slug: "orders-r1", observedAt: fresh() }),
				],
			},
			{},
			{
				"POST /services/:id/database/promote": () =>
					Response.json({
						dryRun: true,
						plan: promotePlan({ replica: { id: "r1", slug: "orders-r1" } }),
					}),
			},
		);
		await screen.findByRole("list", { name: "Replicas" });
		await user.click(
			within(replicaRow("orders-r1")).getByRole("button", { name: "Promote" }),
		);
		const dialog = await screen.findByRole("dialog");
		expect(
			within(dialog).getByText("Promote to primary · orders-r1"),
		).toBeInTheDocument();
		await within(dialog).findByText("switchover");
		const planCall = api.calls.find((c) => c.method === "POST");
		expect(planCall?.path).toBe("/services/r1/database/promote");
		expect(planCall?.body).toEqual({ dryRun: true });
		// Closing the dialog leaves the list as it was.
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);
	});

	test("Rebuild asks first, then sends an empty rebuild request and refreshes the view", async () => {
		let key: string | null = null;
		const user = userEvent.setup();
		const { api } = await panel(
			{
				role: "primary",
				replicas: [
					member({
						id: "r1",
						slug: "orders-r1",
						state: "broken",
						observedAt: fresh(),
					}),
				],
			},
			{},
			{
				"POST /services/:id/database/rebuild": (request: Request) => {
					key = request.headers.get("Idempotency-Key");
					return Response.json({});
				},
			},
		);
		await screen.findByRole("list", { name: "Replicas" });
		await user.click(
			within(replicaRow("orders-r1")).getByRole("button", { name: "Rebuild" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(within(dialog).getByText("Rebuild orders-r1?")).toBeInTheDocument();
		expect(
			within(dialog).getByText(/The data is copied again from the primary\./),
		).toBeInTheDocument();
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);

		await user.click(within(dialog).getByRole("button", { name: "Rebuild" }));
		await waitFor(() =>
			expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
		);
		const posts = api.calls.filter((c) => c.method === "POST");
		expect(posts).toHaveLength(1);
		expect(posts[0]).toMatchObject({
			path: "/services/r1/database/rebuild",
			body: {},
		});
		expect(key).toMatch(/^[0-9a-f-]{36}$/);
		expect(
			(await screen.findAllByText("Rebuilding the replica")).length,
		).toBeGreaterThan(0);
		await waitFor(() =>
			expect(
				api.calls.filter(
					(c) => c.method === "GET" && c.path.endsWith("/replication"),
				).length,
			).toBeGreaterThan(1),
		);
	});

	test("a refused rebuild keeps the confirmation open with the reason", async () => {
		const user = userEvent.setup();
		await panel(
			{
				role: "primary",
				replicas: [
					member({
						id: "r1",
						slug: "orders-r1",
						state: "broken",
						observedAt: fresh(),
					}),
				],
			},
			{},
			{
				"POST /services/:id/database/rebuild": problem(
					409,
					"A rebuild is already running.",
					"busy",
				),
			},
		);
		await screen.findByRole("list", { name: "Replicas" });
		await user.click(
			within(replicaRow("orders-r1")).getByRole("button", { name: "Rebuild" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Rebuild" }));
		expect(
			await within(dialog).findByText("A rebuild is already running."),
		).toBeInTheDocument();
		expect(
			screen.queryByText("Rebuilding the replica"),
		).not.toBeInTheDocument();
	});

	test("Remove needs the replica's name typed and a hold, then deletes it", async () => {
		let key: string | null = null;
		const user = userEvent.setup();
		const { api } = await panel(
			{
				role: "primary",
				replicas: [
					member({ id: "r1", slug: "orders-r1", observedAt: fresh() }),
				],
			},
			{},
			{
				"DELETE /services/:id/database/replica": (request: Request) => {
					key = request.headers.get("Idempotency-Key");
					return Response.json({});
				},
			},
		);
		await screen.findByRole("list", { name: "Replicas" });
		await user.click(screen.getByRole("button", { name: "Remove orders-r1" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(within(dialog).getByText("Remove orders-r1?")).toBeInTheDocument();
		expect(
			within(dialog).getByText(/The primary is not touched\./),
		).toBeInTheDocument();
		const key1 = within(dialog).getByRole("button", {
			name: /Hold to remove replica/,
		});

		// A wrong name refuses the hold.
		await user.type(within(dialog).getByRole("textbox"), "orders-r");
		await hold(key1);
		expect(api.calls.filter((c) => c.method === "DELETE")).toEqual([]);

		await user.type(within(dialog).getByRole("textbox"), "1");
		await hold(
			within(dialog).getByRole("button", { name: /Hold to remove replica/ }),
		);
		await waitFor(() =>
			expect(api.calls.filter((c) => c.method === "DELETE")).toHaveLength(1),
		);
		expect(api.calls.find((c) => c.method === "DELETE")?.path).toBe(
			"/services/r1/database/replica",
		);
		expect(key).toMatch(/^[0-9a-f-]{36}$/);
		expect(
			(await screen.findAllByText("Removing the replica")).length,
		).toBeGreaterThan(0);
	});
});

describe("ReplicasPanel replica", () => {
	const self = (state: string, overrides = {}) => ({
		state,
		message: "",
		lagBytes: 2 * MIB,
		observedAt: fresh(),
		...overrides,
	});
	const asReplica = (
		state: string,
		overrides: Parameters<typeof replication>[0] = {},
	) => ({
		role: "replica" as const,
		canPromote: true,
		rebuildable: false,
		primary: { id: "svc-p", slug: "orders-db" },
		self: self(state),
		...overrides,
	});
	const replicaService = postgresService({
		id: "svc-r",
		name: "Orders replica",
		slug: "orders-r1",
	});

	test("describes how the replica follows its primary", async () => {
		await panel(asReplica("streaming"), { service: replicaService });
		expect(await screen.findByText("replica")).toBeInTheDocument();
		const link = screen.getByRole("link", { name: "orders-db" });
		expect(
			new URL(link.getAttribute("href") ?? "", "http://x").searchParams.get(
				"service",
			),
		).toBe("svc-p");
		expect(
			screen.getByText(/It is read-only: writes go to the primary/),
		).toBeInTheDocument();
		expect(screen.getByText("Streaming")).toBeInTheDocument();
		expect(
			screen.getByRole("progressbar", { name: "Replication lag" }),
		).toBeInTheDocument();
		expect(screen.getByText(MANUAL_NOTE)).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Copy writer URL" }),
		).toBeInTheDocument();
		// A replica neither adds replicas nor explains the feature.
		expect(
			screen.queryByRole("button", { name: "Add standby replica" }),
		).toBeNull();
		expect(screen.queryByText(PANEL_PURPOSE)).toBeNull();
		expect(screen.getByText(PANEL_CAVEAT)).toBeInTheDocument();
	});

	test("without a known primary it says 'its primary'", async () => {
		const { primary: _omitted, ...noPrimary } = asReplica("streaming");
		await panel(noPrimary, { service: replicaService });
		expect(
			await screen.findByText(/Follows its primary\./),
		).toBeInTheDocument();
	});

	test("a streaming replica can be promoted and removed but not rebuilt", async () => {
		await panel(asReplica("streaming"), { service: replicaService });
		const promote = await screen.findByRole("button", {
			name: "Promote to primary",
		});
		expect(promote).toBeEnabled();
		const rebuild = screen.getByRole("button", { name: "Rebuild" });
		expect(rebuild).toBeDisabled();
		expect(rebuild).toHaveAttribute(
			"title",
			"A replica that is streaming does not need a rebuild.",
		);
		expect(
			screen.getByRole("button", { name: "Remove replica" }),
		).toBeEnabled();
	});

	test("the control plane can veto Promote even for a streaming replica", async () => {
		await panel(asReplica("streaming", { canPromote: false }), {
			service: replicaService,
		});
		expect(
			await screen.findByRole("button", { name: "Promote to primary" }),
		).toBeDisabled();
	});

	test("a replica that needs a rebuild offers Rebuild and not Promote", async () => {
		await panel(asReplica("needs_rebuild", { self: self("needs_rebuild") }), {
			service: replicaService,
		});
		expect(
			await screen.findByRole("button", { name: "Rebuild" }),
		).toBeEnabled();
		const promote = screen.getByRole("button", { name: "Promote to primary" });
		expect(promote).toBeDisabled();
		expect(promote).toHaveAttribute(
			"title",
			expect.stringContaining("Only a replica that is receiving changes"),
		);
	});

	test("a replica reading older than a minute counts as unknown", async () => {
		await panel(
			asReplica("streaming", {
				self: self("streaming", { observedAt: ago(5 * 60_000) }),
			}),
			{ service: replicaService },
		);
		expect(await screen.findByText("Unknown")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Promote to primary" }),
		).toBeDisabled();
		expect(screen.queryByRole("progressbar")).toBeNull();
	});

	test("Promote opens the dialog for this database", async () => {
		const user = userEvent.setup();
		await panel(
			asReplica("streaming"),
			{ service: replicaService },
			{
				"POST /services/:id/database/promote": () =>
					Response.json({ dryRun: true, plan: promotePlan({ lagBytes: 0 }) }),
			},
		);
		await user.click(
			await screen.findByRole("button", { name: "Promote to primary" }),
		);
		const dialog = await screen.findByRole("dialog");
		expect(
			within(dialog).getByText("Promote to primary · Orders replica"),
		).toBeInTheDocument();
		expect(within(dialog).getByLabelText(/to confirm/)).toBeInTheDocument();
	});

	test("Rebuild of this replica posts to its own service id", async () => {
		const user = userEvent.setup();
		const { api } = await panel(
			asReplica("broken"),
			{ service: replicaService },
			{ "POST /services/:id/database/rebuild": rebuilt },
		);
		await user.click(await screen.findByRole("button", { name: "Rebuild" }));
		const dialog = await screen.findByRole("alertdialog");
		expect(within(dialog).getByText("Rebuild orders-r1?")).toBeInTheDocument();
		await user.click(within(dialog).getByRole("button", { name: "Rebuild" }));
		await waitFor(() =>
			expect(api.calls.some((c) => c.method === "POST")).toBe(true),
		);
		expect(api.calls.find((c) => c.method === "POST")).toMatchObject({
			path: "/services/svc-r/database/rebuild",
			body: {},
		});
	});

	test("Remove replica deletes this service after the slug is typed", async () => {
		const user = userEvent.setup();
		const { api } = await panel(
			asReplica("streaming"),
			{ service: replicaService },
			{ "DELETE /services/:id/database/replica": removed },
		);
		await user.click(
			await screen.findByRole("button", { name: "Remove replica" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.type(within(dialog).getByRole("textbox"), "orders-r1");
		await hold(
			within(dialog).getByRole("button", { name: /Hold to remove replica/ }),
		);
		await waitFor(() =>
			expect(api.calls.some((c) => c.method === "DELETE")).toBe(true),
		);
		expect(api.calls.find((c) => c.method === "DELETE")?.path).toBe(
			"/services/svc-r/database/replica",
		);
	});
});

describe("ReplicasPanel fenced old primary", () => {
	test("explains that it is fenced and offers to rebuild it behind the new primary", async () => {
		const user = userEvent.setup();
		const { api } = await panel(
			{
				role: "fenced",
				rebuildable: true,
				primary: { id: "svc-new", slug: "orders-r1" },
			},
			{},
			{ "POST /services/:id/database/rebuild": rebuilt },
		);
		expect(await screen.findByText("fenced")).toBeInTheDocument();
		expect(screen.getByText("Fenced")).toBeInTheDocument();
		expect(
			screen.getByText(
				/A replica \(orders-r1\) was promoted in place of this database\./,
			),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Add standby replica" }),
		).toBeNull();
		expect(screen.queryByText(PANEL_PURPOSE)).toBeNull();

		await user.click(
			screen.getByRole("button", { name: "Rebuild as a replica of orders-r1" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText(/copied from the new primary onto a new volume/),
		).toBeInTheDocument();
		await user.click(within(dialog).getByRole("button", { name: "Rebuild" }));
		await waitFor(() =>
			expect(api.calls.some((c) => c.method === "POST")).toBe(true),
		);
		expect(api.calls.find((c) => c.method === "POST")).toMatchObject({
			path: "/services/svc-primary/database/rebuild",
			body: { followServiceId: "svc-new" },
		});
	});

	test("without a known new primary there is nothing to rebuild behind", async () => {
		await panel({ role: "fenced", rebuildable: true });
		expect(await screen.findByText("Fenced")).toBeInTheDocument();
		expect(
			screen.queryByText(/was promoted in place of this database/),
		).toBeNull();
		expect(
			screen.queryByRole("button", { name: /Rebuild as a replica/ }),
		).toBeNull();
	});

	test("a fenced database that cannot be rebuilt offers no rebuild", async () => {
		await panel({
			role: "fenced",
			rebuildable: false,
			primary: { id: "svc-new", slug: "orders-r1" },
		});
		expect(await screen.findByText("Fenced")).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: /Rebuild as a replica/ }),
		).toBeNull();
	});
});
