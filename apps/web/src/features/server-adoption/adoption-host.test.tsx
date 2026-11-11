import { afterEach, describe, expect, setDefaultTimeout, test } from "bun:test";
import {
	act,
	configure,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	AdoptionApplyRequest,
	AdoptionPlan,
	AdoptionResult,
	Project,
} from "#/api/gen/types.gen";
import { serverWire } from "#/features/servers/server.fixture";
import { type ApiStub, stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { AdoptionHost } from "./adoption-host";
import { closeAdoption, openAdoption } from "./store";
import {
	applied,
	DB,
	ENV,
	environment,
	plan,
	SVC,
	service,
} from "./test-fixtures";

// The review debounces its plan by 400 ms and the machine may be busy.
setDefaultTimeout(30_000);
configure({ asyncUtilTimeout: 5_000 });

afterEach(() => act(() => closeAdoption()));

/** A canned reply; the stub holds its status and body to openapi.json. */
interface Reply {
	status: number;
	body: unknown;
}

const planned = (body: AdoptionPlan = plan()): Reply => ({ status: 200, body });
const created = (body: AdoptionResult = applied()): Reply => ({
	status: 201,
	body,
});

interface Setup {
	plan?: AdoptionPlan;
	apply?: AdoptionResult;
	projects?: Project[];
}

/**
 * The server the review talks to. `replies` are the canned replies of the plan
 * and apply routes: a test that wants a later request answered differently
 * assigns the next reply with `answer` (the stub reads it when it answers).
 * `keys` are the Idempotency-Keys of the apply requests, which the stub does
 * not record, and `hold` is a promise the apply request waits for before it
 * reaches the stub, so a test can look at the screen while it is in flight.
 */
function stub(setup: Setup = {}) {
	const replies = {
		plan: planned(setup.plan),
		apply: created(setup.apply),
	};
	const api = stubApi({
		"GET /servers/:serverId": {
			body: serverWire({ id: "srv-1", name: "alpha" }),
		},
		"GET /projects": {
			body: make("PageProject", { items: setup.projects ?? [] }),
		},
		"POST /servers/:serverId/adoption/plan": replies.plan,
		"POST /servers/:serverId/adoption": replies.apply,
	});
	const keys: string[] = [];
	const held: { promise?: Promise<void> } = {};
	const inner = globalThis.fetch;
	globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
		const request = input instanceof Request ? input : undefined;
		const method = (request?.method ?? init?.method ?? "GET").toUpperCase();
		const url = new URL(request?.url ?? String(input), location.href);
		if (method === "POST" && url.pathname.endsWith("/servers/srv-1/adoption")) {
			const headers = new Headers(request?.headers ?? init?.headers);
			keys.push(headers.get("Idempotency-Key") ?? "");
			await held.promise;
		}
		return inner(input, init);
	}) as typeof fetch;
	return Object.assign(api, {
		keys,
		hold(promise: Promise<void>) {
			held.promise = promise;
		},
		answer(route: "plan" | "apply", reply: Reply) {
			Object.assign(replies[route], reply);
		},
	});
}

async function openReview() {
	const view = await renderWithProviders(<AdoptionHost />);
	act(() => openAdoption("srv-1"));
	await screen.findByText("Review workloads on alpha");
	return view;
}

function planCalls(api: ApiStub) {
	return api.calls.filter((c) => c.path === "/servers/srv-1/adoption/plan");
}

const apply = () =>
	screen.getByRole("button", { name: /^(Adopt \d|Apply choices)/ });
const action = (slug: string, label: string) =>
	within(
		screen.getByRole("group", { name: `What to do with ${slug}` }),
	).getByRole("button", { name: label });

/** What the plan is after the web service is left running, the way the control plane answers. */
const leftPlan = () =>
	plan({
		environments: [environment({}, [service({ action: "leave" })])],
		totals: { adopt: 0, leave: 1, remove: 0, containers: 2 },
	});

/** The choices of every apply request the stub has seen. */
function applyChoices(api: ApiStub) {
	return api.calls
		.filter((c) => c.path === "/servers/srv-1/adoption")
		.map((c) => (c.body as AdoptionApplyRequest).choices);
}

describe("opening", () => {
	test("renders nothing until the store opens it, and Cancel closes it", async () => {
		const api = stub();
		const user = userEvent.setup();
		await renderWithProviders(<AdoptionHost />);
		expect(screen.queryByText(/Review workloads/)).not.toBeInTheDocument();
		expect(api.calls).toEqual([]);

		act(() => openAdoption("srv-1"));
		expect(
			await screen.findByText("Review workloads on alpha"),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(screen.queryByText(/Review workloads on/)).not.toBeInTheDocument(),
		);
		expect(api.unmatched).toEqual([]);
	});

	test("plans on open with no choices, and shows the environment, its service and the totals", async () => {
		const api = stub();
		await openReview();
		const env = await screen.findByRole("region", {
			name: "Environment production",
		});
		expect(within(env).getByDisplayValue("web")).toBeInTheDocument();
		expect(within(env).getByText("nginx:1.27")).toBeInTheDocument();
		expect(within(env).getByText("2 replicas")).toBeInTheDocument();
		expect(
			screen.getByText(/Nothing on it is changed until you apply/),
		).toBeInTheDocument();
		expect(
			await screen.findByText("2 containers · 1 to adopt"),
		).toBeInTheDocument();
		expect(apply()).toHaveTextContent("Adopt 1 service");
		expect(apply()).toBeEnabled();
		expect(planCalls(api)).toHaveLength(1);
		expect(planCalls(api)[0]?.body).toEqual({ choices: {} });
		expect(api.unmatched).toEqual([]);
	});

	test("a plan that fails says why and can be tried again", async () => {
		const api = stub();
		api.answer(
			"plan",
			problem(409, {
				title: "Conflict",
				detail: "There is nothing to adopt on this server.",
				code: "nothing_to_adopt",
			}),
		);
		const user = userEvent.setup();
		await openReview();
		expect(
			await screen.findByText(/There is nothing to adopt on this server/),
		).toBeInTheDocument();
		api.answer("plan", planned());
		await user.click(screen.getByRole("button", { name: "Try again" }));
		expect(
			await screen.findByRole("region", { name: "Environment production" }),
		).toBeInTheDocument();
		expect(planCalls(api)).toHaveLength(2);
	});
});

describe("choices", () => {
	test("a choice re-plans a beat later with that choice, and the plan on screen follows", async () => {
		const api = stub();
		const user = userEvent.setup();
		await openReview();
		await screen.findByText("2 containers · 1 to adopt");

		api.answer("plan", planned(leftPlan()));
		await user.click(action("web", "Leave running"));
		expect(action("web", "Leave running")).toHaveAttribute(
			"aria-pressed",
			"true",
		);

		await waitFor(() => expect(planCalls(api)).toHaveLength(2));
		expect(planCalls(api)[1]?.body).toEqual({
			choices: { services: { [SVC]: { action: "leave" } } },
		});
		expect(
			await screen.findByText("2 containers · 1 left running"),
		).toBeInTheDocument();
		expect(apply()).toHaveTextContent("Apply choices");
		expect(
			screen.getByText(/The containers keep running, unmanaged/),
		).toBeInTheDocument();
	});

	test("typing a name or an environment name sends it, once, after the pause", async () => {
		const api = stub();
		const user = userEvent.setup();
		await openReview();
		await screen.findByText("2 containers · 1 to adopt");

		await user.clear(screen.getByLabelText("Name of web"));
		await user.type(screen.getByLabelText("Name of web"), "site");
		await user.clear(screen.getByLabelText("Environment name"));
		await user.type(screen.getByLabelText("Environment name"), "live");

		await waitFor(() => expect(planCalls(api)).toHaveLength(2));
		expect(planCalls(api)[1]?.body).toEqual({
			choices: {
				environments: { [ENV]: { name: "live" } },
				services: { [SVC]: { name: "site" } },
			},
		});
	});

	test("an existing project and the production switch are choices too", async () => {
		const api = stub({
			projects: [make("Project", { id: "p-9", name: "Shop", system: false })],
		});
		const user = userEvent.setup();
		await openReview();
		await screen.findByText("2 containers · 1 to adopt");
		await user.click(screen.getByRole("switch", { name: "Production" }));
		await user.click(screen.getByRole("combobox", { name: "Adopt into" }));
		await user.click(await screen.findByRole("option", { name: "Shop" }));

		await waitFor(() => expect(planCalls(api)).toHaveLength(2));
		expect(planCalls(api)[1]?.body).toEqual({
			choices: {
				environments: { [ENV]: { production: false, projectId: "p-9" } },
			},
		});
		expect(screen.queryByLabelText("New project name")).not.toBeInTheDocument();
	});

	test("a domain can be left out and a variable made secret, from the details", async () => {
		const api = stub({
			plan: plan({
				environments: [
					environment({}, [
						service({
							domains: [
								{ host: "shop.example.com", include: true },
								{
									host: "admin.example.com",
									include: false,
									note: "access rules cannot be read back; the domain is left out",
								},
							],
							env: [{ key: "API_URL", secret: false }],
						}),
					]),
				],
			}),
		});
		const user = userEvent.setup();
		await openReview();
		await screen.findByText("2 containers · 1 to adopt");
		await user.click(screen.getByRole("button", { name: /Details/ }));

		expect(screen.getByLabelText("Adopt admin.example.com")).toHaveAttribute(
			"aria-disabled",
			"true",
		);
		expect(
			screen.getByText(/access rules cannot be read back/),
		).toBeInTheDocument();
		await user.click(screen.getByLabelText("Adopt shop.example.com"));
		await user.click(screen.getByLabelText("API_URL is a secret"));

		await waitFor(() => expect(planCalls(api)).toHaveLength(2));
		expect(planCalls(api)[1]?.body).toEqual({
			choices: {
				services: {
					[SVC]: { skipHosts: ["shop.example.com"], secret: { API_URL: true } },
				},
			},
		});
	});

	test("a service that cannot be adopted says why and offers leave and remove only", async () => {
		stub({
			plan: plan({
				environments: [
					environment({}, [
						service({
							kind: "stack",
							slug: "wiki",
							name: "wiki",
							action: "leave",
							adoptable: false,
							reason: "Compose stacks are not adopted: it keeps running.",
						}),
					]),
				],
				totals: { adopt: 0, leave: 1, remove: 0, containers: 2 },
			}),
		});
		await openReview();
		expect(
			await screen.findByText(/Compose stacks are not adopted/),
		).toBeInTheDocument();
		expect(action("wiki", "Adopt")).toBeDisabled();
		expect(action("wiki", "Leave running")).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(action("wiki", "Remove")).toBeEnabled();
	});

	test("remove says the containers go and the volumes stay", async () => {
		stub();
		const user = userEvent.setup();
		await openReview();
		await screen.findByText("2 containers · 1 to adopt");
		await user.click(action("web", "Remove"));
		expect(
			screen.getByText(
				"The containers are removed after adoption; volumes stay.",
			),
		).toBeInTheDocument();
	});
});

describe("blockers", () => {
	const blocked = plan({
		blockers: ["web belongs to another team", "api: the slug is taken"],
	});

	test("are listed and keep Apply off", async () => {
		stub({ plan: blocked });
		await openReview();
		const alert = await screen.findByText("Fix these 2 to continue");
		expect(alert).toBeInTheDocument();
		expect(screen.getByText("web belongs to another team")).toBeInTheDocument();
		expect(screen.getByText("api: the slug is taken")).toBeInTheDocument();
		expect(apply()).toBeDisabled();
	});

	test("one blocker is worded for one", async () => {
		stub({ plan: plan({ blockers: ["web belongs to another team"] }) });
		await openReview();
		expect(await screen.findByText("Fix this to continue")).toBeInTheDocument();
	});

	test("a plan with nothing to apply keeps Apply off", async () => {
		stub({
			plan: plan({ totals: { adopt: 0, leave: 0, remove: 0, containers: 0 } }),
		});
		await openReview();
		await screen.findByRole("region", { name: "Environment production" });
		await waitFor(() => expect(apply()).toBeDisabled());
		expect(screen.getByRole("status", { name: "" })).toHaveTextContent(
			"Nothing to apply.",
		);
	});

	test("the plan's warnings show as notes", async () => {
		stub({ plan: plan({ warnings: ["The proxy has no routes."] }) });
		await openReview();
		expect(await screen.findByText("A note")).toBeInTheDocument();
		expect(screen.getByText("The proxy has no routes.")).toBeInTheDocument();
	});
});

describe("applying", () => {
	test("sends the choices with an idempotency key and shows the result with counts and links", async () => {
		const api = stub({
			apply: applied({ left: 1 }),
		});
		const user = userEvent.setup();
		await openReview();
		await screen.findByText("2 containers · 1 to adopt");
		await user.click(apply());

		expect(await screen.findByText("Workloads adopted")).toBeInTheDocument();
		expect(applyChoices(api)).toEqual([{}]);
		expect(api.keys[0]).toMatch(/^[0-9a-f-]{36}$/);
		expect(
			within(screen.getByRole("dialog")).getByText(
				"Adopted 1 service, left 1 running unmanaged.",
			),
		).toBeInTheDocument();
		expect(screen.getByRole("link", { name: "web" })).toHaveAttribute(
			"href",
			`/p/p-1/production?service=${SVC}`,
		);
		expect(screen.getByRole("button", { name: "Close" })).toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});

	test("nothing can change while the apply is in flight, and the result still arrives", async () => {
		const release = Promise.withResolvers<void>();
		const api = stub();
		api.hold(release.promise);
		const user = userEvent.setup();
		await openReview();
		await screen.findByText("2 containers · 1 to adopt");
		await user.click(apply());

		await waitFor(() => expect(api.keys).toHaveLength(1));
		expect(screen.getByLabelText("Name of web")).toBeDisabled();
		expect(screen.getByLabelText("Environment name")).toBeDisabled();
		expect(screen.getByRole("switch", { name: "Production" })).toHaveAttribute(
			"aria-disabled",
			"true",
		);
		expect(action("web", "Leave running")).toBeDisabled();
		expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
		expect(
			screen.getByRole("button", { name: "Leave all running unmanaged" }),
		).toBeDisabled();
		expect(screen.getByRole("button", { name: /Adopting/ })).toBeDisabled();

		// A click on a locked segment changes nothing, and Escape does not close.
		await user.click(action("web", "Leave running"));
		await user.keyboard("{Escape}");
		expect(screen.getByRole("dialog")).toBeInTheDocument();

		release.resolve();
		expect(await screen.findByText("Workloads adopted")).toBeInTheDocument();
		expect(api.keys).toHaveLength(1);
		expect(applyChoices(api)).toEqual([{}]);
	});

	test("Leave all running unmanaged applies every service as leave", async () => {
		const base = plan({
			environments: [
				environment({}, [
					service(),
					service({ id: DB, slug: "db", name: "db", kind: "database" }),
				]),
			],
			totals: { adopt: 2, leave: 0, remove: 0, containers: 3 },
		});
		const api = stub({
			plan: base,
			apply: applied({ left: 2, services: null, projects: null }),
		});
		const user = userEvent.setup();
		await openReview();
		await screen.findByText("3 containers · 2 to adopt");
		// Whatever was chosen for a row does not survive: every service is left.
		await user.click(action("web", "Remove"));
		await user.click(
			screen.getByRole("button", { name: "Leave all running unmanaged" }),
		);

		expect(
			await screen.findByText("Workloads left unmanaged"),
		).toBeInTheDocument();
		expect(applyChoices(api)).toEqual([
			{ services: { [SVC]: { action: "leave" }, [DB]: { action: "leave" } } },
		]);
		expect(
			within(screen.getByRole("dialog")).getByText(
				"Left 2 workloads running unmanaged.",
			),
		).toBeInTheDocument();
	});

	test("leaving all is available while blockers stand, since leaving needs none of them", async () => {
		const api = stub({
			plan: plan({ blockers: ["web belongs to another team"] }),
		});
		const user = userEvent.setup();
		await openReview();
		await screen.findByText("Fix this to continue");
		expect(apply()).toBeDisabled();
		await user.click(
			screen.getByRole("button", { name: "Leave all running unmanaged" }),
		);
		await screen.findByText("Workloads adopted");
		expect(applyChoices(api)).toHaveLength(1);
	});

	test("an apply the control plane refuses stays on the review with its reason", async () => {
		const api = stub();
		api.answer(
			"apply",
			problem(422, {
				title: "Unprocessable",
				detail: "The host changed since the review.",
				code: "adoption_blocked",
			}),
		);
		const user = userEvent.setup();
		await openReview();
		await screen.findByText("2 containers · 1 to adopt");
		await user.click(apply());
		expect(await screen.findByText("Nothing was adopted")).toBeInTheDocument();
		expect(
			screen.getByText(/The host changed since the review/),
		).toBeInTheDocument();
		expect(screen.queryByText("Workloads adopted")).not.toBeInTheDocument();
		expect(apply()).toBeEnabled();
	});

	test("a retry of the same request reuses the key; a changed choice gets a new one", async () => {
		const api = stub();
		api.answer(
			"apply",
			problem(503, {
				title: "Unavailable",
				detail: "try later",
				code: "unavailable",
			}),
		);
		const user = userEvent.setup();
		await openReview();
		await screen.findByText("2 containers · 1 to adopt");
		await user.click(apply());
		await screen.findByText("Nothing was adopted");
		await user.click(apply());
		await waitFor(() => expect(api.keys).toHaveLength(2));
		expect(api.keys[1]).toBe(api.keys[0] as string);

		api.answer("plan", planned(leftPlan()));
		api.answer("apply", created());
		await user.click(action("web", "Leave running"));
		await waitFor(() => expect(planCalls(api)).toHaveLength(2));
		await screen.findByText("2 containers · 1 left running");
		await user.click(apply());
		await waitFor(() => expect(api.keys).toHaveLength(3));
		expect(api.keys[2]).not.toBe(api.keys[0]);
	});
});

describe("a server that is no longer held", () => {
	test("explains the workloads run unmanaged", async () => {
		stub({ plan: plan({ held: false }) });
		await openReview();
		expect(
			await screen.findByText(
				/These workloads run on this host without being managed/,
			),
		).toBeInTheDocument();
		expect(
			screen.queryByText(/Nothing on it is changed until you apply/),
		).not.toBeInTheDocument();
	});
});
