import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { InspectorContext } from "#/features/canvas/inspector/inspector-context";
import type { InspectorTab } from "#/features/canvas/inspector/tab-ids";
import {
	type OpenReviewOptions,
	onOpenStagedReview,
} from "#/features/deploy-button/open-review";
import { type ApiCall, stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { databaseView, problem } from "./database.fixture";
import { UpgradeDialog } from "./upgrade-dialog";

const service = { id: "svc-db", name: "orders-db", slug: "orders-db-x7" };
const PLAN = "POST /services/:id/database/upgrade/plan";
const UPGRADE = "POST /services/:id/database/upgrade";
const VIEW = "GET /services/:id/database";

interface PlanOverrides {
	kind?: string;
	method?: string;
	executable?: boolean;
	downtime?: string;
	steps?: string[] | null;
	warnings?: string[] | null;
	blockers?: string[] | null;
}

/** An UpgradePlan to `to`, an executable pg_upgrade unless overridden. */
const planFor = (to: string, over: PlanOverrides = {}) => ({
	engine: "postgres",
	from: "16",
	to,
	kind: "upgrade",
	method: "pg_upgrade",
	executable: true,
	downtime: "about a minute",
	steps: null,
	warnings: null,
	blockers: null,
	...over,
});

/** Answers the plan call for whatever target was asked for. */
function planRoute(over: PlanOverrides | ((to: string) => PlanOverrides) = {}) {
	return async (request: Request) => {
		const body: { targetVersion: string } = await request.clone().json();
		const extra = typeof over === "function" ? over(body.targetVersion) : over;
		return Response.json(planFor(body.targetVersion, extra));
	};
}

const bodyOf = (call: ApiCall) => call.body;
const isUpgrade = (call: ApiCall) =>
	call.path === "/services/svc-db/database/upgrade";

interface Opened {
	closed: boolean[];
	tabs: InspectorTab[];
}

async function open(database = databaseView()) {
	const seen: Opened = { closed: [], tabs: [] };
	await renderWithProviders(
		<InspectorContext.Provider
			value={{ close: () => {}, openTab: (tab) => seen.tabs.push(tab) }}
		>
			<UpgradeDialog
				service={service}
				database={database}
				open
				onOpenChange={(next) => seen.closed.push(next)}
			/>
		</InspectorContext.Provider>,
	);
	return seen;
}

const confirmField = () => screen.findByLabelText(/to confirm/);
const upgradeButton = (label: string) =>
	screen.getByRole("button", { name: `Upgrade to ${label}` });

describe("UpgradeDialog", () => {
	test("offers only newer versions and plans the first one", async () => {
		const api = stubApi({ [PLAN]: planRoute() });
		const user = userEvent.setup();
		await open();

		expect(
			await screen.findByText("Upgrade version · orders-db"),
		).toBeInTheDocument();
		expect(screen.getByText(/PostgreSQL 16 is running\./)).toBeInTheDocument();
		const target = screen.getByRole("combobox", { name: "Upgrade to" });
		await waitFor(() => expect(target).toHaveTextContent("PostgreSQL 17"));
		expect(api.calls[0]?.path).toBe("/services/svc-db/database/upgrade/plan");
		expect(api.calls[0]?.body).toEqual({ targetVersion: "17" });

		await user.click(target);
		expect(
			await screen.findByRole("option", { name: "PostgreSQL 18" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("option", { name: "PostgreSQL 17" }),
		).toBeInTheDocument();
		// 16 itself (and anything older) is not an upgrade.
		expect(
			screen.queryByRole("option", { name: "PostgreSQL 16" }),
		).not.toBeInTheDocument();
	});

	test("a flavor only offers the versions its image has", async () => {
		stubApi({ [PLAN]: planRoute() });
		const user = userEvent.setup();
		await open(
			databaseView({
				spec: { engine: "postgres", version: "16", flavor: "postgis" },
			}),
		);
		expect(
			screen.getByText(/PostgreSQL 16 \(postgis\) is running\./),
		).toBeVisible();
		await user.click(screen.getByRole("combobox", { name: "Upgrade to" }));
		expect(
			await screen.findByRole("option", { name: "PostgreSQL 17" }),
		).toBeInTheDocument();
		// PostGIS is not built for 18.
		expect(
			screen.queryByRole("option", { name: "PostgreSQL 18" }),
		).not.toBeInTheDocument();
	});

	test("on the newest version there is nothing to pick or confirm", async () => {
		const api = stubApi({ [PLAN]: planRoute() });
		const user = userEvent.setup();
		const seen = await open(
			databaseView({ spec: { engine: "postgres", version: "18" } }),
		);
		expect(
			await screen.findByText(
				"PostgreSQL 18 is the newest version offered for this database.",
			),
		).toBeInTheDocument();
		expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
		expect(screen.queryByText(/^Upgrade to /)).not.toBeInTheDocument();
		expect(screen.queryByRole("status")).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(seen.closed).toEqual([false]);
		expect(api.calls).toEqual([]);
	});

	test("shows the plan: method, downtime, steps, warnings, and the version's notes", async () => {
		stubApi({
			[PLAN]: planRoute((to) => ({
				steps: ["Stop the database", `Convert the data to ${to}`],
				warnings: ["Extensions must be reinstalled."],
			})),
		});
		const user = userEvent.setup();
		await open();
		expect(
			await screen.findByText("pg_upgrade into a new volume"),
		).toBeVisible();
		expect(screen.getByText("· downtime: about a minute")).toBeVisible();
		expect(screen.getByText("upgrade")).toBeVisible();
		expect(screen.getByText("Stop the database")).toBeVisible();
		expect(screen.getByText("Convert the data to 17")).toBeVisible();
		expect(screen.getByText("Before you upgrade")).toBeVisible();
		expect(screen.getByText("Extensions must be reinstalled.")).toBeVisible();
		// 17 has no notes; 18 does.
		expect(screen.queryByText(/New on-disk layout/)).not.toBeInTheDocument();

		await user.click(screen.getByRole("combobox", { name: "Upgrade to" }));
		await user.click(
			await screen.findByRole("option", { name: "PostgreSQL 18" }),
		);
		expect(await screen.findByText(/New on-disk layout/)).toBeVisible();
		expect(await screen.findByText("Convert the data to 18")).toBeVisible();
	});

	test("picking another version plans again for it and clears the typed confirmation", async () => {
		const api = stubApi({ [PLAN]: planRoute() });
		const user = userEvent.setup();
		await open();
		const field = await confirmField();
		await user.type(field, "orders-db-x7");
		expect(upgradeButton("PostgreSQL 17")).toBeEnabled();

		await user.click(screen.getByRole("combobox", { name: "Upgrade to" }));
		await user.click(
			await screen.findByRole("option", { name: "PostgreSQL 18" }),
		);

		await waitFor(() =>
			expect(api.calls.map(bodyOf)).toEqual([
				{ targetVersion: "17" },
				{ targetVersion: "18" },
			]),
		);
		await waitFor(() => expect(upgradeButton("PostgreSQL 18")).toBeDisabled());
		expect(await confirmField()).toHaveValue("");
	});

	test("shows a placeholder until the plan arrives, with confirm locked", async () => {
		stubApi({ [PLAN]: () => new Promise<Response>(() => {}) });
		await open();
		expect(
			await screen.findByRole("status", { name: "Planning the upgrade" }),
		).toBeInTheDocument();
		expect(upgradeButton("PostgreSQL 17")).toBeDisabled();
		expect(screen.queryByLabelText(/to confirm/)).not.toBeInTheDocument();
	});

	test("a plan that fails shows the reason and offers no confirmation", async () => {
		stubApi({
			[PLAN]: problem(422, "no image for that version", "unprocessable"),
		});
		await open();
		expect(
			await screen.findByText("The upgrade could not be planned"),
		).toBeInTheDocument();
		expect(screen.getByText("no image for that version")).toBeVisible();
		expect(screen.queryByLabelText(/to confirm/)).not.toBeInTheDocument();
		expect(upgradeButton("PostgreSQL 17")).toBeDisabled();
		expect(
			screen.queryByRole("status", { name: "Planning the upgrade" }),
		).not.toBeInTheDocument();
	});

	test("confirming needs the slug typed exactly", async () => {
		const api = stubApi({ [PLAN]: planRoute() });
		const user = userEvent.setup();
		await open();
		const field = await confirmField();
		expect(upgradeButton("PostgreSQL 17")).toBeDisabled();

		await user.type(field, "orders-db");
		expect(upgradeButton("PostgreSQL 17")).toBeDisabled();
		await user.type(field, "-X7");
		expect(upgradeButton("PostgreSQL 17")).toBeDisabled();
		await user.clear(field);
		await user.type(field, "orders-db-x7");
		expect(upgradeButton("PostgreSQL 17")).toBeEnabled();
		expect(api.calls.filter(isUpgrade)).toEqual([]);
	});

	test("blockers lock the upgrade even with the slug typed", async () => {
		stubApi({
			[PLAN]: planRoute({ blockers: ["A backup is running."] }),
		});
		const user = userEvent.setup();
		await open();
		expect(
			await screen.findByText("It cannot be upgraded right now"),
		).toBeVisible();
		expect(screen.getByText("A backup is running.")).toBeVisible();
		await user.type(await confirmField(), "orders-db-x7");
		expect(upgradeButton("PostgreSQL 17")).toBeDisabled();
	});

	test("a staged upgrade sends the target and the typed slug, then offers the staged review", async () => {
		const opened: OpenReviewOptions[] = [];
		const stop = onOpenStagedReview((options) => opened.push(options));
		const api = stubApi({
			[PLAN]: planRoute({ method: "redeploy" }),
			[UPGRADE]: {
				status: 200,
				body: { status: "staged", plan: planFor("17", { method: "redeploy" }) },
			},
		});
		const user = userEvent.setup();
		const seen = await open();
		await user.type(await confirmField(), "orders-db-x7");
		await user.click(upgradeButton("PostgreSQL 17"));

		expect(await screen.findByText("Upgrade staged")).toBeVisible();
		expect(screen.getByText(/Deploy to start PostgreSQL 17/)).toBeVisible();
		// A redeploy has no old volume to mention.
		expect(screen.queryByText(/previous volume/)).not.toBeInTheDocument();
		const call = api.calls.find(isUpgrade);
		expect(call?.method).toBe("POST");
		expect(call?.body).toEqual({
			targetVersion: "17",
			confirm: "orders-db-x7",
		});
		// The picker locks once started.
		expect(screen.getByRole("combobox", { name: "Upgrade to" })).toBeDisabled();

		await user.click(
			screen.getByRole("button", { name: "Review staged changes" }),
		);
		stop();
		expect(opened).toHaveLength(1);
		expect(seen.closed).toEqual([false]);
	});

	test("Enter in the confirmation field confirms once the slug matches", async () => {
		const api = stubApi({
			[PLAN]: planRoute(),
			[UPGRADE]: {
				body: { status: "staged", plan: planFor("17") },
			},
		});
		const user = userEvent.setup();
		await open();
		const field = await confirmField();
		await user.type(field, "wrong{Enter}");
		expect(api.calls.filter(isUpgrade)).toEqual([]);
		await user.clear(field);
		await user.type(field, "orders-db-x7{Enter}");
		expect(await screen.findByText("Upgrade staged")).toBeVisible();
		expect(api.calls.filter(isUpgrade)).toHaveLength(1);
	});

	test("a refused upgrade shows the reason and stays open for another try", async () => {
		stubApi({
			[PLAN]: planRoute(),
			[UPGRADE]: problem(409, "a deploy is in progress", "conflict"),
		});
		const user = userEvent.setup();
		await open();
		await user.type(await confirmField(), "orders-db-x7");
		await user.click(upgradeButton("PostgreSQL 17"));
		expect(await screen.findByText("The upgrade was refused")).toBeVisible();
		expect(screen.getByText("a deploy is in progress")).toBeVisible();
		expect(upgradeButton("PostgreSQL 17")).toBeEnabled();
		expect(screen.queryByText("Upgrade staged")).not.toBeInTheDocument();
	});

	test("a manual upgrade points to Backups and has nothing to confirm", async () => {
		const api = stubApi({
			[PLAN]: planRoute({
				kind: "upgrade",
				method: "dump_restore",
				executable: false,
				steps: ["Take a backup", "Restore into a new database"],
			}),
		});
		const user = userEvent.setup();
		const seen = await open();
		expect(
			await screen.findByText("This upgrade is not automatic"),
		).toBeVisible();
		expect(screen.getByText("Dump and restore (manual)")).toBeVisible();
		expect(screen.getByText("Restore into a new database")).toBeVisible();
		expect(screen.getByText(/on PostgreSQL 17 \(Backups/)).toBeVisible();
		expect(screen.queryByLabelText(/to confirm/)).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: /^Upgrade to / })).toBeNull();

		await user.click(screen.getByRole("button", { name: "Open Backups" }));
		expect(seen.tabs).toEqual(["backups"]);
		expect(seen.closed).toEqual([false]);
		expect(api.calls.filter(isUpgrade)).toEqual([]);
	});

	test("a plan that is not an upgrade offers no confirmation field and cannot be confirmed", async () => {
		stubApi({
			[PLAN]: planRoute({ kind: "noop", method: "none", executable: true }),
		});
		await open();
		expect(await screen.findByText("No change")).toBeVisible();
		expect(screen.getByText("noop")).toBeVisible();
		expect(screen.queryByLabelText(/to confirm/)).not.toBeInTheDocument();
		expect(screen.queryByText("This upgrade is not automatic")).toBeNull();
		expect(upgradeButton("PostgreSQL 17")).toBeDisabled();
	});

	test("an unknown method shows its raw name", async () => {
		stubApi({ [PLAN]: planRoute({ method: "new_method" }) });
		await open();
		expect(await screen.findByText("new_method")).toBeVisible();
	});

	describe("an upgrade the agent runs", () => {
		const startedPending = {
			body: {
				status: "pending",
				upgradeId: "up-1",
				plan: planFor("17"),
			},
		};
		const upgradeView = (over: Record<string, unknown>) => ({
			body: databaseView({
				upgrade: {
					id: "up-1",
					createdAt: "2026-01-01T00:00:00Z",
					fromVersion: "16",
					toVersion: "17",
					method: "pg_upgrade",
					newVolume: "vol-new",
					oldVolume: "vol-old",
					status: "pending",
					...over,
				},
			}),
		});

		async function start() {
			const user = userEvent.setup();
			const seen = await open();
			await user.type(await confirmField(), "orders-db-x7");
			await user.click(upgradeButton("PostgreSQL 17"));
			return { user, seen };
		}

		test("while it converts the data the dialog can only be closed", async () => {
			stubApi({
				[PLAN]: planRoute(),
				[UPGRADE]: startedPending,
				[VIEW]: upgradeView({ status: "pending" }),
			});
			await start();
			expect(
				await screen.findByText(/Converting the data into a new volume/),
			).toBeVisible();
			expect(screen.getByRole("button", { name: "Close" })).toBeEnabled();
			expect(screen.queryByRole("button", { name: /^Upgrade to / })).toBeNull();
			expect(screen.queryByText("Upgrade staged")).not.toBeInTheDocument();
			expect(screen.queryByLabelText(/to confirm/)).not.toBeInTheDocument();
		});

		test("when it is ready the old volume is named", async () => {
			stubApi({
				[PLAN]: planRoute(),
				[UPGRADE]: startedPending,
				[VIEW]: upgradeView({ status: "ready" }),
			});
			await start();
			expect(await screen.findByText("Upgrade staged")).toBeVisible();
			expect(
				screen.getByText(
					"Deploy to start PostgreSQL 17; the previous volume “vol-old” is kept.",
				),
			).toBeVisible();
			expect(
				screen.getByRole("button", { name: "Review staged changes" }),
			).toBeEnabled();
		});

		test("when it fails the reason is shown and the old data is said to be untouched", async () => {
			stubApi({
				[PLAN]: planRoute(),
				[UPGRADE]: startedPending,
				[VIEW]: upgradeView({
					status: "failed",
					error: "pg_upgrade: lc_collate mismatch",
				}),
			});
			await start();
			expect(await screen.findByText("The upgrade failed")).toBeVisible();
			expect(screen.getByText(/pg_upgrade: lc_collate mismatch/)).toBeVisible();
			expect(
				screen.getByText(/The previous data is untouched\./),
			).toBeVisible();
			expect(screen.getByRole("button", { name: "Close" })).toBeEnabled();
			expect(screen.queryByText("Upgrade staged")).not.toBeInTheDocument();
		});

		test("a failure without a reason says the agent gave none", async () => {
			stubApi({
				[PLAN]: planRoute(),
				[UPGRADE]: startedPending,
				[VIEW]: upgradeView({ status: "failed" }),
			});
			await start();
			expect(
				await screen.findByText(/The agent did not report a reason\./),
			).toBeVisible();
		});

		test("an outcome that belongs to an earlier upgrade is ignored", async () => {
			const api = stubApi({
				[PLAN]: planRoute(),
				[UPGRADE]: startedPending,
				[VIEW]: upgradeView({ id: "up-0-earlier", status: "ready" }),
			});
			await start();
			await waitFor(() =>
				expect(
					api.calls.some(
						(c) => c.method === "GET" && c.path === "/services/svc-db/database",
					),
				).toBe(true),
			);
			expect(
				await screen.findByText(/Converting the data into a new volume/),
			).toBeVisible();
			expect(screen.queryByText("Upgrade staged")).not.toBeInTheDocument();
		});
	});
});
