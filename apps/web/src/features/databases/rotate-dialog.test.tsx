import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
	type OpenReviewOptions,
	onOpenStagedReview,
} from "#/features/deploy-button/open-review";
import { type ApiCall, type ApiStub, stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { databaseView, problem } from "./database.fixture";
import { RotateDialog } from "./rotate-dialog";

const service = { id: "svc-db", name: "orders-db" };
const ROTATE = "POST /services/:id/database/rotate-credentials";
const VIEW = "GET /services/:id/database";

/** The recorded call is the dry run, not the real rotation. */
const isDryRun = (call: ApiCall) =>
	typeof call.body === "object" &&
	call.body !== null &&
	"dryRun" in call.body &&
	call.body.dryRun === true;

interface PlanOverrides {
	mode?: "exec" | "redeploy" | "variable";
	consumers?: { name: string; serviceId: string; slug: string }[] | null;
	warnings?: string[] | null;
	blockers?: string[] | null;
	databaseRestart?: boolean;
}

const plan = (over: PlanOverrides = {}) => ({
	mode: "exec",
	passwordKey: "POSTGRES_PASSWORD",
	databaseRestart: false,
	consumers: [{ name: "web", serviceId: "svc-web", slug: "web-slug" }],
	warnings: null,
	blockers: null,
	...over,
});

const planned = (over: PlanOverrides = {}) => ({
	body: { dryRun: true, status: "planned", plan: plan(over) },
});

/** The reply to the real call; the dry run is told apart by its body. */
function rotateRoute(
	dry: PlanOverrides,
	real: Response | (() => Response) = () =>
		Response.json({ dryRun: false, status: "applied", plan: plan(dry) }),
) {
	return async (request: Request) => {
		const body = (await request.clone().json()) as { dryRun: boolean };
		if (body.dryRun) {
			return Response.json({
				dryRun: true,
				status: "planned",
				plan: plan(dry),
			});
		}
		return typeof real === "function" ? real() : real;
	};
}

async function open(onOpenChange: (open: boolean) => void = () => {}) {
	await renderWithProviders(
		<RotateDialog service={service} open onOpenChange={onOpenChange} />,
	);
}

describe("RotateDialog", () => {
	test("asks for a dry run when it opens and lists what a rotation would stage", async () => {
		const api = stubApi({
			[ROTATE]: planned({
				consumers: [
					{ name: "web", serviceId: "svc-web", slug: "web-slug" },
					{ name: "worker", serviceId: "svc-worker", slug: "worker-slug" },
				],
			}),
		});
		await open();

		expect(await screen.findByText("web-slug")).toBeInTheDocument();
		expect(screen.getByText("worker")).toBeInTheDocument();
		expect(
			screen.getByText("Rotate credentials · orders-db"),
		).toBeInTheDocument();
		expect(
			screen.getByText(/The password is changed inside the running database/),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Rotate password now" }),
		).toBeEnabled();
		// Only the dry run happened; nothing was rotated yet.
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]?.method).toBe("POST");
		expect(api.calls[0]?.path).toBe(
			"/services/svc-db/database/rotate-credentials",
		);
		expect(api.calls[0]?.body).toEqual({ dryRun: true });
		expect(api.unmatched).toEqual([]);
	});

	test("shows a placeholder and a locked confirm while the plan loads", async () => {
		stubApi({ [ROTATE]: () => new Promise<Response>(() => {}) });
		await open();
		expect(
			await screen.findByRole("status", { name: "Planning the rotation" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Rotate password" }),
		).toBeDisabled();
	});

	test("says so when no other service references the database", async () => {
		stubApi({ [ROTATE]: planned({ consumers: null }) });
		await open();
		expect(
			await screen.findByText("No other service references this database."),
		).toBeInTheDocument();
	});

	test("a redeploy-mode plan stages the password and notes the restart", async () => {
		stubApi({
			[ROTATE]: planned({ mode: "redeploy", databaseRestart: true }),
		});
		await open();
		expect(
			await screen.findByRole("button", { name: "Stage new password" }),
		).toBeEnabled();
		expect(
			screen.getByText(/A new password is staged\. The database picks it up/),
		).toBeInTheDocument();
		expect(
			screen.getByText(/The database itself restarts when you deploy\./),
		).toBeInTheDocument();
	});

	test("an exec-mode plan without a restart does not mention one", async () => {
		stubApi({ [ROTATE]: planned() });
		await open();
		await screen.findByText("web");
		expect(
			screen.queryByText(/The database itself restarts/),
		).not.toBeInTheDocument();
	});

	test("warnings are shown and still allow the rotation", async () => {
		stubApi({
			[ROTATE]: planned({ warnings: ["Open connections are dropped."] }),
		});
		await open();
		expect(await screen.findByText("Before you rotate")).toBeInTheDocument();
		expect(screen.getByText("Open connections are dropped.")).toBeVisible();
		expect(
			screen.getByRole("button", { name: "Rotate password now" }),
		).toBeEnabled();
	});

	test("blockers are listed and lock the confirm button", async () => {
		const api = stubApi({
			[ROTATE]: planned({ blockers: ["The database is not running."] }),
		});
		const user = userEvent.setup();
		await open();
		expect(
			await screen.findByText("It cannot be rotated right now"),
		).toBeInTheDocument();
		expect(screen.getByText("The database is not running.")).toBeVisible();
		const confirm = screen.getByRole("button", { name: "Rotate password now" });
		expect(confirm).toBeDisabled();
		await user.click(confirm);
		expect(api.calls).toHaveLength(1);
	});

	test("a plan that cannot be computed shows the error and offers no rotation", async () => {
		stubApi({
			[ROTATE]: problem(409, "the database is being upgraded", "conflict"),
		});
		await open();
		expect(
			await screen.findByText("The rotation could not be planned"),
		).toBeInTheDocument();
		expect(screen.getByText("the database is being upgraded")).toBeVisible();
		expect(
			screen.getByRole("button", { name: "Rotate password" }),
		).toBeDisabled();
	});

	test("confirming sends the real call and offers the staged review", async () => {
		const opened: OpenReviewOptions[] = [];
		const stop = onOpenStagedReview((options) => opened.push(options));
		const closed: boolean[] = [];
		const api = stubApi({ [ROTATE]: rotateRoute({}) });
		const user = userEvent.setup();
		await open((next) => closed.push(next));

		await user.click(
			await screen.findByRole("button", { name: "Rotate password now" }),
		);
		expect(await screen.findByText("Credentials rotated")).toBeInTheDocument();
		expect(api.calls.map((c) => c.body)).toEqual([
			{ dryRun: true },
			{ dryRun: false },
		]);
		// The plan is replaced by the result.
		expect(
			screen.queryByText("Services that will be staged"),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Rotate password now" }),
		).not.toBeInTheDocument();

		await user.click(
			screen.getByRole("button", { name: "Review staged changes" }),
		);
		stop();
		expect(opened).toHaveLength(1);
		expect(closed).toEqual([false]);
	});

	test("Close after a rotation only closes", async () => {
		const opened: OpenReviewOptions[] = [];
		const stop = onOpenStagedReview((options) => opened.push(options));
		const closed: boolean[] = [];
		stubApi({ [ROTATE]: rotateRoute({}) });
		const user = userEvent.setup();
		await open((next) => closed.push(next));
		await user.click(
			await screen.findByRole("button", { name: "Rotate password now" }),
		);
		await user.click(await screen.findByRole("button", { name: "Close" }));
		stop();
		expect(closed).toEqual([false]);
		expect(opened).toEqual([]);
	});

	test("Cancel closes without rotating", async () => {
		const closed: boolean[] = [];
		const api = stubApi({ [ROTATE]: planned() });
		const user = userEvent.setup();
		await open((next) => closed.push(next));
		await screen.findByText("web");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(closed).toEqual([false]);
		expect(api.calls.map((c) => c.body)).toEqual([{ dryRun: true }]);
	});

	test("a refused rotation shows the reason and can be retried", async () => {
		let attempts = 0;
		const api = stubApi({
			[ROTATE]: rotateRoute({}, () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{
								status: 409,
								title: "x",
								detail: "rotation already running",
								code: "conflict",
							},
							{ status: 409 },
						)
					: Response.json({ dryRun: false, status: "applied", plan: plan({}) });
			}),
		});
		const user = userEvent.setup();
		await open();
		await user.click(
			await screen.findByRole("button", { name: "Rotate password now" }),
		);
		expect(
			await screen.findByText("The rotation was refused"),
		).toBeInTheDocument();
		expect(screen.getByText("rotation already running")).toBeVisible();
		// The plan stays so the user can try again.
		expect(screen.getByText("Services that will be staged")).toBeVisible();

		await user.click(
			screen.getByRole("button", { name: "Rotate password now" }),
		);
		expect(await screen.findByText("Credentials rotated")).toBeInTheDocument();
		expect(api.calls.filter((c) => !isDryRun(c))).toHaveLength(2);
	});

	describe("a rotation the agent has to carry out", () => {
		const pending = (rotationId?: string) =>
			Response.json({
				dryRun: false,
				status: "pending",
				plan: plan({}),
				...(rotationId ? { rotationId } : {}),
			});

		test("waits while the agent works, then reports it applied", async () => {
			const api = stubApi({
				[ROTATE]: rotateRoute({}, () => pending("rot-1")),
				[VIEW]: {
					body: databaseView({
						rotation: {
							id: "rot-1",
							status: "applied",
							mode: "exec",
							createdAt: "2026-01-01T00:00:00Z",
						},
					}),
				},
			});
			const user = userEvent.setup();
			await open();
			await user.click(
				await screen.findByRole("button", { name: "Rotate password now" }),
			);
			expect(
				await screen.findByText("Credentials rotated"),
			).toBeInTheDocument();
			expect(
				screen.getByRole("button", { name: "Review staged changes" }),
			).toBeEnabled();
			expect(
				api.calls.some(
					(c) => c.method === "GET" && c.path === "/services/svc-db/database",
				),
			).toBe(true);
		});

		test("a failed rotation shows the agent's reason and says the password is unchanged", async () => {
			stubApi({
				[ROTATE]: rotateRoute({}, () => pending("rot-1")),
				[VIEW]: {
					body: databaseView({
						rotation: {
							id: "rot-1",
							status: "failed",
							mode: "exec",
							error: "ALTER ROLE failed: connection refused",
							createdAt: "2026-01-01T00:00:00Z",
						},
					}),
				},
			});
			const user = userEvent.setup();
			await open();
			await user.click(
				await screen.findByRole("button", { name: "Rotate password now" }),
			);
			expect(
				await screen.findByText("The rotation failed"),
			).toBeInTheDocument();
			expect(
				screen.getByText(/ALTER ROLE failed: connection refused/),
			).toBeVisible();
			expect(screen.getByText(/The password was not changed\./)).toBeVisible();
			expect(
				screen.queryByRole("button", { name: /Rotate password now/ }),
			).not.toBeInTheDocument();
			expect(screen.getByRole("button", { name: "Close" })).toBeEnabled();
		});

		test("a failure without a reason says the agent gave none", async () => {
			stubApi({
				[ROTATE]: rotateRoute({}, () => pending()),
				[VIEW]: {
					body: databaseView({
						rotation: {
							id: "whatever",
							status: "failed",
							mode: "exec",
							createdAt: "2026-01-01T00:00:00Z",
						},
					}),
				},
			});
			const user = userEvent.setup();
			await open();
			await user.click(
				await screen.findByRole("button", { name: "Rotate password now" }),
			);
			expect(
				await screen.findByText(/The agent did not report a reason\./),
			).toBeVisible();
		});

		test("keeps working while the latest rotation is another one, and cannot be cancelled", async () => {
			const api = stubApi({
				[ROTATE]: rotateRoute({}, () => pending("rot-2")),
				[VIEW]: {
					body: databaseView({
						rotation: {
							id: "rot-1-old",
							status: "applied",
							mode: "exec",
							createdAt: "2025-01-01T00:00:00Z",
						},
					}),
				},
			});
			const user = userEvent.setup();
			await open();
			await user.click(
				await screen.findByRole("button", { name: "Rotate password now" }),
			);
			expect(
				await screen.findByText(/Rotating: the agent is changing the password/),
			).toBeInTheDocument();
			// Wait for the first poll so the stale rotation has been seen.
			await expectPolled(api);
			expect(screen.queryByText("Credentials rotated")).not.toBeInTheDocument();
			expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
		});
	});
});

async function expectPolled(api: ApiStub) {
	await waitFor(() =>
		expect(
			api.calls.some(
				(c) => c.method === "GET" && c.path === "/services/svc-db/database",
			),
		).toBe(true),
	);
}
