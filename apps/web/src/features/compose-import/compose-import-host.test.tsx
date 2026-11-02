import { afterEach, describe, expect, setDefaultTimeout, test } from "bun:test";
import {
	act,
	fireEvent,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	ComposePlan,
	ComposePlanRequest,
	Server,
} from "#/api/gen/types.gen";
import {
	type Landing,
	registerLanding,
} from "#/features/canvas/canvas-landing";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { ComposeImportHost } from "./compose-import-host";
import { composeImportActions, useComposeImportState } from "./store";
import {
	component,
	envRow,
	plan,
	service,
	stackModePlan,
	stackPlan,
} from "./test-fixtures";

// Every step re-plans after a 300 ms debounce, and a test walks up to three.
setDefaultTimeout(30_000);

// The landing registry is module state: a canvas left registered would answer
// landOnCanvas in every later file.
const landingReleases: Array<() => void> = [];
afterEach(() => {
	for (const release of landingReleases.splice(0)) release();
	return act(() => composeImportActions.close());
});

const PLAN = "POST /environments/:environmentId/compose-imports/plan";
const IMPORT = "POST /environments/:environmentId/compose-imports";
const CANVAS = "GET /projects/:projectId/environments/:environmentId/canvas";
const COMPOSE = "services:\n  web:\n    image: nginx\n";

const environment = make("Environment", {
	id: "e1",
	slug: "prod",
	name: "Production",
	isProduction: true,
	kind: "standard",
	projectId: "p1",
	parentEnvironmentId: null,
	prNumber: null,
	prTitle: "",
	createdAt: "2026-01-01T00:00:00Z",
});

const server = (id: string, status: Server["status"] = "online") =>
	make("Server", {
		id,
		name: id,
		status,
		proxy: { status: "ok" },
		drainState: "none",
		acceptWorkloads: true,
		buildEnabled: false,
		unrestricted: false,
		serviceCount: 0,
		createdAt: "2026-01-01T00:00:00Z",
	});

const web = service({ key: "web", name: "web", port: 80, portHttp: true });
const db = service({ key: "db", name: "db", kind: "database" });
const splitPlan = plan({
	services: [web, db],
	checks: [{ id: "docker", message: "Docker is ready.", status: "ok" }],
});

const result = make("ComposeImportResult", {
	deployments: [],
	groupId: "g1",
	name: "shop",
	rewrites: [],
	services: [
		make("ServiceView", { id: "svc1" }),
		make("ServiceView", { id: "svc2" }),
	],
	warnings: [],
});

const canvasBody = make("Canvas", {
	version: 7,
	layout: {
		nodes: {},
		groups: [
			{
				id: "g1",
				name: "shop",
				serviceIds: ["svc1", "svc2"],
				x: 0,
				y: 0,
				w: 480,
				h: 240,
				collapsed: false,
			},
		],
		viewport: { x: 0, y: 0, zoom: 1 },
	},
});

const problem = (
	status: number,
	code: string,
	detail: string,
	details?: unknown,
) => Response.json({ status, title: "x", detail, code, details }, { status });

interface Options {
	plan?: ComposePlan | ((body: ComposePlanRequest) => Response | ComposePlan);
	servers?: Server[];
	routes?: Parameters<typeof stubApi>[0];
}

/** Shows whether the import dialog is open, as text the test can read. */
function Probe() {
	return (
		<output aria-label="open">{String(useComposeImportState().open)}</output>
	);
}

/** Mounts the host on /p/p1/prod with the API stubbed, and opens the dialog. */
async function open(options: Options = {}) {
	const planned = options.plan ?? splitPlan;
	const api = stubApi({
		"GET /projects": { body: { items: [] } },
		"GET /projects/:projectId/environments": { body: { items: [environment] } },
		"GET /servers": {
			body: make("PageServer", { items: options.servers ?? [server("alpha")] }),
		},
		"GET /git/connections": { body: { items: [] } },
		[PLAN]: async (request) => {
			const body = (await request.json()) as ComposePlanRequest;
			const reply = await (typeof planned === "function"
				? planned(body)
				: planned);
			return reply instanceof Response ? reply : Response.json(reply);
		},
		[CANVAS]: { body: canvasBody },
		...options.routes,
	});
	const landings: Landing[] = [];
	const off = registerLanding((landing) => landings.push(landing));
	landingReleases.push(off);
	const view = await renderWithProviders(
		<>
			<ComposeImportHost />
			<Probe />
		</>,
		{ path: "/p/p1/prod", routePath: "/p/$projectId/$envSlug" },
	);
	await act(async () => composeImportActions.open());
	return { api, landings, off, ...view };
}

const planCalls = (api: ReturnType<typeof stubApi>) =>
	api.calls.filter((c) => c.path.endsWith("/plan"));
const lastPlan = (api: ReturnType<typeof stubApi>) =>
	planCalls(api).at(-1)?.body as ComposePlanRequest;
const importCalls = (api: ReturnType<typeof stubApi>) =>
	api.calls.filter(
		(c) => c.method === "POST" && c.path.endsWith("/compose-imports"),
	);

async function paste(text = COMPOSE) {
	fireEvent.change(
		screen.getByRole("textbox", { name: "docker-compose.yml" }),
		{
			target: { value: text },
		},
	);
}

/**
 * A footer button by its text. A role query walks the whole dialog, and
 * waitFor repeats it on every DOM change: that starves the debounce timer
 * this wizard's plan waits on, so the polled lookup has to be cheap.
 */
const next = (name: string) => {
	const found = [...document.querySelectorAll("button")].find(
		(button) => button.textContent?.trim() === name,
	);
	if (!found) throw new Error(`no button "${name}"`);
	return found;
};

/**
 * The plan is debounced and refetched: wait for the button to come back. The
 * poll throws a plain error, because a failed jest-dom assertion prints the
 * whole DOM on every attempt and starves the timers it is waiting for.
 */
const ready = async (name: string) => {
	await waitFor(
		() => {
			if ((next(name) as HTMLButtonElement).disabled) {
				throw new Error(`"${name}" is still disabled`);
			}
		},
		{ timeout: 8000 },
	);
	expect(next(name)).toBeEnabled();
};

/** Source → Review, once the plan for the pasted text has landed. */
async function toReview(
	user: ReturnType<typeof userEvent.setup>,
	text = COMPOSE,
) {
	await paste(text);
	await user.click(next("Review"));
	await screen.findByRole("checkbox", { name: "Include web" });
	await waitFor(() =>
		expect(lastPlan(arguments_.api).source.compose).toBe(text),
	);
}
// toReview needs the api of the current test; tests set it before calling.
const arguments_ = { api: undefined as unknown as ReturnType<typeof stubApi> };

describe("opening", () => {
	test("renders nothing until the environment is known", async () => {
		stubApi({
			"GET /projects": { body: { items: [] } },
			"GET /projects/:projectId/environments": { body: { items: [] } },
			"GET /servers": { body: make("PageServer", { items: [] }) },
		});
		await renderWithProviders(<ComposeImportHost />, {
			path: "/p/p1/prod",
			routePath: "/p/$projectId/$envSlug",
		});
		await act(async () => composeImportActions.open());
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
	});

	test("is closed until opened, then shows step one", async () => {
		stubApi({
			"GET /projects": { body: { items: [] } },
			"GET /projects/:projectId/environments": {
				body: { items: [environment] },
			},
			"GET /servers": { body: make("PageServer", { items: [] }) },
		});
		await renderWithProviders(<ComposeImportHost />, {
			path: "/p/p1/prod",
			routePath: "/p/$projectId/$envSlug",
		});
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		await act(async () => composeImportActions.open());
		expect(await screen.findByRole("dialog")).toBeInTheDocument();
		expect(screen.getByText("Import Docker Compose")).toBeInTheDocument();
		expect(
			screen.getByText("Paste or upload a compose file, or read it from git."),
		).toBeInTheDocument();
		const steps = screen.getByRole("list", { name: "Steps" });
		expect(within(steps).getByText("Source").closest("li")).toHaveAttribute(
			"aria-current",
			"step",
		);
	});

	test("Review waits for a source; Cancel closes the dialog", async () => {
		const user = userEvent.setup();
		const { api } = await open();
		arguments_.api = api;
		expect(next("Review")).toBeDisabled();
		await paste();
		expect(next("Review")).toBeEnabled();
		await user.click(next("Cancel"));
		expect(screen.getByLabelText("open")).toHaveTextContent("false");
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		// Nothing was planned: the plan only runs from the Review step on.
		expect(planCalls(api)).toEqual([]);
	});

	test("closing and reopening starts from a blank source", async () => {
		const user = userEvent.setup();
		await open();
		await paste();
		await user.click(next("Cancel"));
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		await act(async () => composeImportActions.open());
		expect(
			await screen.findByRole("textbox", { name: "docker-compose.yml" }),
		).toHaveValue("");
	});
});

describe("review", () => {
	test("plans the pasted file for the environment on screen and lists the services", async () => {
		const user = userEvent.setup();
		const { api } = await open();
		arguments_.api = api;
		await paste();
		await user.click(next("Review"));
		expect(
			screen.getByText(
				"Check what each service becomes and change what you need.",
				{
					exact: false,
				},
			),
		).toBeInTheDocument();
		await screen.findByRole("checkbox", { name: "Include web" });
		await waitFor(() => expect(lastPlan(api)?.source.compose).toBe(COMPOSE));
		const call = planCalls(api).at(-1);
		expect(call?.path).toBe("/environments/e1/compose-imports/plan");
		expect(call?.body).toEqual({
			source: { compose: COMPOSE },
			choices: {},
			serverId: "alpha",
		});
		expect(
			screen.getByRole("checkbox", { name: "Include db" }),
		).toBeInTheDocument();
		expect(screen.getByText("2 of 2 services included")).toBeInTheDocument();
		// Review right after pasting: no plan goes out for the source before the paste.
		expect(
			planCalls(api).map((c) => (c.body as ComposePlanRequest).source.compose),
		).toEqual([COMPOSE]);
	});

	test("shows a skeleton while the first plan is on its way", async () => {
		const user = userEvent.setup();
		await open({ plan: () => new Promise<never>(() => {}) as never });
		await paste();
		await user.click(next("Review"));
		expect(
			screen.getByRole("status", { name: "Reading the Compose file" }),
		).toBeInTheDocument();
		expect(next("Continue")).toBeDisabled();
	});

	test("a plan that cannot be read says so and goes back to the source", async () => {
		const user = userEvent.setup();
		await open({
			plan: () => problem(422, "invalid_compose", "services must be a map"),
		});
		await paste("nonsense");
		await user.click(next("Review"));
		expect(
			await screen.findByText("The Compose file could not be read"),
		).toBeInTheDocument();
		expect(
			screen.getByText(/services must be a map Nothing was changed\./),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Edit source" }));
		expect(
			screen.getByRole("textbox", { name: "docker-compose.yml" }),
		).toHaveValue("nonsense");
	});

	test("choices are sent with the next plan request", async () => {
		const user = userEvent.setup();
		const { api } = await open();
		arguments_.api = api;
		await toReview(user);
		await user.click(screen.getByRole("checkbox", { name: "Include db" }));
		expect(screen.getByText("1 of 2 services included")).toBeInTheDocument();
		await waitFor(() =>
			expect(lastPlan(api).choices).toEqual({
				services: { db: { include: false } },
			}),
		);
	});

	test("Back returns to the source with the text kept", async () => {
		const user = userEvent.setup();
		const { api } = await open();
		arguments_.api = api;
		await toReview(user);
		await user.click(next("Back"));
		expect(
			screen.getByRole("textbox", { name: "docker-compose.yml" }),
		).toHaveValue(COMPOSE);
	});

	test("a file the plan asks for is sent with the next request", async () => {
		const user = userEvent.setup();
		const { api } = await open({
			plan: (body) =>
				plan({
					services: [web],
					neededFiles: body.source.files?.["./app.env"]
						? []
						: [
								{
									path: "./app.env",
									purpose: "env_file",
									required: true,
									service: "web",
								},
							],
				}),
		});
		arguments_.api = api;
		await paste();
		await user.click(next("Review"));
		const box = await screen.findByRole("textbox", {
			name: "Contents of ./app.env",
		});
		fireEvent.change(box, { target: { value: "A=1" } });
		await waitFor(() =>
			expect(lastPlan(api).source.files).toEqual({ "./app.env": "A=1" }),
		);
		// Once the file is given the plan stops asking for it, and the box goes.
		await waitFor(() =>
			expect(
				screen.queryByRole("textbox", { name: "Contents of ./app.env" }),
			).not.toBeInTheDocument(),
		);
	});

	test("a failed refresh keeps the last plan on screen", async () => {
		const user = userEvent.setup();
		let failing = false;
		const { api } = await open({
			plan: () => (failing ? problem(500, "boom", "plan broke") : splitPlan),
		});
		arguments_.api = api;
		await toReview(user);
		failing = true;
		await user.click(screen.getByRole("checkbox", { name: "Include db" }));
		expect(
			await screen.findByText("The plan could not be updated"),
		).toBeInTheDocument();
		expect(
			screen.getByRole("checkbox", { name: "Include web" }),
		).toBeInTheDocument();
	});
});

describe("stage", () => {
	async function toStage(
		user: ReturnType<typeof userEvent.setup>,
		options: Options = {},
	) {
		const opened = await open(options);
		arguments_.api = opened.api;
		await toReview(user);
		await user.click(next("Continue"));
		await screen.findByRole("textbox", { name: "Group title" });
		return opened;
	}

	test("offers the server and a group title; the buttons name what is staged", async () => {
		const user = userEvent.setup();
		await toStage(user);
		expect(
			screen.getByText(
				"Choose a server. The services are staged, not deployed.",
			),
		).toBeInTheDocument();
		expect(screen.getByRole("radio", { name: /alpha/ })).toBeChecked();
		expect(next("Stage 2 services")).toBeInTheDocument();
		expect(next("Stage and deploy")).toBeInTheDocument();
		expect(screen.getByText("Docker is ready.")).toBeInTheDocument();
		expect(screen.getByText("web, db")).toBeInTheDocument();
	});

	test("staging posts the reviewed request with an idempotency key, then closes and lands on the canvas", async () => {
		const user = userEvent.setup();
		const keys: Array<string | null> = [];
		const { api, landings } = await toStage(user, {
			routes: {
				[IMPORT]: (request) => {
					keys.push(request.headers.get("Idempotency-Key"));
					return Response.json(result);
				},
			},
		});
		await ready("Stage 2 services");
		fireEvent.change(screen.getByRole("textbox", { name: "Group title" }), {
			target: { value: "Shop" },
		});
		await ready("Stage 2 services");
		await user.click(next("Stage 2 services"));

		expect(
			await screen.findByText(
				"Staged 2 services. Deploy them from the staged bar.",
			),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"shop is on the canvas. Nothing runs until it is deployed.",
			),
		).toBeInTheDocument();
		const call = importCalls(api)[0];
		expect(call?.path).toBe("/environments/e1/compose-imports");
		expect(call?.body).toEqual({
			source: { compose: COMPOSE, name: "Shop" },
			choices: {},
			serverId: "alpha",
		});
		expect(keys).toHaveLength(1);
		expect(keys[0]).toMatch(/^[0-9a-f-]{36}$/);
		expect(screen.getByLabelText("open")).toHaveTextContent("false");
		await waitFor(() => expect(landings).toHaveLength(1));
		expect(landings[0]).toMatchObject({
			groupId: "g1",
			serviceIds: ["svc1", "svc2"],
			document: { version: 7 },
		});
		const canvas = api.calls.find((c) => c.path.endsWith("/canvas"));
		expect(canvas?.path).toBe("/projects/p1/environments/e1/canvas");
	});

	test("Stage and deploy sets deploy and says the services are deploying", async () => {
		const user = userEvent.setup();
		const { api } = await toStage(user, {
			routes: { [IMPORT]: { status: 201, body: result } },
		});
		await ready("Stage and deploy");
		await user.click(next("Stage and deploy"));
		expect(await screen.findByText("Deploying 2 services")).toBeInTheDocument();
		expect(
			screen.getByText("Watch each card for progress."),
		).toBeInTheDocument();
		const body = importCalls(api)[0]?.body as { deploy?: boolean } | undefined;
		expect(body?.deploy).toBe(true);
	});

	test("server notes stay in a toast until dismissed", async () => {
		const user = userEvent.setup();
		await toStage(user, {
			routes: {
				[IMPORT]: {
					status: 201,
					body: make("ComposeImportResult", {
						...result,
						warnings: ["One port was dropped.", "A label was ignored."],
					}),
				},
			},
		});
		await ready("Stage 2 services");
		await user.click(next("Stage 2 services"));
		expect(
			await screen.findByText("Notes about this import"),
		).toBeInTheDocument();
		expect(
			screen.getByText("One port was dropped. A label was ignored."),
		).toBeInTheDocument();
	});

	test("a canvas that cannot be read still imports, with a warning", async () => {
		const user = userEvent.setup();
		const { landings } = await toStage(user, {
			routes: {
				[IMPORT]: { status: 201, body: result },
				[CANVAS]: () => problem(500, "boom", "canvas unavailable"),
			},
		});
		await ready("Stage 2 services");
		await user.click(next("Stage 2 services"));
		expect(
			await screen.findByText("The new group could not be placed"),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				/canvas unavailable The services are on the canvas; arrange them there\./,
			),
		).toBeInTheDocument();
		expect(landings).toEqual([]);
	});

	test("a refused import keeps the dialog and says why; Check again plans again", async () => {
		const user = userEvent.setup();
		const { api } = await toStage(user, {
			routes: {
				[IMPORT]: () => problem(409, "import_conflict", "The file changed."),
			},
		});
		await ready("Stage 2 services");
		await user.click(next("Stage 2 services"));
		expect(
			await screen.findByText("The plan changed while staging"),
		).toBeInTheDocument();
		expect(
			screen.getByText("The file changed. Nothing was staged."),
		).toBeInTheDocument();
		expect(screen.getByLabelText("open")).toHaveTextContent("true");

		const before = planCalls(api).length;
		await user.click(screen.getByRole("button", { name: "Check again" }));
		await waitFor(() => expect(planCalls(api).length).toBeGreaterThan(before));
		await waitFor(() =>
			expect(screen.queryByText("The plan changed while staging")).toBeNull(),
		);
	});

	test("a blocked import lists the blockers the server gave", async () => {
		const user = userEvent.setup();
		await toStage(user, {
			routes: {
				[IMPORT]: () =>
					problem(422, "import_blocked", "blocked", {
						blockers: ["No port.", "Bad image."],
					}),
			},
		});
		await ready("Stage 2 services");
		await user.click(next("Stage 2 services"));
		expect(
			await screen.findByText("Something still blocks the import"),
		).toBeInTheDocument();
		expect(
			screen.getByText("No port. Bad image. Nothing was staged."),
		).toBeInTheDocument();
	});

	test("a retry after a failure reuses the key; a changed request gets a new one", async () => {
		const user = userEvent.setup();
		const keys: Array<string | null> = [];
		let attempt = 0;
		await toStage(user, {
			routes: {
				[IMPORT]: (request) => {
					keys.push(request.headers.get("Idempotency-Key"));
					attempt++;
					return attempt === 1
						? problem(500, "boom", "lost the response")
						: Response.json(result);
				},
			},
		});
		await ready("Stage 2 services");
		await user.click(next("Stage 2 services"));
		await screen.findByText("Could not stage the import");
		expect(
			screen.getByText("lost the response Nothing was staged."),
		).toBeInTheDocument();
		// No replan for a plain failure.
		expect(
			screen.queryByRole("button", { name: "Check again" }),
		).not.toBeInTheDocument();
		await user.click(next("Stage 2 services"));
		await screen.findByText(
			"Staged 2 services. Deploy them from the staged bar.",
		);
		expect(keys).toHaveLength(2);
		expect(keys[1]).toBe(keys[0] as string);
	});

	test("the staging buttons are held while the plan is rechecked after a change", async () => {
		const user = userEvent.setup();
		await toStage(user);
		await ready("Stage 2 services");
		fireEvent.change(screen.getByRole("textbox", { name: "Group title" }), {
			target: { value: "New" },
		});
		expect(next("Stage 2 services")).toBeDisabled();
		expect(
			screen
				.getAllByRole("status")
				.some((s) => s.textContent === "Checking the plan."),
		).toBe(true);
		await ready("Stage 2 services");
	});

	test("a plan that is not installable shows its first blocker and cannot be staged", async () => {
		const user = userEvent.setup();
		await toStage(user, {
			plan: plan({
				services: [web],
				installable: false,
				blockers: ["Image is private."],
			}),
		});
		await waitFor(() =>
			expect(
				screen
					.getAllByRole("status")
					.some((s) => s.textContent === "Image is private."),
			).toBe(true),
		);
		expect(next("Stage 1 service")).toBeDisabled();
		expect(next("Stage and deploy")).toBeDisabled();
	});

	test("without an included service Stage says so", async () => {
		const user = userEvent.setup();
		const { api } = await open({ plan: plan({ services: [web] }) });
		arguments_.api = api;
		await toReview(user);
		await user.click(screen.getByRole("checkbox", { name: "Include web" }));
		await user.click(next("Continue"));
		await screen.findByRole("textbox", { name: "Group title" });
		await waitFor(() =>
			expect(
				screen
					.getAllByRole("status")
					.some((s) => s.textContent === "Include at least one service."),
			).toBe(true),
		);
		expect(screen.getByText("No service is included.")).toBeInTheDocument();
	});

	test("Back from the stage step returns to the review", async () => {
		const user = userEvent.setup();
		await toStage(user);
		await user.click(next("Back"));
		expect(
			screen.getByRole("checkbox", { name: "Include web" }),
		).toBeInTheDocument();
	});
});

describe("servers", () => {
	test("revoked and pending servers are not offered; the first usable one is chosen", async () => {
		const user = userEvent.setup();
		const { api } = await open({
			servers: [
				server("gone", "revoked"),
				server("waiting", "pending"),
				server("live"),
			],
		});
		arguments_.api = api;
		await toReview(user);
		await user.click(next("Continue"));
		await screen.findByRole("radiogroup", { name: "Server" });
		expect(screen.getAllByRole("radio")).toHaveLength(1);
		expect(screen.getByRole("radio", { name: /live/ })).toBeChecked();
		expect(lastPlan(api).serverId).toBe("live");
	});

	test("with no server the plan has no serverId and Stage asks for one", async () => {
		const user = userEvent.setup();
		const { api } = await open({ servers: [] });
		arguments_.api = api;
		await toReview(user);
		await user.click(next("Continue"));
		expect(
			await screen.findByRole("link", { name: "Add a server" }),
		).toBeInTheDocument();
		expect(lastPlan(api)).not.toHaveProperty("serverId");
		expect(
			screen
				.getAllByRole("status")
				.some((s) => s.textContent === "Choose a server."),
		).toBe(true);
		expect(next("Stage 2 services")).toBeDisabled();
	});

	test("choosing another server plans against it", async () => {
		const user = userEvent.setup();
		const { api } = await open({ servers: [server("alpha"), server("beta")] });
		arguments_.api = api;
		await toReview(user);
		await user.click(next("Continue"));
		await user.click(await screen.findByRole("radio", { name: /beta/ }));
		await waitFor(() => expect(lastPlan(api).serverId).toBe("beta"));
	});
});

describe("Run as stack", () => {
	const stack = stackModePlan(
		stackPlan({
			name: "shop-stack",
			components: [
				component({ name: "web", port: 80 }),
				component({ name: "db" }),
			],
			variables: [envRow({ key: "TAG", value: "1" })],
		}),
		{
			name: "shop-stack",
			checks: [{ id: "c", message: "Compose found.", status: "ok" }],
		},
	);

	async function toStackReview(
		user: ReturnType<typeof userEvent.setup>,
		options: Options = {},
	) {
		const opened = await open({ plan: stack, ...options });
		arguments_.api = opened.api;
		await user.click(screen.getByRole("button", { name: "Run as stack" }));
		await paste();
		await user.click(next("Review"));
		await screen.findByRole("region", { name: "Components" });
		return opened;
	}

	test("plans with mode stack and reviews components, not services", async () => {
		const user = userEvent.setup();
		const { api } = await toStackReview(user);
		await waitFor(() => expect(lastPlan(api).mode).toBe("stack"));
		expect(
			screen.getByText("2 components, 0 problems, 1 variable", {
				selector: "p",
			}),
		).toBeInTheDocument();
		expect(
			screen.getByText(/The file stays as it is and runs as one stack\./, {
				exact: false,
			}),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("checkbox", { name: /Include/ }),
		).not.toBeInTheDocument();
	});

	test("the stage step names the stack and stages it as one node", async () => {
		const user = userEvent.setup();
		const { api, landings } = await toStackReview(user, {
			routes: {
				[IMPORT]: {
					status: 201,
					body: make("ComposeImportResult", {
						...result,
						groupId: "",
						name: "shop-stack",
						services: [make("ServiceView", { id: "svc1" })],
					}),
				},
			},
		});
		await user.click(next("Continue"));
		await screen.findByRole("textbox", { name: "Stack name" });
		expect(
			screen.getByText(/The stack is staged as one service, with no group\./, {
				exact: false,
			}),
		).toBeInTheDocument();
		await ready("Stage stack");
		await user.click(next("Stage stack"));
		expect(
			await screen.findByText(
				"Staged the stack shop-stack. Deploy it from the staged bar.",
			),
		).toBeInTheDocument();
		expect(importCalls(api)[0]?.body).toMatchObject({
			mode: "stack",
			serverId: "alpha",
		});
		// A stack is one node: there is no group to bring into view.
		expect(landings).toEqual([]);
		expect(screen.getByLabelText("open")).toHaveTextContent("false");
	});

	test("deploying a stack says so by name", async () => {
		const user = userEvent.setup();
		await toStackReview(user, {
			routes: {
				[IMPORT]: {
					status: 201,
					body: make("ComposeImportResult", {
						...result,
						groupId: "",
						name: "shop-stack",
					}),
				},
			},
		});
		await user.click(next("Continue"));
		await ready("Stage and deploy");
		await user.click(next("Stage and deploy"));
		expect(await screen.findByText("Deploying shop-stack")).toBeInTheDocument();
		expect(
			screen.getByText("Watch its card for progress."),
		).toBeInTheDocument();
	});

	test("switching the mode starts the choices over", async () => {
		const user = userEvent.setup();
		const { api } = await open({ plan: splitPlan });
		arguments_.api = api;
		await toReview(user);
		await user.click(screen.getByRole("checkbox", { name: "Include db" }));
		await waitFor(() =>
			expect(lastPlan(api).choices).toEqual({
				services: { db: { include: false } },
			}),
		);
		await user.click(next("Back"));
		await user.click(screen.getByRole("button", { name: "Run as stack" }));
		await user.click(next("Review"));
		await waitFor(() =>
			expect(lastPlan(api)).toMatchObject({ mode: "stack", choices: {} }),
		);
	});
});
