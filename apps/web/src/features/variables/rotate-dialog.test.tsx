import { describe, expect, it } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	RotationAffected,
	VariableRotationPlan,
} from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, type Overrides, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import {
	RotateDialog,
	RotatePlanDialog,
	type RotationSource,
} from "./rotate-dialog";

const ROTATE = "POST /services/:id/variables/:key/rotate";

function plan(
	overrides: Overrides<VariableRotationPlan> = {},
): VariableRotationPlan {
	return make("VariableRotationPlan", {
		key: "SECRET",
		kind: "app-secret",
		staged: false,
		affected: [],
		blockers: [],
		deployOrder: [],
		warnings: [],
		...overrides,
	});
}

const WEB: RotationAffected = make("RotationAffected", {
	serviceId: "s-web",
	slug: "web",
	strategy: "rolling",
	downtime: "none",
	via: ["DATABASE_URL", "REDIS_URL"],
});
const WORKER: RotationAffected = make("RotationAffected", {
	serviceId: "s-worker",
	slug: "worker",
	strategy: "recreate",
	downtime: "brief",
	via: null,
});

describe("RotateDialog", () => {
	it("renders nothing and requests nothing while closed", async () => {
		const api = stubApi({});
		await renderWithProviders(
			<RotateDialog serviceId="s1" variableKey={null} onClose={() => {}} />,
		);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	it("plans with a dry run first and lists the affected services", async () => {
		const reply = Promise.withResolvers<Response>();
		const api = stubApi({ [ROTATE]: () => reply.promise });
		await renderWithProviders(
			<RotateDialog serviceId="s1" variableKey="SECRET" onClose={() => {}} />,
		);
		expect(
			await screen.findByRole("status", { name: "Planning the rotation" }),
		).toBeInTheDocument();
		// Nothing can be staged until the plan is known.
		expect(
			screen.getByRole("button", { name: "Stage rotation" }),
		).toBeDisabled();

		reply.resolve(
			Response.json(
				plan({
					affected: [WEB, WORKER],
					deployOrder: ["web", "worker"],
					warnings: ["Existing sessions end"],
				}),
			),
		);
		expect(await screen.findByText("Rotate SECRET")).toBeInTheDocument();
		const table = await screen.findByRole("table");
		const rows = within(table).getAllByRole("row").slice(1);
		expect(rows).toHaveLength(2);
		expect(rows[0]).toHaveTextContent("web");
		expect(rows[0]).toHaveTextContent("DATABASE_URL, REDIS_URL");
		expect(rows[0]).toHaveTextContent("rolling, no downtime");
		expect(rows[1]).toHaveTextContent("worker");
		expect(rows[1]).toHaveTextContent("recreate, brief downtime");
		expect(screen.getByText("web → worker")).toBeInTheDocument();
		expect(screen.getByText("Existing sessions end")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Stage rotation" }),
		).toBeEnabled();

		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]?.path).toBe("/services/s1/variables/SECRET/rotate");
		expect(api.calls[0]?.body).toEqual({ dryRun: true });
	});

	it("hides the deploy order when a single service is affected", async () => {
		stubApi({
			[ROTATE]: { body: plan({ affected: [WEB], deployOrder: ["web"] }) },
		});
		await renderWithProviders(
			<RotateDialog serviceId="s1" variableKey="SECRET" onClose={() => {}} />,
		);
		await screen.findByRole("table");
		expect(screen.queryByText(/Deploy order/)).not.toBeInTheDocument();
	});

	it("shows blockers and keeps staging disabled", async () => {
		const api = stubApi({
			[ROTATE]: {
				body: plan({
					kind: "engine-bound",
					blockers: [
						{
							code: "engine_bound",
							message: "Rotate it from the database page",
						},
					],
				}),
			},
		});
		await renderWithProviders(
			<RotateDialog serviceId="s1" variableKey="SECRET" onClose={() => {}} />,
		);
		expect(
			await screen.findByText("Rotate it from the database page"),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Stage rotation" }),
		).toBeDisabled();
		// Only the dry run was requested.
		expect(api.calls.map((c) => c.body)).toEqual([{ dryRun: true }]);
	});

	it("shows the server's reason when planning fails and cannot stage", async () => {
		stubApi({
			[ROTATE]: problem(404, {
				code: "not_found",
				title: "Not found",
				detail: "SECRET is not generated",
			}),
		});
		await renderWithProviders(
			<RotateDialog serviceId="s1" variableKey="SECRET" onClose={() => {}} />,
		);
		expect(
			await screen.findByText("SECRET is not generated"),
		).toBeInTheDocument();
		expect(screen.queryByRole("table")).not.toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Stage rotation" }),
		).toBeDisabled();
	});

	it("stages the rotation for real, toasts the deploy order and closes", async () => {
		const user = userEvent.setup();
		let closed = 0;
		const api = stubApi({
			[ROTATE]: (request) =>
				request
					.clone()
					.json()
					.then((body: { dryRun: boolean }) =>
						Response.json(
							body.dryRun
								? plan({ affected: [WEB, WORKER] })
								: plan({
										staged: true,
										affected: [WEB, WORKER],
										deployOrder: ["db", "web"],
									}),
						),
					),
			"GET /services/:id/variables": { body: make("VariableList") },
		});
		await renderWithProviders(
			<RotateDialog
				serviceId="s1"
				variableKey="SECRET"
				onClose={() => {
					closed += 1;
				}}
			/>,
		);
		await screen.findByRole("table");
		await user.click(screen.getByRole("button", { name: "Stage rotation" }));

		expect(await screen.findByText("Rotated SECRET")).toBeInTheDocument();
		expect(
			screen.getByText("Staged. Deploy in this order: db → web."),
		).toBeInTheDocument();
		await waitFor(() => expect(closed).toBe(1));
		expect(api.calls.map((call) => call.body)).toEqual([
			{ dryRun: true },
			{ dryRun: false },
		]);
	});

	it("toasts a plain 'Staged.' when the answer has no deploy order", async () => {
		const user = userEvent.setup();
		stubApi({
			[ROTATE]: { body: plan({ staged: true }) },
			"GET /services/:id/variables": { body: make("VariableList") },
		});
		await renderWithProviders(
			<RotateDialog serviceId="s1" variableKey="SECRET" onClose={() => {}} />,
		);
		await user.click(
			await screen.findByRole("button", { name: "Stage rotation" }),
		);
		expect(await screen.findByText("Rotated SECRET")).toBeInTheDocument();
		expect(screen.getByText("Staged.")).toBeInTheDocument();
	});

	it("shows the server's reason and stays open when staging fails", async () => {
		const user = userEvent.setup();
		let closed = 0;
		stubApi({
			[ROTATE]: (request) =>
				request
					.clone()
					.json()
					.then((body: { dryRun: boolean }) =>
						body.dryRun
							? Response.json(plan())
							: Response.json(
									problem(409, {
										code: "conflict",
										title: "Conflict",
										detail: "generation moved",
									}).body,
									{ status: 409 },
								),
					),
		});
		await renderWithProviders(
			<RotateDialog
				serviceId="s1"
				variableKey="SECRET"
				onClose={() => {
					closed += 1;
				}}
			/>,
		);
		await user.click(
			await screen.findByRole("button", { name: "Stage rotation" }),
		);
		expect(await screen.findByText("generation moved")).toBeInTheDocument();
		expect(closed).toBe(0);
		expect(screen.queryByText("Rotated SECRET")).not.toBeInTheDocument();
		// The person can try again.
		expect(
			screen.getByRole("button", { name: "Stage rotation" }),
		).toBeEnabled();
	});

	it("closes from Cancel without staging anything", async () => {
		const user = userEvent.setup();
		let closed = 0;
		const api = stubApi({ [ROTATE]: { body: plan() } });
		await renderWithProviders(
			<RotateDialog
				serviceId="s1"
				variableKey="SECRET"
				onClose={() => {
					closed += 1;
				}}
			/>,
		);
		await screen.findByRole("table");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(closed).toBe(1);
		expect(api.calls.map((call) => call.body)).toEqual([{ dryRun: true }]);
	});
});

describe("RotatePlanDialog", () => {
	function source(overrides: Partial<RotationSource> = {}): RotationSource {
		return {
			queryKey: ["plan", "shared-key"],
			fetchPlan: async () => plan({ affected: [WEB] }),
			stage: {
				mutate: () => {},
				isPending: false,
				isError: false,
				error: null,
			},
			...overrides,
		};
	}

	it("plans through the owner's fetchPlan and stages through its mutation", async () => {
		const user = userEvent.setup();
		const staged: string[] = [];
		let closed = 0;
		await renderWithProviders(
			<RotatePlanDialog
				variableKey="SHARED_KEY"
				onClose={() => {
					closed += 1;
				}}
				source={source({
					stage: {
						mutate: (key, options) => {
							staged.push(key);
							options.onSuccess(
								plan({ staged: true, deployOrder: ["a", "b"] }),
							);
						},
						isPending: false,
						isError: false,
						error: null,
					},
				})}
			/>,
		);
		expect(await screen.findByText("Rotate SHARED_KEY")).toBeInTheDocument();
		await user.click(
			await screen.findByRole("button", { name: "Stage rotation" }),
		);
		expect(staged).toEqual(["SHARED_KEY"]);
		expect(
			await screen.findByText("Staged. Deploy in this order: a → b."),
		).toBeInTheDocument();
		expect(closed).toBe(1);
	});

	it("handles an owner that resolves with no plan", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<RotatePlanDialog
				variableKey="K"
				onClose={() => {}}
				source={source({
					stage: {
						mutate: (_key, options) => options.onSuccess(undefined),
						isPending: false,
						isError: false,
						error: null,
					},
				})}
			/>,
		);
		await user.click(
			await screen.findByRole("button", { name: "Stage rotation" }),
		);
		expect(await screen.findByText("Staged.")).toBeInTheDocument();
	});

	it("disables staging and shows its error while the owner's mutation is pending or failed", async () => {
		await renderWithProviders(
			<RotatePlanDialog
				variableKey="K"
				onClose={() => {}}
				source={source({
					stage: {
						mutate: () => {},
						isPending: true,
						isError: true,
						error: new Error("shared layer is locked"),
					},
				})}
			/>,
		);
		await screen.findByRole("table");
		expect(
			screen.getByRole("button", { name: /Stage rotation/ }),
		).toBeDisabled();
		expect(screen.getByText("shared layer is locked")).toBeInTheDocument();
	});

	it("shows an error of the owner's plan fetch", async () => {
		await renderWithProviders(
			<RotatePlanDialog
				variableKey="K"
				onClose={() => {}}
				source={source({
					fetchPlan: async () => {
						throw new Error("plan unavailable");
					},
				})}
			/>,
		);
		expect(await screen.findByText("plan unavailable")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Stage rotation" }),
		).toBeDisabled();
	});
});
