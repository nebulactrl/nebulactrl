import { describe, expect, mock, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { MANUAL_NOTE, PromoteDialog } from "./promote-dialog";
import { promotePlan, replication } from "./replicas.fixture";

const PROMOTE = "POST /services/:id/database/promote";
const REPLICATION = "GET /services/:id/database/replication";

const replica = { id: "svc-r1", name: "Orders replica", slug: "orders-r1" };

const problem = (status: number, detail: string, code: string) => ({
	status,
	body: { status, title: "Error", detail, code },
});

const isRealPromote = (call: { method: string; body: unknown }) =>
	call.method === "POST" &&
	(call.body as { dryRun?: boolean }).dryRun === false;

const running = {
	id: "promo-1",
	status: "running" as const,
	mode: "switchover" as const,
	step: "freeze",
	message: "Freezing the old primary",
	startedAt: "2026-01-01T00:00:00Z",
};

async function open(onOpenChange: (open: boolean) => void = () => {}) {
	const result = await renderWithProviders(
		<PromoteDialog service={replica} open onOpenChange={onOpenChange} />,
	);
	return { ...result, dialog: await screen.findByRole("dialog") };
}

/** The promote route: a dry run answers with the plan, a real one with `started`. */
function promoteRoute(
	plan: ReturnType<typeof promotePlan>,
	started: (
		request: Request,
		body: Record<string, unknown>,
	) => Response | undefined = () => undefined,
) {
	return async (request: Request) => {
		const body = (await request.clone().json()) as Record<string, unknown>;
		if (body.dryRun === true) {
			return Response.json({ dryRun: true, plan });
		}
		return (
			started(request, body) ??
			Response.json({
				dryRun: false,
				plan,
				promotionId: "promo-1",
				status: "running",
			})
		);
	};
}

describe("PromoteDialog plan", () => {
	test("dry-runs the promote on open and describes a switchover", async () => {
		const api = stubApi({
			[PROMOTE]: promoteRoute(promotePlan({ lagBytes: 0 })),
		});
		const { dialog } = await open();
		expect(
			within(dialog).getByText("Promote to primary · Orders replica"),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText(new RegExp(MANUAL_NOTE)),
		).toBeInTheDocument();
		expect(await within(dialog).findByText("switchover")).toBeInTheDocument();
		expect(
			within(dialog).getByText(
				/orders-db is reachable: it is frozen, stopped and fenced/,
			),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText("The replica is in sync with the primary."),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText(/orders-db is fenced: stopped, its data kept/),
		).toBeInTheDocument();
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]).toMatchObject({
			method: "POST",
			path: "/services/svc-r1/database/promote",
			body: { dryRun: true },
		});
	});

	test("shows a planning placeholder until the plan arrives", async () => {
		let release: () => void = () => {};
		const gate = new Promise<void>((resolve) => {
			release = resolve;
		});
		const route = promoteRoute(promotePlan({ lagBytes: 0 }));
		stubApi({
			[PROMOTE]: async (request) => {
				await gate;
				return route(request);
			},
		});
		const { dialog } = await open();
		expect(
			within(dialog).getByRole("status", { name: "Planning the promote" }),
		).toBeInTheDocument();
		expect(
			within(dialog).getByRole("button", { name: "Promote to primary" }),
		).toBeDisabled();
		release();
		expect(await within(dialog).findByText("switchover")).toBeInTheDocument();
		expect(
			within(dialog).queryByRole("status", { name: "Planning the promote" }),
		).not.toBeInTheDocument();
	});

	test("says how far behind the replica is, or that the lag is unknown", async () => {
		stubApi({
			[PROMOTE]: promoteRoute(promotePlan({ lagBytes: 3 * 1024 * 1024 })),
		});
		const { dialog, unmount } = await open();
		expect(
			await within(dialog).findByText(
				/The replica is 3 MiB behind the primary\./,
			),
		).toBeInTheDocument();
		unmount();

		stubApi({ [PROMOTE]: promoteRoute(promotePlan()) });
		const second = await open();
		expect(
			await within(second.dialog).findByText("The replica's lag is not known."),
		).toBeInTheDocument();
	});

	test("lists the consumers per server with their keys, mesh devices and warnings", async () => {
		stubApi({
			[PROMOTE]: promoteRoute(
				promotePlan({
					consumers: [
						{
							serviceId: "c1",
							slug: "web",
							serverName: "eu-1",
							keys: ["DATABASE_URL", "DATABASE_WRITE_URL"],
							devicesAdded: 1,
							warnings: [{ code: "w", message: "Web needs a redeploy" }],
						},
						{
							serviceId: "c2",
							slug: "worker",
							serverName: "eu-1",
							keys: [],
							devicesAdded: 1,
							warnings: null,
						},
						{
							serviceId: "c3",
							slug: "cron",
							serverName: "ap-1",
							keys: ["DATABASE_URL"],
							devicesAdded: 0,
							warnings: null,
						},
					],
				}),
			),
		});
		const { dialog } = await open();
		const section = await within(dialog).findByRole("region", {
			name: "Consumers that are rewritten",
		});
		const servers = within(section).getAllByText(/^(eu-1|ap-1)/);
		// Sorted by server name.
		expect(servers.map((node) => node.textContent?.trim())).toEqual([
			"ap-1",
			"eu-1adds 2 mesh devices",
		]);
		expect(within(section).getByText("web")).toBeInTheDocument();
		expect(
			within(section).getByText(/DATABASE_URL, DATABASE_WRITE_URL/),
		).toBeInTheDocument();
		expect(
			within(section).getByText("Web needs a redeploy"),
		).toBeInTheDocument();
		expect(
			within(section).getByText(
				/become references to orders-db-replica as staged changes/,
			),
		).toBeInTheDocument();
	});

	test("a promote that rewrites no consumer says so", async () => {
		stubApi({ [PROMOTE]: promoteRoute(promotePlan()) });
		const { dialog } = await open();
		expect(
			await within(dialog).findByText(
				"No service references orders-db's writer keys.",
			),
		).toBeInTheDocument();
		expect(
			within(dialog).queryByText(/as staged changes/),
		).not.toBeInTheDocument();
	});

	test("names the replicas that must be rebuilt and the schedules that move", async () => {
		stubApi({
			[PROMOTE]: promoteRoute(
				promotePlan({
					rebuildReplicas: [
						{ id: "x", slug: "orders-r2" },
						{ id: "y", slug: "orders-r3" },
					],
					schedulesMoving: 2,
				}),
			),
		});
		const { dialog } = await open();
		const section = await within(dialog).findByRole("region", {
			name: "Replicas that must be rebuilt",
		});
		expect(within(section).getByText("orders-r2")).toBeInTheDocument();
		expect(within(section).getByText("orders-r3")).toBeInTheDocument();
		expect(
			within(dialog).getByText(
				/2 backup schedules move\s+to orders-db-replica/,
			),
		).toBeInTheDocument();
	});

	test("one moving schedule reads in the singular", async () => {
		stubApi({ [PROMOTE]: promoteRoute(promotePlan({ schedulesMoving: 1 })) });
		const { dialog } = await open();
		expect(
			await within(dialog).findByText(
				/1 backup schedule moves\s+to orders-db-replica/,
			),
		).toBeInTheDocument();
		expect(
			within(dialog).queryByRole("region", {
				name: "Replicas that must be rebuilt",
			}),
		).not.toBeInTheDocument();
	});

	test("shows the plan's warnings and still lets a clean promote through", async () => {
		stubApi({
			[PROMOTE]: promoteRoute(
				promotePlan({
					warnings: [{ code: "w", message: "Replica lags a little" }],
				}),
			),
		});
		const { dialog } = await open();
		expect(
			await within(dialog).findByText("Before you promote"),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText("Replica lags a little"),
		).toBeInTheDocument();
		expect(within(dialog).getByLabelText(/to confirm/)).toBeInTheDocument();
	});

	test("blockers explain why it cannot be promoted and remove the confirmation", async () => {
		stubApi({
			[PROMOTE]: promoteRoute(
				promotePlan({
					blockers: [
						{ code: "b", message: "A backup is running on the primary" },
					],
				}),
			),
		});
		const { dialog } = await open();
		expect(
			await within(dialog).findByText("It cannot be promoted right now"),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText("A backup is running on the primary"),
		).toBeInTheDocument();
		expect(
			within(dialog).queryByLabelText(/to confirm/),
		).not.toBeInTheDocument();
		expect(
			within(dialog).getByRole("button", { name: "Promote to primary" }),
		).toBeDisabled();
	});

	test("a plan that cannot be made shows the control plane's reason and keeps Promote locked", async () => {
		const api = stubApi({
			[PROMOTE]: problem(
				409,
				"The replica is not streaming.",
				"replica_not_ready",
			),
		});
		const { dialog } = await open();
		expect(
			await within(dialog).findByText("The promote could not be planned"),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText("The replica is not streaming."),
		).toBeInTheDocument();
		expect(
			within(dialog).queryByRole("status", { name: "Planning the promote" }),
		).not.toBeInTheDocument();
		expect(
			within(dialog).getByRole("button", { name: "Promote to primary" }),
		).toBeDisabled();
		expect(api.calls).toHaveLength(1);
	});
});

describe("PromoteDialog confirmation", () => {
	test("Promote unlocks only when the replica's slug is typed exactly", async () => {
		stubApi({ [PROMOTE]: promoteRoute(promotePlan({ lagBytes: 0 })) });
		const user = userEvent.setup();
		const { dialog } = await open();
		const input = await within(dialog).findByLabelText(/to confirm/);
		const button = within(dialog).getByRole("button", {
			name: "Promote to primary",
		});
		expect(button).toBeDisabled();
		await user.type(input, "orders-r");
		expect(button).toBeDisabled();
		await user.type(input, "1x");
		expect(button).toBeDisabled();
		await user.clear(input);
		await user.type(input, "orders-r1");
		expect(button).toBeEnabled();
	});

	test("confirming sends the typed slug with an idempotency key, then follows the promotion to success", async () => {
		let key: string | null = null;
		const view = replication({ role: "replica", promotion: running });
		const api = stubApi({
			[PROMOTE]: promoteRoute(promotePlan({ lagBytes: 0 }), (request) => {
				key = request.headers.get("Idempotency-Key");
				return undefined;
			}),
			[REPLICATION]: () => Response.json(view),
		});
		const user = userEvent.setup();
		const { dialog } = await open();
		await user.type(
			await within(dialog).findByLabelText(/to confirm/),
			"orders-r1",
		);
		await user.click(
			within(dialog).getByRole("button", { name: "Promote to primary" }),
		);

		expect(
			await within(dialog).findByText("Freezing the old primary"),
		).toBeInTheDocument();
		const real = api.calls.find((call) => isRealPromote(call));
		expect(real?.body).toEqual({
			confirm: "orders-r1",
			acceptDataLoss: false,
			dryRun: false,
		});
		expect(key).toMatch(/^[0-9a-f-]{36}$/);
		// The plan and the typed field are gone; only Close is offered.
		expect(
			within(dialog).queryByLabelText(/to confirm/),
		).not.toBeInTheDocument();
		expect(
			within(dialog).queryByRole("button", { name: /Promote/ }),
		).not.toBeInTheDocument();
		expect(
			within(dialog).getByRole("button", { name: "Close" }),
		).toBeInTheDocument();
	});

	test("shows the success message once the promotion succeeded", async () => {
		stubApi({
			[PROMOTE]: promoteRoute(promotePlan({ lagBytes: 0 })),
			[REPLICATION]: {
				body: replication({
					role: "replica",
					promotion: { ...running, status: "succeeded", message: "Done" },
				}),
			},
		});
		const user = userEvent.setup();
		const { dialog } = await open();
		await user.type(
			await within(dialog).findByLabelText(/to confirm/),
			"orders-r1",
		);
		await user.click(
			within(dialog).getByRole("button", { name: "Promote to primary" }),
		);
		expect(await within(dialog).findByText("Promoted")).toBeInTheDocument();
		expect(
			within(dialog).getByText(/Orders replica is the primary now/),
		).toBeInTheDocument();
	});

	test("shows the failure of the promotion it started", async () => {
		stubApi({
			[PROMOTE]: promoteRoute(promotePlan({ lagBytes: 0 })),
			[REPLICATION]: {
				body: replication({
					role: "replica",
					promotion: {
						...running,
						status: "failed",
						message: "The old primary did not stop",
					},
				}),
			},
		});
		const user = userEvent.setup();
		const { dialog } = await open();
		await user.type(
			await within(dialog).findByLabelText(/to confirm/),
			"orders-r1",
		);
		await user.click(
			within(dialog).getByRole("button", { name: "Promote to primary" }),
		);
		expect(
			await within(dialog).findByText("The promote failed"),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText("The old primary did not stop"),
		).toBeInTheDocument();
		expect(within(dialog).queryByText("Promoted")).not.toBeInTheDocument();
	});

	test("ignores a promotion that this dialog did not start", async () => {
		stubApi({
			[PROMOTE]: promoteRoute(promotePlan({ lagBytes: 0 })),
			[REPLICATION]: {
				body: replication({
					role: "replica",
					// An earlier promote, finished long ago.
					promotion: {
						...running,
						id: "older",
						status: "succeeded",
						message: "Old news",
					},
				}),
			},
		});
		const user = userEvent.setup();
		const { dialog, queryClient } = await open();
		await user.type(
			await within(dialog).findByLabelText(/to confirm/),
			"orders-r1",
		);
		await user.click(
			within(dialog).getByRole("button", { name: "Promote to primary" }),
		);
		await waitFor(() =>
			expect(
				queryClient
					.getQueryCache()
					.getAll()
					.some(
						(q) =>
							q.state.data !== undefined && "role" in (q.state.data as object),
					),
			).toBe(true),
		);
		expect(
			within(dialog).getByText("Starting the promote…"),
		).toBeInTheDocument();
		expect(within(dialog).queryByText("Promoted")).not.toBeInTheDocument();
		expect(within(dialog).queryByText("Old news")).not.toBeInTheDocument();
	});

	test("Enter in the confirmation field promotes once it is unlocked", async () => {
		const api = stubApi({
			[PROMOTE]: promoteRoute(promotePlan({ lagBytes: 0 })),
			[REPLICATION]: {
				body: replication({ role: "replica", promotion: running }),
			},
		});
		const user = userEvent.setup();
		const { dialog } = await open();
		const input = await within(dialog).findByLabelText(/to confirm/);
		await user.type(input, "wrong{Enter}");
		expect(api.calls.filter((c) => isRealPromote(c))).toEqual([]);
		await user.clear(input);
		await user.type(input, "orders-r1{Enter}");
		expect(
			await within(dialog).findByText("Freezing the old primary"),
		).toBeInTheDocument();
		expect(api.calls.filter((c) => isRealPromote(c))).toHaveLength(1);
	});

	test("a refused promote shows the reason and stays on the plan for another try", async () => {
		const api = stubApi({
			[PROMOTE]: promoteRoute(promotePlan({ lagBytes: 0 }), () =>
				Response.json(
					{
						status: 409,
						title: "Conflict",
						detail: "Another promote is running.",
						code: "promotion_running",
					},
					{ status: 409 },
				),
			),
		});
		const user = userEvent.setup();
		const { dialog } = await open();
		await user.type(
			await within(dialog).findByLabelText(/to confirm/),
			"orders-r1",
		);
		await user.click(
			within(dialog).getByRole("button", { name: "Promote to primary" }),
		);
		expect(
			await within(dialog).findByText("The promote was refused"),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText("Another promote is running."),
		).toBeInTheDocument();
		expect(within(dialog).getByLabelText(/to confirm/)).toHaveValue(
			"orders-r1",
		);
		expect(
			within(dialog).getByRole("button", { name: "Promote to primary" }),
		).toBeEnabled();
		expect(api.unmatched).toEqual([]);
	});

	test("Cancel closes the dialog without promoting", async () => {
		const onOpenChange = mock((_open: boolean) => {});
		const api = stubApi({
			[PROMOTE]: promoteRoute(promotePlan({ lagBytes: 0 })),
		});
		const user = userEvent.setup();
		const { dialog } = await open(onOpenChange);
		await within(dialog).findByText("switchover");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		expect(onOpenChange).toHaveBeenCalledWith(false);
		expect(api.calls).toHaveLength(1);
	});
});

describe("PromoteDialog forced promote", () => {
	const forced = promotePlan({
		mode: "forced",
		lagBytes: 5 * 1024 * 1024,
		dataLossWarning: "Up to 5 MiB of writes will be lost.",
	});

	test("warns about data loss and keeps Promote locked until the loss is accepted", async () => {
		const api = stubApi({
			[PROMOTE]: promoteRoute(forced),
			[REPLICATION]: {
				body: replication({ role: "replica", promotion: running }),
			},
		});
		const user = userEvent.setup();
		const { dialog } = await open();
		expect(await within(dialog).findByText("forced")).toBeInTheDocument();
		expect(
			within(dialog).getByText(
				/orders-db is not reachable: orders-db-replica is promoted as it is\./,
			),
		).toBeInTheDocument();
		expect(within(dialog).getByText("Data will be lost")).toBeInTheDocument();
		expect(
			within(dialog).getByText("Up to 5 MiB of writes will be lost."),
		).toBeInTheDocument();

		await user.type(within(dialog).getByLabelText(/to confirm/), "orders-r1");
		const button = within(dialog).getByRole("button", {
			name: "Promote, accepting data loss",
		});
		expect(button).toBeDisabled();
		await user.click(
			within(dialog).getByRole("checkbox", {
				name: /I accept losing those writes/,
			}),
		);
		expect(button).toBeEnabled();
		await user.click(button);
		await within(dialog).findByText("Freezing the old primary");
		const real = api.calls.find((c) => isRealPromote(c));
		expect(real?.body).toEqual({
			confirm: "orders-r1",
			acceptDataLoss: true,
			dryRun: false,
		});
	});

	test("a switchover has no data-loss prompt", async () => {
		stubApi({ [PROMOTE]: promoteRoute(promotePlan({ lagBytes: 0 })) });
		const { dialog } = await open();
		await within(dialog).findByText("switchover");
		expect(
			within(dialog).queryByText("Data will be lost"),
		).not.toBeInTheDocument();
		expect(within(dialog).queryByRole("checkbox")).not.toBeInTheDocument();
	});
});
