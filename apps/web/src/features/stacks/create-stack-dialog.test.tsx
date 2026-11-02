import { afterEach, describe, expect, test } from "bun:test";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Environment } from "#/api/models";
import { registerPlacer } from "#/features/canvas/use-layout-persistence";
import { onOpenStagedReview } from "#/features/deploy-button/open-review";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { CreateStackDialog } from "./create-stack-dialog";
import { createStackActions, useCreateStackState } from "./store";
import { makeServer, makeStack } from "./test-fixtures";

const CHECK = "POST /environments/:environmentId/stacks/check";
const CREATE = "POST /environments/:environmentId/services";
const DEPLOY = "POST /services/:serviceId/deployments";

const COMPOSE = "services:";

const PASSING = {
	components: [{ name: "web", index: 0, image: "nginx", ports: [80] }],
	violations: [],
	notes: [],
	unrestricted: false,
	composeAvailable: true,
};

const DEV: Pick<Environment, "id" | "name" | "isProduction"> = {
	id: "env-1",
	name: "staging",
	isProduction: false,
};
const PROD = { ...DEV, name: "production", isProduction: true };

const SERVERS = [
	makeServer({ id: "srv-off", name: "offline-box", status: "offline" }),
	makeServer({ id: "srv-on", name: "online-box" }),
];

/** The item at `index`; fails the test with a clear message when it is missing. */
function at<T>(items: readonly T[], index: number): T {
	const item = items[index];
	if (item === undefined) throw new Error(`expected an item at index ${index}`);
	return item;
}

afterEach(async () => {
	await act(async () => createStackActions.close());
});

function Harness({
	environment = DEV,
	servers = SERVERS,
}: {
	environment?: typeof DEV;
	servers?: Parameters<typeof CreateStackDialog>[0]["servers"];
}) {
	const state = useCreateStackState();
	return (
		<CreateStackDialog
			open={state.open}
			position={state.position}
			environment={environment}
			servers={servers}
		/>
	);
}

async function openDialog(
	props: Parameters<typeof Harness>[0] = {},
	position?: { x: number; y: number },
) {
	const view = await renderWithProviders(<Harness {...props} />);
	await act(async () => createStackActions.open(position));
	await screen.findByRole("dialog");
	return view;
}

async function fill(
	user: ReturnType<typeof userEvent.setup>,
	name = "shop",
	compose = COMPOSE,
) {
	await user.type(screen.getByLabelText("Name"), name);
	await user.type(screen.getByLabelText("docker-compose.yml"), compose);
}

function createButton(): HTMLButtonElement {
	return screen.getByRole("button", {
		name: "Create stack",
	}) as HTMLButtonElement;
}

/** The wire view the control plane answers a create with. */
function createdBody(name = "shop") {
	return makeStack({ id: "stack-new", name });
}

describe("CreateStackDialog", () => {
	test("renders nothing until it is opened", async () => {
		stubApi({});
		await renderWithProviders(<Harness />);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		await act(async () => createStackActions.open());
		expect(
			await screen.findByRole("heading", { name: "New Compose stack" }),
		).toBeInTheDocument();
	});

	test("starts blocked, asking for a name", async () => {
		stubApi({});
		await openDialog();
		expect(createButton().disabled).toBe(true);
		expect(
			within(screen.getByRole("dialog")).getByText("Give the stack a name."),
		).toBeInTheDocument();
	});

	test("with a name but no compose file it says to add one", async () => {
		const user = userEvent.setup();
		stubApi({});
		await openDialog();
		await user.type(screen.getByLabelText("Name"), "shop");
		expect(screen.getByText("Add a Compose file.")).toBeInTheDocument();
		expect(createButton().disabled).toBe(true);
	});

	test("waits for the live check, then enables Create", async () => {
		const user = userEvent.setup();
		const api = stubApi({ [CHECK]: { body: PASSING } });
		await openDialog();
		await fill(user);
		await waitFor(() => expect(createButton().disabled).toBe(false), {
			timeout: 3000,
		});
		const sent = api.calls.filter((call) => call.method === "POST");
		expect(sent.at(-1)?.path).toBe("/environments/env-1/stacks/check");
		// The online server is chosen by default, and the check judged against it.
		expect(sent.at(-1)?.body).toEqual({ compose: COMPOSE, serverId: "srv-on" });
		expect(screen.getByRole("radio", { name: /online-box/ })).toBeChecked();
	});

	test("a Compose file with problems blocks Create and says why", async () => {
		const user = userEvent.setup();
		stubApi({
			[CHECK]: {
				body: {
					...PASSING,
					violations: [
						{
							service: "web",
							key: "privileged",
							message: "Privileged containers are not supported.",
							kind: "unsupported",
						},
					],
				},
			},
		});
		await openDialog();
		await fill(user);
		const dialog = within(screen.getByRole("dialog"));
		await waitFor(
			() =>
				expect(
					dialog.getAllByText("1 problem: 1 not supported.").length,
				).toBeGreaterThan(1),
			{ timeout: 3000 },
		);
		expect(createButton().disabled).toBe(true);
	});

	test("creates the stack with the typed name, compose text and server", async () => {
		const user = userEvent.setup();
		let key: string | null = null;
		const api = stubApi({
			[CHECK]: { body: PASSING },
			[CREATE]: (request) => {
				key = request.headers.get("Idempotency-Key");
				return Response.json(createdBody());
			},
		});
		await openDialog();
		await fill(user, "  shop  ");
		await waitFor(() => expect(createButton().disabled).toBe(false), {
			timeout: 3000,
		});
		await user.click(createButton());
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);
		const call = api.calls.find((c) => c.path.endsWith("/services"));
		expect(call).toMatchObject({
			method: "POST",
			path: "/environments/env-1/services",
			body: {
				name: "shop",
				kind: "stack",
				sourceType: "compose",
				serverId: "srv-on",
				config: { stack: { compose: COMPOSE } },
			},
		});
		expect(key).toMatch(/^[0-9a-f-]{36}$/);
		expect(api.calls.some((c) => c.path.endsWith("/deployments"))).toBe(false);
		// The toast says nothing runs yet and offers to deploy.
		expect(await screen.findByText("Created shop")).toBeInTheDocument();
		expect(
			screen.getByText("Nothing runs until it is deployed."),
		).toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});

	test("the toast's Deploy now starts the first deployment", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			[CHECK]: { body: PASSING },
			[CREATE]: () => Response.json(createdBody()),
			[DEPLOY]: {
				status: 202,
				body: make("DeploymentView", {
					serviceId: "stack-new",
					status: "queued",
				}),
			},
		});
		await openDialog();
		await fill(user);
		await waitFor(() => expect(createButton().disabled).toBe(false), {
			timeout: 3000,
		});
		await user.click(createButton());
		await user.click(await screen.findByRole("button", { name: "Deploy now" }));
		await waitFor(() =>
			expect(api.calls.some((c) => c.path.endsWith("/deployments"))).toBe(true),
		);
		expect(api.calls.find((c) => c.path.endsWith("/deployments"))?.path).toBe(
			"/services/stack-new/deployments",
		);
	});

	test("Deploy now deploys straight away and says so", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			[CHECK]: { body: PASSING },
			[CREATE]: () => Response.json(createdBody()),
			[DEPLOY]: {
				status: 202,
				body: make("DeploymentView", {
					serviceId: "stack-new",
					status: "queued",
				}),
			},
		});
		await openDialog();
		await fill(user);
		await user.click(screen.getByRole("switch", { name: "Deploy now" }));
		await waitFor(() => expect(createButton().disabled).toBe(false), {
			timeout: 3000,
		});
		await user.click(createButton());
		expect(await screen.findByText("Deploying shop")).toBeInTheDocument();
		expect(
			screen.getByText("Follow it on its card or under Deployments."),
		).toBeInTheDocument();
		expect(
			api.calls.map((c) => c.path).filter((p) => p.includes("services")),
		).toEqual([
			"/environments/env-1/services",
			"/services/stack-new/deployments",
		]);
		expect(screen.queryByText("Created shop")).not.toBeInTheDocument();
	});

	test("a failed first deploy is reported with its reason, the stack stays created", async () => {
		const user = userEvent.setup();
		stubApi({
			[CHECK]: { body: PASSING },
			[CREATE]: () => Response.json(createdBody()),
			[DEPLOY]: problem(409, {
				title: "Conflict",
				status: 409,
				detail: "Server is draining.",
			}),
		});
		await openDialog();
		await fill(user);
		await user.click(screen.getByRole("switch", { name: "Deploy now" }));
		await waitFor(() => expect(createButton().disabled).toBe(false), {
			timeout: 3000,
		});
		await user.click(createButton());
		expect(
			await screen.findByText("Could not deploy shop"),
		).toBeInTheDocument();
		expect(screen.getByText(/Server is draining\./)).toBeInTheDocument();
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);
	});

	test("in production the stack is only staged: Deploy now is off and the toast offers the review", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			[CHECK]: { body: PASSING },
			[CREATE]: () => Response.json(createdBody()),
		});
		await openDialog({ environment: PROD });
		const toggle = screen.getByRole("switch", { name: "Deploy now" });
		expect(toggle).toHaveAttribute("aria-disabled", "true");
		expect(
			screen.getByText(
				"production is production: the stack is staged and you review it before it deploys.",
			),
		).toBeInTheDocument();
		await fill(user);
		await waitFor(() => expect(createButton().disabled).toBe(false), {
			timeout: 3000,
		});
		let reviews = 0;
		const off = onOpenStagedReview(() => {
			reviews += 1;
		});
		await user.click(createButton());
		expect(
			await screen.findByText(
				"Staged in production. Nothing runs until you review and deploy.",
			),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Review & deploy" }));
		off();
		expect(reviews).toBe(1);
		expect(api.calls.some((c) => c.path.endsWith("/deployments"))).toBe(false);
	});

	test("a rejected create shows the server's message and keeps the dialog open", async () => {
		const user = userEvent.setup();
		stubApi({
			[CHECK]: { body: PASSING },
			[CREATE]: problem(409, {
				title: "Conflict",
				status: 409,
				detail: "Name already taken.",
			}),
		});
		await openDialog();
		await fill(user);
		await waitFor(() => expect(createButton().disabled).toBe(false), {
			timeout: 3000,
		});
		await user.click(createButton());
		expect(await screen.findByText(/Name already taken\./)).toBeInTheDocument();
		expect(screen.getByRole("dialog")).toBeInTheDocument();
		expect(screen.getByLabelText("Name")).toHaveValue("shop");
	});

	test("places the new card where it was dropped", async () => {
		const user = userEvent.setup();
		const placed: unknown[] = [];
		const unregister = registerPlacer((id, position) => {
			placed.push([id, position]);
		});
		try {
			stubApi({
				[CHECK]: { body: PASSING },
				[CREATE]: () => Response.json(createdBody()),
			});
			await openDialog({}, { x: 120, y: 80 });
			await fill(user);
			await waitFor(() => expect(createButton().disabled).toBe(false), {
				timeout: 3000,
			});
			await user.click(createButton());
			await waitFor(() => expect(placed).toHaveLength(1));
			expect(placed[0]).toEqual(["stack-new", { x: 120, y: 80 }]);
		} finally {
			unregister();
		}
	});

	test("warns when the drop position could not be saved", async () => {
		const user = userEvent.setup();
		const unregister = registerPlacer(() => {
			throw new Error("offline");
		});
		try {
			stubApi({
				[CHECK]: { body: PASSING },
				[CREATE]: () => Response.json(createdBody()),
			});
			await openDialog({}, { x: 1, y: 2 });
			await fill(user);
			await waitFor(() => expect(createButton().disabled).toBe(false), {
				timeout: 3000,
			});
			await user.click(createButton());
			expect(
				await screen.findByText("shop was placed automatically"),
			).toBeInTheDocument();
			expect(
				screen.getByText(
					"Its position could not be saved where you dropped it.",
				),
			).toBeInTheDocument();
			// The stack was still created.
			expect(await screen.findByText("Created shop")).toBeInTheDocument();
		} finally {
			unregister();
		}
	});

	test("without a drop position nothing is placed", async () => {
		const user = userEvent.setup();
		const placed: unknown[] = [];
		const unregister = registerPlacer((id) => placed.push(id));
		try {
			stubApi({
				[CHECK]: { body: PASSING },
				[CREATE]: () => Response.json(createdBody()),
			});
			await openDialog();
			await fill(user);
			await waitFor(() => expect(createButton().disabled).toBe(false), {
				timeout: 3000,
			});
			await user.click(createButton());
			await screen.findByText("Created shop");
			expect(placed).toEqual([]);
		} finally {
			unregister();
		}
	});

	test("Cancel closes the dialog without creating anything", async () => {
		const user = userEvent.setup();
		const api = stubApi({});
		await openDialog();
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);
		expect(api.calls).toEqual([]);
	});

	test("pressing Enter while blocked does not create", async () => {
		const user = userEvent.setup();
		const api = stubApi({});
		await openDialog();
		await user.type(screen.getByLabelText("Name"), "shop{Enter}");
		expect(api.calls).toEqual([]);
		expect(screen.getByRole("dialog")).toBeInTheDocument();
	});

	test("a reopened dialog starts from a blank form", async () => {
		const user = userEvent.setup();
		stubApi({});
		await openDialog();
		await user.type(screen.getByLabelText("Name"), "leftover");
		await act(async () => createStackActions.close());
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);
		await act(async () => createStackActions.open());
		await screen.findByRole("dialog");
		expect(screen.getByLabelText("Name")).toHaveValue("");
	});

	test("lists only servers that can take a stack and lets the user pick one", async () => {
		const user = userEvent.setup();
		const api = stubApi({ [CHECK]: { body: PASSING } });
		await openDialog({
			servers: [
				makeServer({ id: "srv-rev", name: "revoked-box", status: "revoked" }),
				makeServer({ id: "srv-pen", name: "pending-box", status: "pending" }),
				...SERVERS,
			],
		});
		const group = screen.getByRole("radiogroup", { name: "Server" });
		expect(within(group).getAllByRole("radio")).toHaveLength(2);
		expect(
			within(group).getByRole("radio", { name: /offline-box/ }),
		).toBeDefined();
		expect(
			within(group).getByRole("radio", { name: /online-box/ }),
		).toBeDefined();
		expect(within(group).queryByText(/revoked-box|pending-box/)).toBeNull();
		await user.click(within(group).getByRole("radio", { name: /offline-box/ }));
		expect(
			within(group).getByRole("radio", { name: /offline-box/ }),
		).toBeChecked();
		await fill(user);
		await waitFor(
			() =>
				expect(api.calls.at(-1)?.body).toEqual({
					compose: COMPOSE,
					serverId: "srv-off",
				}),
			{ timeout: 3000 },
		);
	});

	test("falls back to an offline server when none is online", async () => {
		stubApi({});
		await openDialog({ servers: [at(SERVERS, 0)] });
		expect(screen.getByRole("radio", { name: /offline-box/ })).toBeChecked();
	});

	test("an unschedulable server is skipped as the default", async () => {
		stubApi({});
		await openDialog({
			servers: [
				makeServer({
					id: "srv-drain",
					name: "draining-box",
					drainState: "draining",
				}),
				at(SERVERS, 1),
			],
		});
		expect(screen.getByRole("radio", { name: /online-box/ })).toBeChecked();
		expect(screen.getByRole("radio", { name: /draining-box/ })).toBeDisabled();
	});

	test("with no server it points to enrolling one and blocks Create", async () => {
		const user = userEvent.setup();
		stubApi({});
		await openDialog({ servers: [] });
		expect(screen.getByText(/No server is enrolled yet\./)).toBeInTheDocument();
		const link = screen.getByRole("link", { name: "Add a server" });
		expect(link).toHaveAttribute("href", expect.stringContaining("/servers"));
		await fill(user);
		expect(createButton().disabled).toBe(true);
		await user.click(link);
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);
	});
});
