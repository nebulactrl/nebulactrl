import { describe, expect, test } from "bun:test";
import { useSearch } from "@tanstack/react-router";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { CombinePlan, CombineView } from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { makeService } from "#/features/canvas/lib/fixtures";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { CombineDialog } from "./combine-dialog";

const shop = makeService({
	id: "web",
	name: "Shop",
	slug: "shop",
	sourceType: "git",
	config: {
		...makeService().config,
		port: 3000,
		gitUrl: "https://github.com/acme/shop",
	},
	domains: [
		{ id: "d1", hostname: "shop.acme.com" } as Service["domains"][number],
	],
});
const worker = makeService({
	id: "wk",
	name: "Shop worker",
	slug: "shop-worker",
	sourceType: "git",
	config: {
		...makeService().config,
		port: 0,
		gitUrl: "https://github.com/acme/shop",
	},
});
const SERVICES = [shop, worker];

function plan(over: Partial<CombinePlan> = {}): CombinePlan {
	return {
		fingerprint: "fp1",
		targetServiceId: "web",
		blocking: null,
		domainMoves: null,
		taskMoves: null,
		settingsIgnored: null,
		warnings: null,
		variables: { sameCount: 3, onlyIn: null, conflicts: null },
		members: [
			{
				serviceId: "web",
				slug: "shop",
				name: "Shop",
				kind: "app",
				generation: 1,
				port: 3000,
				command: null,
				domains: [{ id: "d1", hostname: "shop.acme.com" }],
				hasStagedChanges: false,
				suggestedProcessName: "",
				isTarget: true,
			},
			{
				serviceId: "wk",
				slug: "shop-worker",
				name: "Shop worker",
				kind: "worker",
				generation: 1,
				port: 0,
				command: ["node", "worker.js"],
				domains: null,
				hasStagedChanges: false,
				suggestedProcessName: "worker",
				isTarget: false,
			},
		],
		...over,
	} as CombinePlan;
}

function view(over: Partial<CombineView> = {}): CombineView {
	return {
		id: "c1",
		environmentId: "env1",
		targetServiceId: "web",
		sourceServiceIds: ["wk"],
		state: "deploying",
		deploymentId: "",
		error: "",
		createdAt: "2026-10-31T10:00:00Z",
		finishedAt: null,
		...over,
	};
}

const PLAN_ROUTE = "POST /environments/env1/services/combine/plan";
const START_ROUTE = "POST /environments/env1/services/combine";

function Probe() {
	const search = useSearch({ strict: false }) as Record<string, string>;
	return <p data-testid="search">{JSON.stringify(search)}</p>;
}

async function mount(
	props: { services?: readonly Service[] | null; isProduction?: boolean } = {},
) {
	const closed = { count: 0 };
	const result = await renderWithProviders(
		<>
			<Probe />
			<CombineDialog
				services={props.services === undefined ? SERVICES : props.services}
				environmentId="env1"
				isProduction={props.isProduction ?? false}
				onClose={() => {
					closed.count++;
				}}
			/>
		</>,
	);
	return { ...result, closed };
}

const continueButton = () => screen.getByRole("button", { name: /Continue/ });

/**
 * Waits until Continue is enabled. A failing `expect(button).toBeEnabled()`
 * inside `waitFor` serialises the element on every retry, which costs seconds
 * in happy-dom, so the poll throws a plain error instead.
 */
const continueEnabled = () =>
	waitFor(() => {
		if (continueButton().hasAttribute("disabled")) {
			throw new Error("Continue is still disabled");
		}
	});

/** Walks Services -> Review and confirms with the typed target slug. */
async function startCombine(user: ReturnType<typeof userEvent.setup>) {
	await continueEnabled();
	await user.click(continueButton());
	await user.click(
		await screen.findByRole("button", { name: "Combine services…" }),
	);
	const dialog = await screen.findByRole("alertdialog");
	await user.type(within(dialog).getByRole("textbox"), "shop");
	await user.click(
		within(dialog).getByRole("button", { name: "Combine services" }),
	);
}

describe("CombineDialog: services step", () => {
	test("renders nothing while closed", async () => {
		const api = stubApi({});
		await mount({ services: null });
		expect(screen.queryByText("Combine into one service")).toBeNull();
		expect(api.calls).toEqual([]);
	});

	test("lists the services, keeps the one with a domain, and asks the plan for them", async () => {
		const api = stubApi({ [PLAN_ROUTE]: { body: plan() } });
		await mount();
		expect(
			await screen.findByText("Combine into one service"),
		).toBeInTheDocument();
		expect(screen.getByText(":3000 · shop.acme.com")).toBeInTheDocument();
		expect(screen.getByText("no port")).toBeInTheDocument();
		expect(
			screen.getByRole("radio", { name: /Shop worker/ }),
		).not.toBeChecked();
		expect(
			screen.getByRole("radio", { name: /^Shop\b(?! worker)/ }),
		).toBeChecked();
		// the other service becomes a process named after its slug minus the prefix
		expect(screen.getByLabelText("Process name for Shop worker")).toHaveValue(
			"worker",
		);
		expect(screen.getByText(/runs its own command as the/)).toHaveTextContent(
			"runs its own command as the web process",
		);
		await continueEnabled();
		expect(api.calls[0]).toMatchObject({
			method: "POST",
			path: "/environments/env1/services/combine/plan",
			body: { serviceIds: ["web", "wk"], targetServiceId: "web" },
		});
		expect(screen.getAllByRole("listitem").map((li) => li.textContent)).toEqual(
			expect.arrayContaining(["1 Services", "·2 Review", "·3 Progress"]),
		);
	});

	test("Continue waits for the plan", async () => {
		stubApi({ [PLAN_ROUTE]: () => new Promise<Response>(() => {}) });
		await mount();
		await screen.findByText("Combine into one service");
		expect(continueButton()).toBeDisabled();
	});

	test("Cancel closes the dialog", async () => {
		const user = userEvent.setup();
		stubApi({ [PLAN_ROUTE]: { body: plan() } });
		const { closed } = await mount();
		await user.click(await screen.findByRole("button", { name: "Cancel" }));
		expect(closed.count).toBe(1);
	});

	test("a bad or duplicate process name blocks Continue and says why", async () => {
		const user = userEvent.setup();
		stubApi({ [PLAN_ROUTE]: { body: plan() } });
		await mount();
		const input = await screen.findByLabelText("Process name for Shop worker");
		await continueEnabled();

		await user.clear(input);
		expect(await screen.findByText("Name is required.")).toBeInTheDocument();
		expect(continueButton()).toBeDisabled();

		await user.type(input, "Bad Name");
		expect(
			screen.getByText(/Use 1 to 31 lowercase letters/),
		).toBeInTheDocument();
		expect(input).toHaveAttribute("aria-invalid", "true");
		expect(continueButton()).toBeDisabled();

		await user.clear(input);
		await user.type(input, "web");
		expect(
			screen.getByText(
				"web is the name of the default process. Pick another name.",
			),
		).toBeInTheDocument();

		await user.clear(input);
		await user.type(input, "queue");
		expect(screen.queryByText(/Pick another name/)).toBeNull();
		expect(continueButton()).toBeEnabled();
	});

	test("choosing another service to stay re-plans with that target", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			[PLAN_ROUTE]: (req) =>
				req
					.clone()
					.json()
					.then((body: { targetServiceId?: string }) =>
						Response.json(
							body.targetServiceId === "wk"
								? plan({ targetServiceId: "wk" })
								: plan(),
						),
					),
		});
		await mount();
		await screen.findByLabelText("Process name for Shop worker");
		await user.click(screen.getByRole("radio", { name: /Shop worker/ }));
		expect(
			await screen.findByLabelText("Process name for Shop"),
		).toBeInTheDocument();
		expect(screen.queryByLabelText("Process name for Shop worker")).toBeNull();
		await waitFor(() =>
			expect(
				api.calls.some(
					(c) =>
						(c.body as { targetServiceId?: string }).targetServiceId === "wk",
				),
			).toBe(true),
		);
	});

	test("blocking reasons are listed with their fix, and Continue stays off", async () => {
		stubApi({
			[PLAN_ROUTE]: {
				body: plan({
					blocking: [
						{
							code: "server_mismatch",
							message: "Shop and Shop worker run on different servers.",
						},
					],
				}),
			},
		});
		await mount();
		expect(
			await screen.findByText("These services cannot be combined yet"),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"Shop and Shop worker run on different servers. Move the services to the same server, then try again.",
			),
		).toBeInTheDocument();
		expect(continueButton()).toBeDisabled();
	});

	test("a plan that fails to load is reported", async () => {
		stubApi({
			[PLAN_ROUTE]: problem(500, { detail: "plan engine down" }),
		});
		await mount();
		expect(
			await screen.findByText("Could not check these services"),
		).toBeInTheDocument();
		expect(screen.getByText(/plan engine down/)).toBeInTheDocument();
		expect(continueButton()).toBeDisabled();
	});
});

describe("CombineDialog: variables step", () => {
	const conflicting = () =>
		plan({
			variables: {
				sameCount: 2,
				onlyIn: [{ key: "QUEUE_URL", serviceId: "wk", secret: false }],
				conflicts: [
					{
						key: "LOG_LEVEL",
						secret: false,
						scope: "runtime",
						options: [
							{ serviceId: "web", slug: "shop", preview: "info", sameAs: null },
							{
								serviceId: "wk",
								slug: "shop-worker",
								preview: "debug",
								sameAs: null,
							},
						],
					},
					{
						key: "API_KEY",
						secret: true,
						scope: "both",
						options: [
							{ serviceId: "web", slug: "shop", preview: "", sameAs: null },
							{
								serviceId: "wk",
								slug: "shop-worker",
								preview: "",
								sameAs: null,
							},
						],
					},
				],
			},
		});

	test("conflicts add a step that blocks Continue until each has a value", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			[PLAN_ROUTE]: { body: conflicting() },
			[START_ROUTE]: { status: 202, body: view() },
			"GET /combines/c1": { body: view() },
		});
		await mount();
		await continueEnabled();
		expect(screen.getByText(/2 Variables/)).toBeInTheDocument();
		await user.click(continueButton());

		expect(
			await screen.findByText(/2 variables are the same everywhere\. 2 differ/),
		).toBeInTheDocument();
		expect(screen.getByText("LOG_LEVEL")).toBeInTheDocument();
		expect(screen.getByText("API_KEY")).toBeInTheDocument();
		expect(screen.getByText("Kept as is")).toBeInTheDocument();
		expect(screen.getByText("QUEUE_URL")).toBeInTheDocument();
		expect(continueButton()).toBeDisabled();

		await user.click(
			screen.getByRole("button", { name: "Use shop for every key" }),
		);
		await continueEnabled();
		await user.click(continueButton());

		await user.click(
			await screen.findByRole("button", { name: "Combine services…" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.type(within(dialog).getByRole("textbox"), "shop");
		await user.click(
			within(dialog).getByRole("button", { name: "Combine services" }),
		);
		await screen.findByRole("list", { name: "Combine progress" });
		const start = api.calls.find((c) => c.path.endsWith("/services/combine"));
		expect(start?.body).toMatchObject({
			fingerprint: "fp1",
			targetServiceId: "web",
			serviceIds: ["web", "wk"],
			processNames: { wk: "worker" },
			variableChoices: { LOG_LEVEL: "web", API_KEY: "web" },
			stopSourcesFirst: false,
		});
	});

	test("Back from Variables returns to Services", async () => {
		const user = userEvent.setup();
		stubApi({ [PLAN_ROUTE]: { body: conflicting() } });
		await mount();
		await continueEnabled();
		await user.click(continueButton());
		await user.click(await screen.findByRole("button", { name: "Back" }));
		expect(await screen.findByText("Service that stays")).toBeInTheDocument();
	});
});

describe("CombineDialog: review step", () => {
	test("lists processes, moves, ignored settings, warnings and what gets deleted", async () => {
		const user = userEvent.setup();
		stubApi({
			[PLAN_ROUTE]: {
				body: plan({
					warnings: ["Shop worker has staged changes that will ship."],
					domainMoves: [
						{
							domainId: "d2",
							hostname: "jobs.acme.com",
							fromServiceId: "wk",
							process: "worker",
						},
					],
					taskMoves: [{ taskId: "t1", name: "nightly", fromServiceId: "wk" }],
					settingsIgnored: [
						{ serviceId: "wk", field: "config.port", note: "Shop's port wins" },
					],
				}),
			},
		});
		await mount();
		await continueEnabled();
		// a typed name carries over to the review and to the domain move
		const input = screen.getByLabelText("Process name for Shop worker");
		await user.clear(input);
		await user.type(input, "jobs");
		await user.click(continueButton());

		expect(await screen.findByText("Processes of Shop")).toBeInTheDocument();
		expect(screen.getByText("Worth knowing")).toBeInTheDocument();
		expect(
			screen.getByText("Shop worker has staged changes that will ship."),
		).toBeInTheDocument();
		const processes = screen.getByText("Processes of Shop")
			.parentElement as HTMLElement;
		expect(processes).toHaveTextContent(
			"web runs the command of shop (stays as it is)",
		);
		expect(processes).toHaveTextContent("jobs runs the command of shop-worker");
		const domains = screen.getByText("Domains that move")
			.parentElement as HTMLElement;
		expect(domains).toHaveTextContent("jobs.acme.com goes to the jobs process");
		expect(screen.getByText("Tasks that move").parentElement).toHaveTextContent(
			"nightly from shop-worker",
		);
		expect(
			screen.getByText("Settings not carried over (Shop wins)").parentElement,
		).toHaveTextContent("shop-worker config.port: Shop's port wins");
		expect(
			screen.getByText("Deleted once the deployment succeeds").parentElement,
		).toHaveTextContent("shop-worker");

		await user.click(screen.getByRole("button", { name: "Back" }));
		expect(
			await screen.findByLabelText("Process name for Shop worker"),
		).toHaveValue("jobs");
	});

	test("omits the optional sections when the plan has nothing to move", async () => {
		const user = userEvent.setup();
		stubApi({ [PLAN_ROUTE]: { body: plan() } });
		await mount();
		await continueEnabled();
		await user.click(continueButton());
		await screen.findByText("Processes of Shop");
		for (const title of [
			"Domains that move",
			"Tasks that move",
			"Worth knowing",
		]) {
			expect(screen.queryByText(title)).toBeNull();
		}
	});
});

describe("CombineDialog: confirming and progress", () => {
	test("the confirmation needs the target's slug typed, then starts the combine", async () => {
		const user = userEvent.setup();
		let key: string | null = null;
		const api = stubApi({
			[PLAN_ROUTE]: { body: plan() },
			[START_ROUTE]: (req) => {
				key = req.headers.get("Idempotency-Key");
				return Response.json(view({ state: "staged" }));
			},
			"GET /combines/c1": { body: view({ state: "staged" }) },
		});
		await mount();
		await continueEnabled();
		await user.click(continueButton());
		await user.click(
			await screen.findByRole("button", { name: "Combine services…" }),
		);

		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Combine 2 services into Shop?"),
		).toBeInTheDocument();
		expect(
			within(dialog).queryByText("This environment is production."),
		).toBeNull();
		const confirm = within(dialog).getByRole("button", {
			name: "Combine services",
		});
		expect(confirm).toBeDisabled();
		await user.type(within(dialog).getByRole("textbox"), "shop");
		expect(confirm).toBeEnabled();
		await user.click(confirm);

		const steps = await screen.findByRole("list", { name: "Combine progress" });
		expect(
			within(steps)
				.getAllByRole("listitem")
				.map((li) => li.textContent),
		).toEqual([
			"StagedIn progress",
			"DeployingWaiting",
			"CutoverWaiting",
			"DoneWaiting",
		]);
		expect(
			screen.getByText(
				/Closing this window does not stop it\. Follow the deployment/,
			),
		).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Close" })).toBeInTheDocument();
		expect(key).toMatch(/^[0-9a-f-]{36}$/);
		const start = api.calls.find(
			(c) => c.method === "POST" && c.path.endsWith("/services/combine"),
		);
		expect(start?.body).toMatchObject({ stopSourcesFirst: false });
	});

	test("Stop the others first is sent with the request", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			[PLAN_ROUTE]: { body: plan() },
			[START_ROUTE]: { status: 202, body: view() },
			"GET /combines/c1": { body: view() },
		});
		await mount();
		await user.click(
			await screen.findByRole("checkbox", { name: "Stop the others first" }),
		);
		await startCombine(user);
		await screen.findByRole("list", { name: "Combine progress" });
		const start = api.calls.find(
			(c) => c.method === "POST" && c.path.endsWith("/services/combine"),
		);
		expect(start?.body).toMatchObject({ stopSourcesFirst: true });
	});

	test("production is named in the confirmation", async () => {
		const user = userEvent.setup();
		stubApi({ [PLAN_ROUTE]: { body: plan() } });
		await mount({ isProduction: true });
		await continueEnabled();
		await user.click(continueButton());
		await user.click(
			await screen.findByRole("button", { name: "Combine services…" }),
		);
		expect(
			await screen.findByText("This environment is production."),
		).toBeInTheDocument();
	});

	test("a start that fails keeps the confirmation open with the reason", async () => {
		const user = userEvent.setup();
		stubApi({
			[PLAN_ROUTE]: { body: plan() },
			[START_ROUTE]: problem(500, { detail: "agent unreachable" }),
		});
		await mount();
		await startCombine(user);
		expect(await screen.findByText("agent unreachable")).toBeInTheDocument();
		expect(screen.queryByRole("list", { name: "Combine progress" })).toBeNull();
	});

	test("a stale plan is refreshed and the reader is sent back to review", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			[PLAN_ROUTE]: { body: plan() },
			[START_ROUTE]: problem(409, {
				code: "plan_stale",
				detail: "plan changed",
			}),
		});
		await mount();
		await startCombine(user);
		expect(
			await screen.findByText(/The services changed while you were reviewing/),
		).toBeInTheDocument();
		expect(await screen.findByText("Processes of Shop")).toBeInTheDocument();
		expect(screen.queryByRole("alertdialog")).toBeNull();
		expect(screen.queryByRole("list", { name: "Combine progress" })).toBeNull();
		const plans = api.calls.filter((c) => c.path.endsWith("/combine/plan"));
		expect(plans.length).toBeGreaterThanOrEqual(2);
	});

	test("a finished combine announces itself and closes the dialog", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			[PLAN_ROUTE]: { body: plan() },
			[START_ROUTE]: {
				status: 202,
				body: view({ state: "done", finishedAt: "2026-10-31T10:05:00Z" }),
			},
			"GET /combines/c1": { body: view({ state: "done" }) },
		});
		const { closed } = await mount();
		await startCombine(user);
		expect(await screen.findByText("Services combined")).toBeInTheDocument();
		expect(screen.getByText("Shop now runs 2 processes.")).toBeInTheDocument();
		await waitFor(() => expect(closed.count).toBe(1));
		expect(api.unmatched).toEqual([]);
	});

	test("View deployment opens the target's Deployments tab and closes the dialog", async () => {
		const user = userEvent.setup();
		stubApi({
			[PLAN_ROUTE]: { body: plan() },
			[START_ROUTE]: {
				status: 202,
				body: view({ state: "deploying", deploymentId: "dep1" }),
			},
			"GET /combines/c1": {
				body: view({ state: "deploying", deploymentId: "dep1" }),
			},
		});
		const { closed } = await mount();
		await startCombine(user);
		await user.click(
			await screen.findByRole("button", { name: "View deployment" }),
		);
		expect(closed.count).toBe(1);
		await waitFor(() =>
			expect(
				JSON.parse(screen.getByTestId("search").textContent ?? "{}"),
			).toMatchObject({
				service: "web",
				tab: "deployments",
			}),
		);
	});

	test("a failed combine can be retried", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			[PLAN_ROUTE]: { body: plan() },
			[START_ROUTE]: {
				status: 202,
				body: view({ state: "failed", error: "build exploded" }),
			},
			"GET /combines/c1": {
				body: view({ state: "failed", error: "build exploded" }),
			},
			"POST /combines/c1/retry": {
				status: 202,
				body: view({ state: "deploying", deploymentId: "dep2" }),
			},
		});
		await mount();
		await startCombine(user);
		expect(await screen.findByText("The combine failed")).toBeInTheDocument();
		expect(screen.getByText("build exploded")).toBeInTheDocument();
		expect(
			screen.getByText(/Nothing was deleted and no domain moved/),
		).toBeInTheDocument();
		// staging failed: the first step is the failed one
		expect(
			within(
				screen.getByRole("list", { name: "Combine progress" }),
			).getAllByRole("listitem")[0],
		).toHaveTextContent("StagedFailed");

		await user.click(screen.getByRole("button", { name: "Retry" }));
		await waitFor(() =>
			expect(screen.queryByText("The combine failed")).toBeNull(),
		);
		expect(screen.getByRole("button", { name: "Close" })).toBeInTheDocument();
		expect(
			api.calls.some(
				(c) => c.path === "/combines/c1/retry" && c.method === "POST",
			),
		).toBe(true);
	});

	test("a failed combine with no reason says so, and Abort leaves everything as it was", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			[PLAN_ROUTE]: { body: plan() },
			[START_ROUTE]: {
				status: 202,
				body: view({ state: "failed", error: "", deploymentId: "dep1" }),
			},
			"GET /combines/c1": {
				body: view({ state: "failed", error: "", deploymentId: "dep1" }),
			},
			"POST /combines/c1/abort": { body: view({ state: "aborted" }) },
		});
		await mount();
		await startCombine(user);
		expect(
			await screen.findByText("No reason was reported."),
		).toBeInTheDocument();
		await user.click(
			screen.getByRole("button", {
				name: "Abort and keep everything as it was",
			}),
		);
		expect(await screen.findByText("Combine aborted")).toBeInTheDocument();
		expect(screen.getByText(/Everything is as it was/)).toBeInTheDocument();
		expect(screen.queryByText("The combine failed")).toBeNull();
		expect(api.calls.some((c) => c.path === "/combines/c1/abort")).toBe(true);
	});

	test("a retry that fails shows why and keeps the choices", async () => {
		const user = userEvent.setup();
		stubApi({
			[PLAN_ROUTE]: { body: plan() },
			[START_ROUTE]: {
				status: 202,
				body: view({ state: "failed", error: "build exploded" }),
			},
			"GET /combines/c1": {
				body: view({ state: "failed", error: "build exploded" }),
			},
			"POST /combines/c1/retry": problem(409, {
				detail: "another combine is running",
			}),
		});
		await mount();
		await startCombine(user);
		await user.click(await screen.findByRole("button", { name: "Retry" }));
		expect(
			await screen.findByText("another combine is running"),
		).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Retry" })).toBeEnabled();
	});
});
