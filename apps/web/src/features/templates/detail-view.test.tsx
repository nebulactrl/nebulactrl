import { afterEach, describe, expect, test } from "bun:test";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	CatalogTemplate,
	TemplateInput,
	TemplatePlan,
} from "#/api/gen/types.gen";
import type { Server } from "#/api/models";
import { Dialog, DialogContent } from "#/components/ui/dialog";
import {
	type Landing,
	registerLanding,
} from "#/features/canvas/canvas-landing";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { DetailView } from "./detail-view";
import { useTemplatesState } from "./store";
import {
	environment,
	resetTemplatesStore,
	server,
	summary,
	template,
} from "./test-fixtures";

// Literal `${NAME}` placeholder: the escaped `$` keeps it from being a template placeholder.
const ref = (name: string) => `\${${name}}`;

// The landing registry is module state: a canvas left registered would answer
// landOnCanvas in every later file.
const landingReleases: Array<() => void> = [];
afterEach(() => {
	for (const release of landingReleases.splice(0)) release();
	act(() => {
		resetTemplatesStore();
	});
});

const PLAN = "POST /environments/:environmentId/templates/:templateId/plan";
const INSTALL =
	"POST /environments/:environmentId/templates/:templateId/instances";
const CANVAS = "GET /projects/:projectId/environments/:environmentId/canvas";

const passing: TemplatePlan = {
	checks: [{ id: "port", status: "ok", message: "Ports are free" }],
	inputs: [],
	installable: true,
	proxyRestart: false,
	secrets: [],
	services: [],
	template: summary({ id: "umami" }),
	warnings: [],
};

const installed = make("TemplateInstallResult", {
	instance: { id: "i1", groupId: "g1" },
	services: [
		make("ServiceView", { id: "svc1" }),
		make("ServiceView", { id: "svc2" }),
	],
	deployments: [],
	warnings: [],
});

const canvasBody = make("Canvas", {
	version: 5,
	layout: {
		nodes: {},
		groups: [
			{
				id: "g1",
				name: "Umami",
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

const problem = (status: number, detail: string) =>
	Response.json({ status, title: "x", detail, code: "err" }, { status });

function Probe() {
	const { templateId } = useTemplatesState();
	return <output aria-label="step">{templateId ?? "gallery"}</output>;
}

function setup(opts: {
	template?: CatalogTemplate;
	servers?: Server[];
	routes?: Parameters<typeof stubApi>[0];
	production?: boolean;
}) {
	const tpl = opts.template ?? template({ id: "umami", name: "Umami" });
	const api = stubApi({
		"GET /catalog/templates/:templateId": { body: tpl },
		[PLAN]: { body: passing },
		[INSTALL]: { status: 201, body: installed },
		[CANVAS]: { body: canvasBody },
		...opts.routes,
	});
	const landings: Landing[] = [];
	const off = registerLanding((landing) => landings.push(landing));
	landingReleases.push(off);
	const rendered = renderWithProviders(
		<>
			<Dialog open>
				<DialogContent>
					<DetailView
						templateId={tpl.id}
						environment={environment({
							isProduction: opts.production ?? false,
						})}
						projectId="p1"
						servers={opts.servers ?? [server({ id: "s1", name: "alpha" })]}
					/>
				</DialogContent>
			</Dialog>
			<Probe />
		</>,
	);
	return { api, landings, rendered, off };
}

const planCalls = (api: ReturnType<typeof stubApi>) =>
	api.calls.filter((c) => c.path.endsWith("/plan"));
const installCalls = (api: ReturnType<typeof stubApi>) =>
	api.calls.filter((c) => c.path.endsWith("/instances"));

// Role queries are slow on this DOM: poll one captured element instead.
const enabled = (button: HTMLElement) => () => {
	if (button.hasAttribute("disabled")) throw new Error("still disabled");
};
const enabledAdd = async () =>
	enabled(await screen.findByRole("button", { name: "Add to canvas" }));
const enabledDeploy = async () =>
	enabled(await screen.findByRole("button", { name: "Add and deploy" }));
const addButton = () => screen.getByRole("button", { name: "Add to canvas" });
const deployButton = () =>
	screen.getByRole("button", { name: "Add and deploy" });

describe("DetailView: loading the template", () => {
	test("shows a placeholder while the template loads", async () => {
		stubApi({
			"GET /catalog/templates/:templateId": () => new Promise(() => {}),
		});
		await renderWithProviders(
			<Dialog open>
				<DialogContent>
					<DetailView
						templateId="umami"
						environment={environment()}
						projectId="p1"
						servers={[]}
					/>
				</DialogContent>
			</Dialog>,
		);
		expect(screen.getByText("Template")).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Add to canvas" }),
		).not.toBeInTheDocument();
	});

	test("a template that fails to load offers a retry and the way back to the gallery", async () => {
		const user = userEvent.setup();
		let attempts = 0;
		const { rendered } = setup({
			routes: {
				"GET /catalog/templates/:templateId": () => {
					attempts += 1;
					return attempts === 1
						? problem(404, "unknown template")
						: Response.json(template({ id: "umami", name: "Umami" }));
				},
			},
		});
		await rendered;
		expect(
			await screen.findByText("That template did not load"),
		).toBeInTheDocument();
		expect(
			screen.getByText(/unknown template Nothing was changed\./),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: /try again|retry/i }));
		expect(await screen.findByText("Add to canvas")).toBeInTheDocument();
	});

	test("All templates goes back to the gallery from the error state", async () => {
		const user = userEvent.setup();
		const { rendered } = setup({
			routes: {
				"GET /catalog/templates/:templateId": () => problem(500, "boom"),
			},
		});
		await rendered;
		await user.click(
			await screen.findByRole("button", { name: "All templates" }),
		);
		expect(screen.getByLabelText("step")).toHaveTextContent("gallery");
	});
});

describe("DetailView: the template's facts", () => {
	test("shows the header, description, links, services and requirements", async () => {
		const { rendered } = setup({
			template: template({
				id: "umami",
				name: "Umami",
				summary: "Privacy-friendly analytics",
				description: "A longer story.",
				category: "analytics",
				links: {
					website: "https://umami.is",
					docs: "https://umami.is/docs",
					source: "https://github.com/umami",
				},
				requires: { arch: ["amd64", "arm64"], diskGb: 2, memoryMb: 1536 },
				services: [
					{ key: "web", kind: "app", name: "Web", image: "umami:1" },
					{
						key: "db",
						kind: "database",
						name: "DB",
						database: { engine: "postgres", version: "17" },
					},
				],
				notes: ["First login is admin / umami"],
			}),
		});
		await rendered;
		expect(
			await screen.findByRole("heading", { name: "Umami" }),
		).toBeInTheDocument();
		expect(screen.getByText("Privacy-friendly analytics")).toBeInTheDocument();
		expect(screen.getByText("analytics")).toBeInTheDocument();
		expect(screen.getByText("A longer story.")).toBeInTheDocument();
		expect(screen.getByRole("link", { name: /Website/ })).toHaveAttribute(
			"href",
			"https://umami.is",
		);
		expect(screen.getByRole("link", { name: /Docs/ })).toHaveAttribute(
			"href",
			"https://umami.is/docs",
		);
		expect(screen.getByRole("link", { name: /Source/ })).toHaveAttribute(
			"href",
			"https://github.com/umami",
		);
		expect(screen.getByRole("link", { name: /Website/ })).toHaveAttribute(
			"rel",
			"noreferrer noopener",
		);
		expect(screen.getByText("Creates 2 services")).toBeInTheDocument();
		expect(screen.getByText("umami:1")).toBeInTheDocument();
		expect(screen.getByText("PostgreSQL 17")).toBeInTheDocument();
		expect(
			screen.getByText("Needs about 1.5 GB RAM and 2 GB disk · amd64, arm64"),
		).toBeInTheDocument();
		expect(screen.getByText("Good to know")).toBeInTheDocument();
		expect(
			screen.getByText("First login is admin / umami"),
		).toBeInTheDocument();
	});

	test("omits what the template does not have: description, links, notes, disk and arch", async () => {
		const { rendered } = setup({
			template: template({
				id: "umami",
				name: "Umami",
				requires: { arch: null, diskGb: 0, memoryMb: 512 },
				notes: null,
			}),
		});
		await rendered;
		await screen.findByRole("heading", { name: "Umami" });
		expect(screen.getByText("Creates 1 service")).toBeInTheDocument();
		expect(screen.getByText("Needs about 512 MB RAM")).toBeInTheDocument();
		expect(screen.queryByText("Good to know")).not.toBeInTheDocument();
		expect(
			screen.queryByRole("link", { name: /Website|Docs|Source/ }),
		).not.toBeInTheDocument();
	});

	test("the back arrow returns to the gallery", async () => {
		const user = userEvent.setup();
		const { rendered } = setup({});
		await rendered;
		await user.click(
			await screen.findByRole("button", { name: "Back to all templates" }),
		);
		expect(screen.getByLabelText("step")).toHaveTextContent("gallery");
	});

	test("a source-available licence gets a callout with a link to its terms; a permissive one does not", async () => {
		const flagged = setup({
			template: template({
				id: "ghost",
				name: "Ghost",
				upstream: {
					class: "source-available",
					license: "BUSL-1.1",
					licenseUrl: "https://example.com/busl",
				},
			}),
		});
		await flagged.rendered;
		const note = await screen.findByRole("note");
		expect(within(note).getByText("Source-available")).toBeInTheDocument();
		expect(
			within(note).getByText(/not an OSI-approved licence/),
		).toBeInTheDocument();
		expect(
			within(note).getByRole("link", { name: /Read the BUSL-1.1 licence/ }),
		).toHaveAttribute("href", "https://example.com/busl");
	});

	test("a permissive licence raises no callout", async () => {
		const { rendered } = setup({});
		await rendered;
		await screen.findByRole("heading", { name: "Umami" });
		expect(screen.queryByRole("note")).not.toBeInTheDocument();
	});
});

describe("DetailView: the preflight", () => {
	test("runs the checks against the default server and enables both buttons when they pass", async () => {
		const { api, rendered } = setup({});
		await rendered;
		expect(await screen.findByText("Ports are free")).toBeInTheDocument();
		await waitFor(await enabledAdd());
		expect(deployButton()).toBeEnabled();
		expect(planCalls(api)[0]?.body).toEqual({ serverId: "s1", inputs: {} });
		expect(planCalls(api)[0]?.path).toBe(
			"/environments/env1/templates/umami/plan",
		);
		expect(screen.getByText("no failures")).toBeInTheDocument();
	});

	test("both buttons stay disabled until the preflight has answered", async () => {
		const { rendered } = setup({
			routes: { [PLAN]: () => new Promise(() => {}) },
		});
		await rendered;
		await screen.findByRole("heading", { name: "Umami" });
		expect(addButton()).toBeDisabled();
		expect(deployButton()).toBeDisabled();
		expect(screen.getByText("Checking")).toBeInTheDocument();
	});

	test("a failed check disables installing and says to fix it", async () => {
		const { rendered } = setup({
			routes: {
				[PLAN]: {
					body: {
						...passing,
						installable: false,
						checks: [
							{
								id: "mem",
								status: "fail",
								message: "Not enough memory on alpha",
							},
							{ id: "img", status: "warn", message: "The image is large" },
						],
					},
				},
			},
		});
		await rendered;
		expect(
			await screen.findByText("Not enough memory on alpha"),
		).toBeInTheDocument();
		expect(screen.getByText("The image is large")).toBeInTheDocument();
		expect(screen.getByText("1 failed · 1 to review")).toBeInTheDocument();
		await waitFor(() =>
			expect(
				screen.getByText("Fix the failed check to continue."),
			).toBeInTheDocument(),
		);
		expect(addButton()).toBeDisabled();
		expect(deployButton()).toBeDisabled();
	});

	test("a plan the server calls not installable keeps the buttons off even with no failed row", async () => {
		const { rendered } = setup({
			routes: { [PLAN]: { body: { ...passing, installable: false } } },
		});
		await rendered;
		await screen.findByText("Ports are free");
		expect(addButton()).toBeDisabled();
	});

	test("a preflight request that fails is shown as a failed check", async () => {
		const { rendered } = setup({
			routes: { [PLAN]: () => problem(500, "planner offline") },
		});
		await rendered;
		expect(await screen.findByText("planner offline")).toBeInTheDocument();
		expect(screen.getByText("1 failed")).toBeInTheDocument();
		expect(addButton()).toBeDisabled();
	});

	test("lists secrets and the proxy restart as notices of their own, not as check rows", async () => {
		const { rendered } = setup({
			routes: {
				[PLAN]: {
					body: {
						...passing,
						secrets: ["APP_KEY", "DB_PASSWORD"],
						proxyRestart: true,
						checks: [
							{ id: "secrets", status: "ok", message: "secrets row" },
							{ id: "proxy-restart", status: "warn", message: "proxy row" },
							{ id: "license", status: "warn", message: "license row" },
						],
					},
				},
			},
		});
		await rendered;
		expect(
			await screen.findByText(
				/2 secrets will be generated or stored encrypted/,
			),
		).toBeInTheDocument();
		expect(screen.getByText("Opens public ports")).toBeInTheDocument();
		expect(screen.queryByText("secrets row")).not.toBeInTheDocument();
		expect(screen.queryByText("proxy row")).not.toBeInTheDocument();
		expect(screen.queryByText("license row")).not.toBeInTheDocument();
	});
});

describe("DetailView: the form", () => {
	const inputs: TemplateInput[] = [
		{
			key: "DOMAIN",
			label: "Domain",
			default: "",
			required: true,
			secret: false,
		},
		{
			key: "ADMIN_PASSWORD",
			label: "Admin password",
			default: ref("{secret(20)}"),
			required: true,
			secret: true,
		},
	];

	test("a required input without a value holds back the checks and names what is missing", async () => {
		const { api, rendered } = setup({
			template: template({ id: "umami", name: "Umami", inputs }),
		});
		await rendered;
		expect(
			(await screen.findAllByText("Fill in Domain to run the checks.")).length,
		).toBe(2);
		expect(screen.getByText("Configuration")).toBeInTheDocument();
		expect(addButton()).toBeDisabled();
		expect(planCalls(api)).toEqual([]);
	});

	test("typing the missing value runs the preflight with it and unlocks installing", async () => {
		const user = userEvent.setup();
		const { api, rendered } = setup({
			template: template({ id: "umami", name: "Umami", inputs }),
		});
		await rendered;
		await user.click(await screen.findByLabelText(/Domain/));
		await user.paste("stats.example.com");
		await waitFor(await enabledAdd());
		// One preflight, with the typed value: none for the form as it was before.
		expect(planCalls(api).map((c) => c.body)).toEqual([
			{ serverId: "s1", inputs: { DOMAIN: "stats.example.com" } },
		]);
		expect(screen.queryByText(/Fill in/)).not.toBeInTheDocument();
	});

	test("the typed inputs are what install sends", async () => {
		const user = userEvent.setup();
		const { api, rendered } = setup({
			template: template({ id: "umami", name: "Umami", inputs }),
		});
		await rendered;
		await user.click(await screen.findByLabelText(/Domain/));
		await user.paste("a.com");
		await waitFor(await enabledAdd());
		await user.click(addButton());
		await waitFor(() => expect(installCalls(api)).toHaveLength(1));
		expect(installCalls(api)[0]?.body).toEqual({
			serverId: "s1",
			inputs: { DOMAIN: "a.com" },
		});
	});
});

describe("DetailView: the server", () => {
	test("with no server enrolled it says so, links to adding one and refuses to install", async () => {
		const { rendered } = setup({ servers: [] });
		await rendered;
		expect(
			await screen.findByText(/No server is enrolled yet\./),
		).toBeInTheDocument();
		expect(screen.getByRole("link", { name: "Add a server" })).toHaveAttribute(
			"href",
			"/servers",
		);
		await waitFor(() =>
			expect(
				screen.getByText("No server can take this stack yet."),
			).toBeInTheDocument(),
		);
		expect(addButton()).toBeDisabled();
	});

	test("revoked and pending servers are not offered", async () => {
		const { rendered } = setup({
			servers: [
				server({ id: "r", name: "revoked-box", status: "revoked" }),
				server({ id: "p", name: "pending-box", status: "pending" }),
				server({ id: "ok", name: "good-box" }),
			],
		});
		await rendered;
		const group = await screen.findByRole("radiogroup", { name: "Server" });
		expect(within(group).getAllByRole("radio")).toHaveLength(1);
		expect(
			within(group).getByRole("radio", { name: /good-box/ }),
		).toBeChecked();
	});

	test("a draining server is shown but cannot be chosen, so the default is another one", async () => {
		const { api, rendered } = setup({
			servers: [
				server({ id: "d", name: "draining-box", drainState: "draining" }),
				server({ id: "ok", name: "good-box" }),
			],
		});
		await rendered;
		const group = await screen.findByRole("radiogroup", { name: "Server" });
		expect(
			within(group).getByRole("radio", { name: /draining-box/ }),
		).toBeDisabled();
		await waitFor(() =>
			expect(planCalls(api)[0]?.body).toEqual({ serverId: "ok", inputs: {} }),
		);
	});

	test("picking another server re-runs the preflight there and installs there", async () => {
		const user = userEvent.setup();
		const { api, rendered } = setup({
			servers: [
				server({ id: "a", name: "alpha" }),
				server({ id: "b", name: "beta" }),
			],
		});
		await rendered;
		await user.click(await screen.findByRole("radio", { name: /beta/ }));
		await waitFor(() =>
			expect(planCalls(api).at(-1)?.body).toEqual({
				serverId: "b",
				inputs: {},
			}),
		);
		await waitFor(await enabledAdd());
		await user.click(addButton());
		await waitFor(() => expect(installCalls(api)).toHaveLength(1));
		expect(installCalls(api)[0]?.body).toEqual(
			expect.objectContaining({ serverId: "b" }),
		);
	});
});

describe("DetailView: templates that need an unrestricted server", () => {
	const gpu = template({
		id: "ollama",
		name: "Ollama",
		requiresUnrestricted: true,
	});

	test("restricted servers are disabled with the reason and the default is the unrestricted one", async () => {
		const { api, rendered } = setup({
			template: gpu,
			servers: [
				server({ id: "r", name: "restricted-box" }),
				server({ id: "u", name: "open-box", unrestricted: true }),
			],
		});
		await rendered;
		expect(
			await screen.findByText("Needs an unrestricted server"),
		).toBeInTheDocument();
		const restricted = screen.getByRole("radio", { name: /restricted-box/ });
		expect(restricted).toBeDisabled();
		expect(restricted).toHaveTextContent(
			"Restricted: this template needs an unrestricted server",
		);
		expect(screen.getByRole("radio", { name: /open-box/ })).toBeChecked();
		expect(
			screen.getByText(
				/Restricted servers are disabled: this template needs host access\./,
			),
		).toBeInTheDocument();
		await waitFor(() =>
			expect(planCalls(api)[0]?.body).toEqual({ serverId: "u", inputs: {} }),
		);
	});

	test("when no server is unrestricted it explains who can fix that and refuses to install", async () => {
		const { rendered } = setup({
			template: gpu,
			servers: [server({ id: "r", name: "restricted-box" })],
		});
		await rendered;
		const notice = await screen.findByRole("note");
		expect(
			within(notice).getByText("No unrestricted server"),
		).toBeInTheDocument();
		expect(
			within(notice).getByText(/An instance admin can turn/),
		).toBeInTheDocument();
		await waitFor(() =>
			expect(
				screen.getByText(
					"No unrestricted server: an instance admin turns it on in the server settings.",
				),
			).toBeInTheDocument(),
		);
		expect(addButton()).toBeDisabled();
		expect(deployButton()).toBeDisabled();
		expect(
			screen.queryByText(/Restricted servers are disabled/),
		).not.toBeInTheDocument();
	});
});

describe("DetailView: installing", () => {
	test("Add to canvas installs staged, confirms, and hands the saved layout to the canvas", async () => {
		const user = userEvent.setup();
		const { api, landings, rendered, off } = setup({});
		try {
			await rendered;
			await waitFor(await enabledAdd());
			await user.click(addButton());
			expect(
				await screen.findByText("2 services added, staged"),
			).toBeInTheDocument();
			expect(
				screen.getByText(
					"Umami is on the canvas. Nothing runs until it is deployed.",
				),
			).toBeInTheDocument();
			const call = installCalls(api)[0];
			expect(call?.method).toBe("POST");
			expect(call?.path).toBe("/environments/env1/templates/umami/instances");
			expect(call?.body).toEqual({ serverId: "s1", inputs: {} });
			await waitFor(() => expect(landings).toHaveLength(1));
			expect(landings[0]?.groupId).toBe("g1");
			expect(landings[0]?.serviceIds).toEqual(["svc1", "svc2"]);
			expect(landings[0]?.document.version).toBe(5);
			expect(
				api.calls.some(
					(c) => c.path === "/projects/p1/environments/env1/canvas",
				),
			).toBe(true);
		} finally {
			off();
		}
	});

	test("Add and deploy asks the server to deploy and says it is deploying", async () => {
		const user = userEvent.setup();
		const { api, rendered, off } = setup({});
		try {
			await rendered;
			await waitFor(await enabledDeploy());
			await user.click(deployButton());
			expect(
				await screen.findByText("Deploying 2 services"),
			).toBeInTheDocument();
			expect(
				screen.getByText("Watch each card for progress."),
			).toBeInTheDocument();
			expect(installCalls(api)[0]?.body).toEqual({
				serverId: "s1",
				inputs: {},
				deploy: true,
			});
		} finally {
			off();
		}
	});

	test("server warnings replace the default toast text", async () => {
		const user = userEvent.setup();
		const { rendered, off } = setup({
			routes: {
				[INSTALL]: {
					status: 201,
					body: { ...installed, warnings: ["Port 80 is shared", "Slow disk"] },
				},
			},
		});
		try {
			await rendered;
			await waitFor(await enabledAdd());
			await user.click(addButton());
			expect(
				await screen.findByText("Port 80 is shared Slow disk"),
			).toBeInTheDocument();
		} finally {
			off();
		}
	});

	test("a canvas that cannot be reloaded still reports the install and tells the user to arrange the services", async () => {
		const user = userEvent.setup();
		const { rendered, off } = setup({
			routes: { [CANVAS]: () => problem(500, "canvas unavailable") },
		});
		try {
			await rendered;
			await waitFor(await enabledAdd());
			await user.click(addButton());
			expect(
				await screen.findByText("The new group could not be placed"),
			).toBeInTheDocument();
			expect(
				screen.getByText(
					/canvas unavailable The services are on the canvas; arrange them there\./,
				),
			).toBeInTheDocument();
			expect(screen.getByText("2 services added, staged")).toBeInTheDocument();
		} finally {
			off();
		}
	});

	test("a changed plan (409) says nothing was added and Check again re-runs the preflight", async () => {
		const user = userEvent.setup();
		const { api, rendered } = setup({
			routes: { [INSTALL]: () => problem(409, "the domain was taken") },
		});
		await rendered;
		await waitFor(await enabledAdd());
		await user.click(addButton());
		expect(
			await screen.findByText("The plan changed while installing"),
		).toBeInTheDocument();
		expect(
			screen.getByText("the domain was taken Nothing was added."),
		).toBeInTheDocument();
		const before = planCalls(api).length;
		await user.click(screen.getByRole("button", { name: "Check again" }));
		await waitFor(() => expect(planCalls(api).length).toBeGreaterThan(before));
		await waitFor(() =>
			expect(
				screen.queryByText("The plan changed while installing"),
			).not.toBeInTheDocument(),
		);
	});

	test("a forbidden install names the permission problem and offers no re-check", async () => {
		const user = userEvent.setup();
		const { rendered } = setup({
			routes: { [INSTALL]: () => problem(403, "viewers cannot install") },
		});
		await rendered;
		await waitFor(await enabledAdd());
		await user.click(addButton());
		expect(
			await screen.findByText("You are not allowed to install this"),
		).toBeInTheDocument();
		expect(screen.getByText("viewers cannot install")).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Check again" }),
		).not.toBeInTheDocument();
	});

	test("an unexpected failure names the template", async () => {
		const user = userEvent.setup();
		const { rendered } = setup({
			routes: { [INSTALL]: () => problem(500, "disk full") },
		});
		await rendered;
		await waitFor(await enabledAdd());
		await user.click(addButton());
		expect(await screen.findByText("Could not add Umami")).toBeInTheDocument();
		expect(screen.getByText("disk full")).toBeInTheDocument();
	});

	test("retrying the same install replays its idempotency key; a changed server is a new request", async () => {
		const user = userEvent.setup();
		const keys: (string | null)[] = [];
		const { rendered } = setup({
			servers: [
				server({ id: "a", name: "alpha" }),
				server({ id: "b", name: "beta" }),
			],
			routes: {
				[INSTALL]: (request) => {
					keys.push(request.headers.get("Idempotency-Key"));
					return problem(500, "flaky");
				},
			},
		});
		await rendered;
		await waitFor(await enabledAdd());
		await user.click(addButton());
		await screen.findByText("Could not add Umami");
		await user.click(addButton());
		await waitFor(() => expect(keys).toHaveLength(2));
		expect(keys[0]).toBeTruthy();
		expect(keys[1]).toBe(keys[0]);
		await user.click(screen.getByRole("radio", { name: /beta/ }));
		await waitFor(await enabledAdd());
		await user.click(addButton());
		await waitFor(() => expect(keys).toHaveLength(3));
		expect(keys[2]).not.toBe(keys[0]);
	});

	test("the toast action stages a deploy of the new stack's services outside production", async () => {
		const user = userEvent.setup();
		const { api, rendered, off } = setup({
			routes: {
				"POST /environments/:environmentId/deploy-staged": {
					status: 202,
					body: make("StagedResult", {
						items: [make("DeploymentView", { id: "d1" })],
					}),
				},
			},
		});
		try {
			await rendered;
			await waitFor(await enabledAdd());
			await user.click(addButton());
			await user.click(
				await screen.findByRole("button", { name: "Deploy stack" }),
			);
			await waitFor(() =>
				expect(
					api.calls.find((c) => c.path === "/environments/env1/deploy-staged")
						?.body,
				).toEqual({
					serviceIds: ["svc1", "svc2"],
				}),
			);
		} finally {
			off();
		}
	});
});
