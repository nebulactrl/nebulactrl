import { describe, expect, spyOn, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { DatabaseView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, type Overrides } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { databaseView, postgres, problem, service } from "./database.fixture";
import { DatabaseSection } from "./database-section";
import { ENGINE_CATALOG } from "./engines.fixture";

// Builds a literal `${NAME}` placeholder (the escaped `$` keeps it from interpolating).
const ref = (name: string) => `\${${name}}`;

const VIEW = "GET /services/:id/database";
const REFRESH = "POST /services/:id/database/refresh";
const REPLICATION = "GET /services/:id/database/replication";

const mysql = ENGINE_CATALOG.find((engine) => engine.id === "mysql");
if (!mysql) throw new Error("fixture catalog has no mysql");

/** A MySQL 8.4 database (no replicas panel), which can be upgraded to 9.7. */
const mysqlView = (extra: Overrides<DatabaseView> = {}) =>
	databaseView({
		engine: mysql,
		spec: { engine: "mysql", version: "8.4" },
		image: "mysql:8.4",
		...extra,
	});

const info = (extra: Record<string, unknown> = {}) => ({
	connections: 12,
	sizeBytes: 1610612736,
	updatedAt: "2026-03-01T10:00:00Z",
	version: "8.4.3",
	...extra,
});

const notManaged = problem(
	404,
	"not a managed database",
	"not_a_managed_database",
);

const upgradeRow = (status: "pending" | "ready" | "failed", extra = {}) => ({
	id: "up-1",
	createdAt: "2026-01-01T00:00:00Z",
	fromVersion: "8.4",
	toVersion: "9.7",
	method: "pg_upgrade",
	newVolume: "vol-new",
	oldVolume: "vol-old",
	status,
	...extra,
});

const rotationRow = (status: "pending" | "applied" | "failed", extra = {}) => ({
	id: "rot-1",
	createdAt: "2026-01-01T00:00:00Z",
	mode: "exec",
	status,
	...extra,
});

async function mount(
	view: DatabaseView | ReturnType<typeof problem>,
	options: {
		svc?: ReturnType<typeof service>;
		extra?: Parameters<typeof stubApi>[0];
		onVariables?: () => void;
	} = {},
) {
	const api = stubApi({
		[VIEW]: "body" in view && "status" in view ? view : { body: view },
		...options.extra,
	});
	await renderWithProviders(
		<DatabaseSection
			service={options.svc ?? service()}
			onOpenConnectionVariables={options.onVariables ?? (() => {})}
		/>,
	);
	return api;
}

describe("DatabaseSection", () => {
	test("shows a placeholder while the details load", async () => {
		stubApi({ [VIEW]: () => new Promise<Response>(() => {}) });
		await renderWithProviders(
			<DatabaseSection
				service={service()}
				onOpenConnectionVariables={() => {}}
			/>,
		);
		expect(
			screen.getByRole("status", { name: "Loading the database" }),
		).toBeInTheDocument();
		expect(screen.queryByText("Rotate credentials")).not.toBeInTheDocument();
	});

	test("a load error is shown with a Retry that recovers", async () => {
		let attempts = 0;
		stubApi({
			[VIEW]: () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{
								status: 500,
								title: "x",
								detail: "agent gateway down",
								code: "x",
							},
							{ status: 500 },
						)
					: Response.json(mysqlView({ info: info() }));
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<DatabaseSection
				service={service()}
				onOpenConnectionVariables={() => {}}
			/>,
		);
		expect(
			await screen.findByText("The database details did not load"),
		).toBeInTheDocument();
		expect(screen.getByText(/agent gateway down/)).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Retry" }));
		expect(await screen.findByText("MySQL 8.4 LTS")).toBeInTheDocument();
		expect(
			screen.queryByText("The database details did not load"),
		).not.toBeInTheDocument();
	});

	test("a service without a managed spec renders nothing", async () => {
		const api = stubApi({ [VIEW]: notManaged });
		const { container } = await renderWithProviders(
			<DatabaseSection
				service={service()}
				onOpenConnectionVariables={() => {}}
			/>,
		);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		await waitFor(() =>
			expect(screen.queryByRole("status")).not.toBeInTheDocument(),
		);
		expect(screen.queryByRole("region", { name: "Database" })).toBeNull();
		expect(screen.queryByText("The database details did not load")).toBeNull();
		expect(container.querySelector("section")).toBeNull();
	});

	test("a running database shows its version, image, measurements and primary export", async () => {
		await mount(mysqlView({ info: info() }));
		const section = await screen.findByRole("region", { name: "Database" });
		expect(within(section).getByText("MySQL 8.4 LTS")).toBeVisible();
		expect(within(section).getByText("mysql:8.4")).toBeVisible();
		expect(within(section).getByText("1.5 GiB")).toBeVisible();
		expect(within(section).getByText("12")).toBeVisible();
		expect(within(section).getByText("8.4.3")).toBeVisible();
		expect(within(section).getByText("DATABASE_URL")).toBeVisible();
		expect(within(section).getByText("secret")).toBeVisible();
		expect(
			within(section).getByText(ref("{svc-db.DATABASE_URL}")),
		).toBeVisible();
		expect(
			within(section).getByRole("button", {
				name: "Copy reference DATABASE_URL",
			}),
		).toBeEnabled();
		expect(
			within(section).getByRole("button", { name: "Copy connection string" }),
		).toBeEnabled();
		expect(within(section).queryByText("No sample yet.")).toBeNull();
		expect(within(section).queryByText(/Asking the agent/)).toBeNull();
	});

	test("a postgres flavor is part of the version line", async () => {
		stubApi({
			[VIEW]: {
				body: databaseView({
					spec: { engine: "postgres", version: "17", flavor: "postgis" },
					info: info(),
				}),
			},
			[REPLICATION]: problem(404, "no replicas api", "not_found"),
		});
		await renderWithProviders(
			<DatabaseSection
				service={service()}
				onOpenConnectionVariables={() => {}}
			/>,
		);
		expect(await screen.findByText("PostgreSQL 17 · PostGIS")).toBeVisible();
	});

	test("without a sample the figures are dashes and it says so", async () => {
		await mount(mysqlView());
		expect(await screen.findByText("No sample yet.")).toBeVisible();
		expect(screen.getAllByText("—")).toHaveLength(3);
	});

	test("a failed measurement is reported and its numbers are not shown", async () => {
		await mount(
			mysqlView({
				info: info({ error: "docker stats timed out", sizeBytes: 999 }),
			}),
		);
		expect(
			await screen.findByText(
				"The last measurement failed: docker stats timed out",
			),
		).toBeVisible();
		expect(screen.queryByText("999 B")).not.toBeInTheDocument();
		expect(screen.getAllByText("—").length).toBeGreaterThanOrEqual(2);
	});

	test("an undeployed database cannot be refreshed and says why", async () => {
		await mount(mysqlView({ deployed: false }));
		expect(
			await screen.findByText(
				"Not deployed yet: deploy it to read its size and connections.",
			),
		).toBeVisible();
		expect(
			screen.getByRole("button", { name: /Refresh the measurements/ }),
		).toBeDisabled();
	});

	test("a stopped database cannot be refreshed and says why", async () => {
		await mount(mysqlView({ info: info() }), {
			svc: service({ desiredState: "stopped" }),
		});
		expect(
			await screen.findByText(
				"Stopped: start it to read its size and connections.",
			),
		).toBeVisible();
		expect(
			screen.getByRole("button", { name: /Refresh the measurements/ }),
		).toBeDisabled();
	});

	test("Refresh asks the agent and waits until the measurement changes", async () => {
		let refreshed = false;
		const api = stubApi({
			[VIEW]: () =>
				Response.json(
					mysqlView({
						info: refreshed
							? info({ updatedAt: "2026-03-01T10:05:00Z", connections: 31 })
							: info(),
					}),
				),
			[REFRESH]: () => {
				refreshed = true;
				return new Response(null, { status: 202 });
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<DatabaseSection
				service={service()}
				onOpenConnectionVariables={() => {}}
			/>,
		);
		expect(await screen.findByText("12")).toBeVisible();
		await user.click(
			screen.getByRole("button", { name: /Refresh the measurements/ }),
		);

		// The refetch after the 202 brings the newer sample: no longer waiting.
		expect(await screen.findByText("31")).toBeVisible();
		const post = api.calls.find((call) => call.method === "POST");
		expect(post?.path).toBe("/services/svc-db/database/refresh");
		await waitFor(() =>
			expect(screen.queryByText("Asking the agent…")).not.toBeInTheDocument(),
		);
		expect(
			screen.getByRole("button", { name: /Refresh the measurements/ }),
		).toBeEnabled();
	});

	test("while the agent has not answered the refresh the note says so and the button is locked", async () => {
		stubApi({
			[VIEW]: { body: mysqlView({ info: info() }) },
			[REFRESH]: { status: 202, body: make("RefreshResult") },
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<DatabaseSection
				service={service()}
				onOpenConnectionVariables={() => {}}
			/>,
		);
		await screen.findByText("12");
		await user.click(
			screen.getByRole("button", { name: /Refresh the measurements/ }),
		);
		expect(await screen.findByText("Asking the agent…")).toBeVisible();
		expect(
			screen.getByRole("button", { name: /Refresh the measurements/ }),
		).toBeDisabled();
	});

	test("an unanswered refresh is called stale after twenty seconds", async () => {
		stubApi({
			[VIEW]: { body: mysqlView({ info: info() }) },
			[REFRESH]: { status: 202, body: make("RefreshResult") },
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<DatabaseSection
				service={service()}
				onOpenConnectionVariables={() => {}}
			/>,
		);
		await screen.findByText("12");
		await user.click(
			screen.getByRole("button", { name: /Refresh the measurements/ }),
		);
		await screen.findByText("Asking the agent…");

		const realNow = Date.now();
		const clock = spyOn(Date, "now").mockReturnValue(realNow + 25_000);
		try {
			// The section re-reads the clock on its two-second tick.
			expect(
				await screen.findByText(
					"No answer yet: the numbers above may be stale.",
					{},
					{ timeout: 8000 },
				),
			).toBeVisible();
		} finally {
			clock.mockRestore();
		}
		expect(screen.queryByText("Asking the agent…")).not.toBeInTheDocument();
	});

	test("a refused refresh toasts the reason and unlocks the button", async () => {
		stubApi({
			[VIEW]: { body: mysqlView({ info: info() }) },
			[REFRESH]: problem(409, "the agent is offline", "conflict"),
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<DatabaseSection
				service={service()}
				onOpenConnectionVariables={() => {}}
			/>,
		);
		await screen.findByText("12");
		await user.click(
			screen.getByRole("button", { name: /Refresh the measurements/ }),
		);
		expect(await screen.findByText("Could not refresh")).toBeVisible();
		expect(screen.getByText("the agent is offline")).toBeVisible();
		expect(screen.queryByText("Asking the agent…")).not.toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: /Refresh the measurements/ }),
		).toBeEnabled();
	});

	test("a database with no exports shows no connection actions", async () => {
		await mount(mysqlView({ exports: null, info: info() }));
		await screen.findByText("MySQL 8.4 LTS");
		expect(
			screen.queryByRole("button", { name: "Rotate credentials" }),
		).toBeNull();
		expect(
			screen.queryByRole("button", { name: "Upgrade version" }),
		).toBeNull();
		expect(
			screen.queryByRole("button", { name: "Copy connection string" }),
		).toBeNull();
	});

	test("the first export is the primary one when none is marked", async () => {
		await mount(
			mysqlView({
				info: info(),
				exports: [
					{
						key: "HOST",
						description: "",
						primary: false,
						secret: false,
						reference: ref("{x.HOST}"),
					},
					{
						key: "URL",
						description: "",
						primary: false,
						secret: false,
						reference: ref("{x.URL}"),
					},
				],
			}),
		);
		expect(await screen.findByText("HOST")).toBeVisible();
		expect(screen.queryByText("secret")).not.toBeInTheDocument();
		expect(screen.queryByText("URL")).not.toBeInTheDocument();
	});

	test("All connection variables opens the variables tab", async () => {
		let opened = 0;
		const user = userEvent.setup();
		await mount(mysqlView({ info: info() }), {
			onVariables: () => (opened += 1),
		});
		await user.click(
			await screen.findByRole("button", { name: /All connection variables/ }),
		);
		expect(opened).toBe(1);
	});

	describe("upgrade and rotation state", () => {
		test("a running upgrade is announced and locks both actions", async () => {
			await mount(mysqlView({ info: info(), upgrade: upgradeRow("pending") }));
			expect(
				await screen.findByText(
					/Upgrading MySQL 8\.4 to 9\.7\. The database is stopped/,
				),
			).toBeVisible();
			expect(
				screen.getByRole("button", { name: "Rotate credentials" }),
			).toBeDisabled();
			expect(
				screen.getByRole("button", { name: "Upgrade version" }),
			).toBeDisabled();
		});

		test("a ready upgrade with staged changes asks for a deploy", async () => {
			await mount(mysqlView({ info: info(), upgrade: upgradeRow("ready") }), {
				svc: service({ hasStagedChanges: true }),
			});
			expect(await screen.findByText("Upgrade ready")).toBeVisible();
			expect(
				screen.getByText(
					"Deploy to start MySQL 9.7; the previous volume “vol-old” is kept.",
				),
			).toBeVisible();
		});

		test("a ready upgrade that was deployed reads as done", async () => {
			await mount(mysqlView({ info: info(), upgrade: upgradeRow("ready") }), {
				svc: service({ hasStagedChanges: false }),
			});
			expect(
				await screen.findByText(
					"Upgraded from 8.4 to 9.7. The previous volume “vol-old” is kept.",
				),
			).toBeVisible();
			expect(screen.queryByText("Upgrade ready")).not.toBeInTheDocument();
		});

		test("a failed upgrade shows its reason", async () => {
			await mount(
				mysqlView({
					info: info(),
					upgrade: upgradeRow("failed", { error: "disk full" }),
				}),
			);
			expect(await screen.findByText("Upgrade failed")).toBeVisible();
			expect(
				screen.getByText("The upgrade to 9.7 failed: disk full"),
			).toBeVisible();
			// A failed upgrade does not lock the actions.
			expect(
				screen.getByRole("button", { name: "Rotate credentials" }),
			).toBeEnabled();
		});

		test("a running rotation is announced and locks both actions", async () => {
			await mount(
				mysqlView({ info: info(), rotation: rotationRow("pending") }),
			);
			expect(
				await screen.findByText(
					"Rotating credentials: the agent is changing the password.",
				),
			).toBeVisible();
			expect(
				screen.getByRole("button", { name: "Rotate credentials" }),
			).toBeDisabled();
			expect(
				screen.getByRole("button", { name: "Upgrade version" }),
			).toBeDisabled();
		});

		test("a failed rotation shows its reason", async () => {
			await mount(
				mysqlView({
					info: info(),
					rotation: rotationRow("failed", { error: "role locked" }),
				}),
			);
			expect(await screen.findByText("Rotation failed")).toBeVisible();
			expect(
				screen.getByText("The last rotation failed: role locked"),
			).toBeVisible();
		});

		test("an applied rotation says when it happened", async () => {
			await mount(
				mysqlView({
					info: info(),
					rotation: rotationRow("applied", {
						finishedAt: new Date().toISOString(),
					}),
				}),
			);
			expect(await screen.findByText(/Credentials last rotated/)).toBeVisible();
			expect(screen.getByRole("time")).toBeInTheDocument();
		});

		test("an applied rotation without a finish time says nothing", async () => {
			await mount(
				mysqlView({ info: info(), rotation: rotationRow("applied") }),
			);
			await screen.findByText("MySQL 8.4 LTS");
			expect(
				screen.queryByText(/Credentials last rotated/),
			).not.toBeInTheDocument();
		});
	});

	describe("actions", () => {
		test("Rotate credentials opens the rotation plan", async () => {
			const api = stubApi({
				[VIEW]: { body: mysqlView({ info: info() }) },
				"POST /services/:id/database/rotate-credentials": {
					body: {
						dryRun: true,
						status: "planned",
						plan: {
							mode: "exec",
							passwordKey: "P",
							databaseRestart: false,
							consumers: [{ name: "web", serviceId: "s", slug: "web-slug" }],
							warnings: null,
							blockers: null,
						},
					},
				},
			});
			const user = userEvent.setup();
			await renderWithProviders(
				<DatabaseSection
					service={service({ name: "orders" })}
					onOpenConnectionVariables={() => {}}
				/>,
			);
			await user.click(
				await screen.findByRole("button", { name: "Rotate credentials" }),
			);
			expect(
				await screen.findByText("Rotate credentials · orders"),
			).toBeVisible();
			expect(await screen.findByText("web-slug")).toBeVisible();
			expect(api.calls.at(-1)?.body).toEqual({ dryRun: true });
		});

		test("Upgrade version opens the upgrade plan for the next version", async () => {
			const api = stubApi({
				[VIEW]: { body: mysqlView({ info: info() }) },
				"POST /services/:id/database/upgrade/plan": {
					body: {
						engine: "mysql",
						from: "8.4",
						to: "9.7",
						kind: "upgrade",
						method: "redeploy",
						executable: true,
						downtime: "a minute",
						steps: null,
						warnings: null,
						blockers: null,
					},
				},
			});
			const user = userEvent.setup();
			await renderWithProviders(
				<DatabaseSection
					service={service({ name: "orders" })}
					onOpenConnectionVariables={() => {}}
				/>,
			);
			await user.click(
				await screen.findByRole("button", { name: "Upgrade version" }),
			);
			expect(await screen.findByText("Upgrade version · orders")).toBeVisible();
			expect(
				await screen.findByText("Redeploy with the new image"),
			).toBeVisible();
			expect(api.calls.at(-1)?.body).toEqual({ targetVersion: "9.7" });
		});

		test("the newest version cannot be upgraded and the button says why", async () => {
			await mount(
				mysqlView({
					info: info(),
					spec: { engine: "mysql", version: "9.7" },
				}),
			);
			const button = await screen.findByRole("button", {
				name: "Upgrade version",
			});
			expect(button).toBeDisabled();
			expect(button).toHaveAttribute(
				"title",
				"MySQL 9.7 is the newest version offered.",
			);
			expect(
				screen.getByRole("button", { name: "Rotate credentials" }),
			).toBeEnabled();
		});
	});

	describe("a postgres database in a replica group", () => {
		const replication = (role: string) => ({
			body: { role, canPromote: false, rebuildable: false, replicas: null },
		});
		const postgresView = () =>
			databaseView({
				engine: postgres,
				spec: { engine: "postgres", version: "16" },
				info: info(),
			});

		test("a replica uses its primary's credentials and cannot be upgraded", async () => {
			stubApi({
				[VIEW]: { body: postgresView() },
				[REPLICATION]: replication("replica"),
			});
			await renderWithProviders(
				<DatabaseSection
					service={service()}
					onOpenConnectionVariables={() => {}}
				/>,
			);
			const rotate = await screen.findByTitle(
				"A read replica uses its primary's credentials: rotate them on the primary.",
				{},
				{ timeout: 8000 },
			);
			expect(rotate).toBeDisabled();
			expect(rotate).toHaveTextContent("Rotate credentials");
			const upgrade = screen.getByTitle(
				"Remove the read replicas first: a major upgrade does not carry them over.",
			);
			expect(upgrade).toBeDisabled();
			expect(upgrade).toHaveTextContent("Upgrade version");
		});

		test("a primary can rotate but cannot be upgraded while it has replicas", async () => {
			stubApi({
				[VIEW]: { body: postgresView() },
				[REPLICATION]: replication("primary"),
			});
			await renderWithProviders(
				<DatabaseSection
					service={service()}
					onOpenConnectionVariables={() => {}}
				/>,
			);
			const upgrade = await screen.findByTitle(
				"Remove the read replicas first: a major upgrade does not carry them over.",
				{},
				{ timeout: 8000 },
			);
			expect(upgrade).toBeDisabled();
			expect(
				screen.getByRole("button", { name: "Rotate credentials" }),
			).toBeEnabled();
		});

		test("a standalone postgres can do both", async () => {
			stubApi({
				[VIEW]: { body: postgresView() },
				[REPLICATION]: replication("standalone"),
			});
			await renderWithProviders(
				<DatabaseSection
					service={service()}
					onOpenConnectionVariables={() => {}}
				/>,
			);
			expect(
				await screen.findByRole("button", { name: "Upgrade version" }),
			).toBeEnabled();
			expect(
				screen.getByRole("button", { name: "Rotate credentials" }),
			).toBeEnabled();
		});

		test("the replica panel is tucked into a disclosure", async () => {
			stubApi({
				[VIEW]: { body: postgresView() },
				[REPLICATION]: replication("standalone"),
			});
			await renderWithProviders(
				<DatabaseSection
					service={service()}
					onOpenConnectionVariables={() => {}}
				/>,
			);
			expect(
				await screen.findByRole("button", { name: /Advanced/ }),
			).toBeVisible();
		});
	});

	test("other engines do not ask for replication", async () => {
		const api = await mount(mysqlView({ info: info() }));
		await screen.findByText("MySQL 8.4 LTS");
		expect(api.calls.map((call) => call.path)).not.toContain(
			"/services/svc-db/database/replication",
		);
		expect(screen.queryByRole("button", { name: /Advanced/ })).toBeNull();
	});
});
