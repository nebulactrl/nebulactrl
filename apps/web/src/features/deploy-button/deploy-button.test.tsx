import { describe, expect, test } from "bun:test";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { FieldChange, ServiceView } from "#/api/gen/types.gen";
import { useCommandRegistry } from "#/components/shell/palette-registry";
import { type ApiRoutes, stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { DeployButton } from "./deploy-button";
import { openStagedReview } from "./open-review";
import { serviceView } from "./test-fixtures";

const PROJECT = make("Project", {
	id: "proj-1",
	name: "Shop",
	slug: "shop",
	description: "",
	system: false,
	registryId: "",
	buildLocation: "inherit",
	buildServerId: "",
	createdAt: "2026-01-01T00:00:00Z",
	updatedAt: "2026-01-01T00:00:00Z",
});

function environment(isProduction: boolean) {
	return make("Environment", {
		id: "env-1",
		name: isProduction ? "Production" : "Staging",
		slug: isProduction ? "production" : "staging",
		projectId: "proj-1",
		isProduction,
		kind: "standard",
		parentEnvironmentId: null,
		prNumber: null,
		prTitle: "",
		createdAt: "2026-01-01T00:00:00Z",
	});
}

interface Backend {
	services?: ServiceView[];
	changes?: Record<string, FieldChange[]>;
	production?: boolean;
	extra?: ApiRoutes;
}

const deployment = (serviceId: string) =>
	make("DeploymentView", {
		id: `dep-${serviceId}`,
		serviceId,
		status: "queued",
		trigger: "manual",
		strategy: "recreate",
		statusMessage: "",
		createdAt: "2026-01-01T00:00:00Z",
	});

function routes({
	services = [],
	changes = {},
	production = false,
	extra = {},
}: Backend = {}) {
	return {
		"GET /projects": { body: { items: [PROJECT] } },
		"GET /projects/:id/environments": {
			body: { items: [environment(production)] },
		},
		"GET /environments/:id/services": () => Response.json({ items: services }),
		"GET /services/:id/pending-changes": (request: Request) => {
			const id = new URL(request.url).pathname.split("/")[4] ?? "";
			return Response.json({ items: changes[id] ?? [] });
		},
		"GET /services/:id/config-source": {
			body: { managed: false, fields: null, pending: null },
		},
		...extra,
	} satisfies ApiRoutes;
}

const PORT_CHANGE: FieldChange = {
	field: "config.port",
	from: "80",
	to: "8080",
};
const TOKEN_CHANGE: FieldChange = {
	field: "variables.TOKEN",
	from: "",
	to: "abc",
};
const FIRST: FieldChange = { field: "deployment", from: "", to: "" };

function Registry() {
	const commands = useCommandRegistry().filter((c) => c.id === "deploy:staged");
	return (
		<div>
			{commands.map((command) => (
				<button
					key={command.id}
					type="button"
					data-disabled={command.disabled || undefined}
					data-hint={command.hint}
					onClick={command.run}
				>
					palette:{command.label}
				</button>
			))}
		</div>
	);
}

async function renderButton(production = false, path?: string) {
	const env = production ? "production" : "staging";
	await renderWithProviders(
		<>
			<DeployButton />
			<Registry />
		</>,
		{ path: path ?? `/p/proj-1/${env}`, routePath: "/p/$projectId/$envSlug" },
	);
	return userEvent.setup();
}

const stagedApi = (extra: ApiRoutes = {}, production = false) =>
	stubApi(
		routes({
			production,
			services: [
				serviceView({
					id: "svc-api",
					name: "api",
					staged: 2,
					pendingChangeCounts: { variables: 1, settings: 1 },
				} as never),
				serviceView({ id: "svc-idle", name: "idle" }),
			],
			changes: { "svc-api": [PORT_CHANGE, TOKEN_CHANGE] },
			extra,
		}),
	);

describe("DeployButton outside an environment", () => {
	test("renders nothing when the page is not inside an environment", async () => {
		stubApi(routes());
		await renderWithProviders(<DeployButton />, { path: "/servers" });
		expect(screen.queryByRole("group")).toBeNull();
		expect(screen.queryByRole("button", { name: /Deploy/ })).toBeNull();
	});

	test("renders nothing for an environment the project does not have", async () => {
		const api = stubApi(routes());
		await renderButton(false, "/p/proj-1/nope");
		await waitFor(() =>
			expect(
				api.calls.some((c) => c.path === "/projects/proj-1/environments"),
			).toBe(true),
		);
		expect(screen.queryByRole("group", { name: "Deploy" })).toBeNull();
	});
});

describe("DeployButton outside production", () => {
	test("with nothing staged the key is inert and says why", async () => {
		stubApi(
			routes({ services: [serviceView({ id: "svc-idle", name: "idle" })] }),
		);
		const user = await renderButton();
		const key = await screen.findByRole("button", {
			name: /Deploy\. 0 staged changes/,
		});
		expect(key).toHaveAttribute("aria-disabled", "true");
		expect(key).toHaveAttribute("title", "Nothing staged to deploy");
		await user.click(key);
		expect(screen.queryByRole("dialog")).toBeNull();
	});

	test("shows the staged count and opens the review with the change lines", async () => {
		stagedApi();
		const user = await renderButton();
		const key = await screen.findByRole("button", {
			name: /Deploy\. 2 staged changes\. Opens review\./,
		});
		await user.click(key);
		const sheet = await screen.findByRole("dialog");
		expect(within(sheet).getByText("Review changes")).toBeInTheDocument();
		const section = await within(sheet).findByRole("region", { name: "api" });
		expect(await within(section).findByText("config.port")).toBeInTheDocument();
		expect(within(section).getByText("80 → 8080")).toBeInTheDocument();
		expect(within(section).getByText("variables.TOKEN")).toBeInTheDocument();
	});

	test("Deploy in the sheet applies the staged changes with the message and closes", async () => {
		const api = stagedApi({
			"POST /environments/:id/deploy-staged": {
				status: 202,
				body: make("StagedResult", {
					items: [deployment("svc-api")],
					order: ["svc-api"],
				}),
			},
		});
		const user = await renderButton();
		await user.click(
			await screen.findByRole("button", { name: /Opens review/ }),
		);
		const sheet = await screen.findByRole("dialog");
		await within(sheet).findByRole("region", { name: "api" });
		await user.type(
			within(sheet).getByLabelText(/Deploy message/),
			"  Bump port  ",
		);
		await user.click(within(sheet).getByRole("button", { name: "Deploy · 2" }));
		expect(await screen.findByText("Deploying 1 service")).toBeInTheDocument();
		const post = api.calls.filter((c) => c.method === "POST");
		expect(post).toHaveLength(1);
		expect(post[0]).toMatchObject({
			path: "/environments/env-1/deploy-staged",
			body: { message: "Bump port" },
		});
		expect(post[0]?.body).not.toHaveProperty("serviceIds");
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});

	test("a refused deploy toasts the reason and keeps the sheet open", async () => {
		stagedApi({
			"POST /environments/:id/deploy-staged": problem(409, {
				title: "Conflict",
				detail: "A deployment is already running.",
			}),
		});
		const user = await renderButton();
		await user.click(
			await screen.findByRole("button", { name: /Opens review/ }),
		);
		const sheet = await screen.findByRole("dialog");
		await within(sheet).findByRole("region", { name: "api" });
		await user.click(within(sheet).getByRole("button", { name: "Deploy · 2" }));
		expect(
			await screen.findByText("Could not deploy the changes"),
		).toBeInTheDocument();
		expect(
			screen.getByText("A deployment is already running."),
		).toBeInTheDocument();
		expect(screen.getByRole("dialog")).toBeInTheDocument();
		expect(
			within(screen.getByRole("dialog")).getByRole("button", {
				name: "Deploy · 2",
			}),
		).toBeEnabled();
	});

	test("discarding one line reverts that field and refreshes", async () => {
		const changes: Record<string, FieldChange[]> = {
			"svc-api": [PORT_CHANGE, TOKEN_CHANGE],
		};
		const api = stubApi(
			routes({
				services: [serviceView({ id: "svc-api", name: "api", staged: 2 })],
				changes,
				extra: {
					"DELETE /services/:id/pending-changes/:field": () => {
						changes["svc-api"] = [TOKEN_CHANGE];
						return new Response(null, { status: 204 });
					},
				},
			}),
		);
		const user = await renderButton();
		await user.click(
			await screen.findByRole("button", { name: /Opens review/ }),
		);
		const sheet = await screen.findByRole("dialog");
		await user.click(
			await within(sheet).findByRole("button", {
				name: "Discard change to config.port on api",
			}),
		);
		await waitFor(() =>
			expect(within(sheet).queryByText("config.port")).toBeNull(),
		);
		expect(api.calls.find((c) => c.method === "DELETE")?.path).toBe(
			"/services/svc-api/pending-changes/config.port",
		);
		expect(within(sheet).getByText("variables.TOKEN")).toBeInTheDocument();
	});

	test("a failed discard toasts the reason", async () => {
		stubApi(
			routes({
				services: [serviceView({ id: "svc-api", name: "api", staged: 2 })],
				changes: { "svc-api": [PORT_CHANGE, TOKEN_CHANGE] },
				extra: {
					"DELETE /services/:id/pending-changes/:field": problem(500, {
						title: "Internal",
						detail: "Could not revert it.",
					}),
				},
			}),
		);
		const user = await renderButton();
		await user.click(
			await screen.findByRole("button", { name: /Opens review/ }),
		);
		await user.click(
			await screen.findByRole("button", {
				name: "Discard change to config.port on api",
			}),
		);
		expect(
			await screen.findByText("Could not discard that change"),
		).toBeInTheDocument();
		expect(screen.getByText("Could not revert it.")).toBeInTheDocument();
	});
});

describe("DeployButton menu", () => {
	test("Review changes opens the sheet; it is disabled with nothing staged", async () => {
		stubApi(
			routes({ services: [serviceView({ id: "svc-idle", name: "idle" })] }),
		);
		const user = await renderButton();
		await user.click(
			await screen.findByRole("button", { name: "Deploy options" }),
		);
		const review = await screen.findByRole("menuitem", {
			name: "Review changes",
		});
		expect(review).toHaveAttribute("aria-disabled", "true");
		expect(
			screen.getByRole("menuitem", { name: "Discard all" }),
		).toHaveAttribute("aria-disabled", "true");
		expect(screen.getByText("Staging")).toBeInTheDocument();
	});

	test("Review changes from the menu opens the review", async () => {
		stagedApi();
		const user = await renderButton();
		await screen.findByRole("button", { name: /Deploy\. 2 staged/ });
		await user.click(screen.getByRole("button", { name: "Deploy options" }));
		await user.click(
			await screen.findByRole("menuitem", { name: "Review changes" }),
		);
		expect(await screen.findByRole("dialog")).toHaveTextContent(
			"Review changes",
		);
	});

	test("Redeploy current starts a deployment of each deployed service that is not staged", async () => {
		const api = stagedApi({
			"POST /services/:id/deployments": (request) =>
				Response.json(
					deployment(new URL(request.url).pathname.split("/")[2] ?? ""),
					{ status: 202 },
				),
		});
		const user = await renderButton();
		await screen.findByRole("button", { name: /Deploy\. 2 staged/ });
		await user.click(screen.getByRole("button", { name: "Deploy options" }));
		await user.click(
			await screen.findByRole("menuitem", { name: "Redeploy current" }),
		);
		expect(
			await screen.findByText("Redeploying 1 service"),
		).toBeInTheDocument();
		// A staged service ships its changes through Deploy, not through a redeploy.
		expect(
			api.calls.filter((c) => c.method === "POST").map((c) => c.path),
		).toEqual(["/services/svc-idle/deployments"]);
	});

	test("Redeploy current with failures names them", async () => {
		stagedApi({
			"POST /services/:id/deployments": problem(500, {
				title: "Internal",
				detail: "No server.",
			}),
		});
		const user = await renderButton();
		await screen.findByRole("button", { name: /Deploy\. 2 staged/ });
		await user.click(screen.getByRole("button", { name: "Deploy options" }));
		await user.click(
			await screen.findByRole("menuitem", { name: "Redeploy current" }),
		);
		expect(
			await screen.findByText("Redeploying 0, 1 failed"),
		).toBeInTheDocument();
		expect(screen.getByText("idle: No server.")).toBeInTheDocument();
	});

	test("Redeploy current with nothing deployed says so", async () => {
		stubApi(
			routes({
				services: [
					serviceView({ id: "svc-new", name: "new", currentDeploymentId: "" }),
				],
			}),
		);
		const user = await renderButton();
		await screen.findByRole("button", { name: /Deploy\. 0 staged/ });
		await user.click(screen.getByRole("button", { name: "Deploy options" }));
		await user.click(
			await screen.findByRole("menuitem", { name: "Redeploy current" }),
		);
		expect(await screen.findByText("Nothing to redeploy")).toBeInTheDocument();
	});

	test("Discard all confirms, then reverts every field but a first deployment", async () => {
		const api = stubApi(
			routes({
				services: [
					serviceView({ id: "svc-api", name: "api", staged: 2 }),
					serviceView({ id: "svc-new", name: "new", staged: 1 }),
				],
				changes: { "svc-api": [PORT_CHANGE, TOKEN_CHANGE], "svc-new": [FIRST] },
				extra: {
					"DELETE /services/:id/pending-changes/:field": {
						body: make("ServiceView"),
					},
				},
			}),
		);
		const user = await renderButton();
		await screen.findByRole("button", { name: /Deploy\. 3 staged/ });
		await user.click(screen.getByRole("button", { name: "Deploy options" }));
		await user.click(
			await screen.findByRole("menuitem", { name: "Discard all" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Discard 3 staged changes?"),
		).toBeInTheDocument();
		expect(api.calls.filter((c) => c.method === "DELETE")).toEqual([]);
		await user.click(
			within(dialog).getByRole("button", { name: "Discard all" }),
		);
		expect(await screen.findByText("Changes discarded")).toBeInTheDocument();
		expect(
			api.calls.filter((c) => c.method === "DELETE").map((c) => c.path),
		).toEqual([
			"/services/svc-api/pending-changes/config.port",
			"/services/svc-api/pending-changes/variables.TOKEN",
		]);
	});

	test("cancelling the discard confirmation changes nothing", async () => {
		const api = stagedApi();
		const user = await renderButton();
		await screen.findByRole("button", { name: /Deploy\. 2 staged/ });
		await user.click(screen.getByRole("button", { name: "Deploy options" }));
		await user.click(
			await screen.findByRole("menuitem", { name: "Discard all" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(api.calls.filter((c) => c.method === "DELETE")).toEqual([]);
	});

	test("a single staged change is discarded in the singular", async () => {
		stubApi(
			routes({
				services: [serviceView({ id: "svc-api", name: "api", staged: 1 })],
				changes: { "svc-api": [PORT_CHANGE] },
			}),
		);
		const user = await renderButton();
		await screen.findByRole("button", {
			name: /Deploy\. 1 staged change\. Opens review/,
		});
		await user.click(screen.getByRole("button", { name: "Deploy options" }));
		await user.click(
			await screen.findByRole("menuitem", { name: "Discard all" }),
		);
		expect(
			await screen.findByText("Discard 1 staged change?"),
		).toBeInTheDocument();
	});

	test("Rename project opens the rename dialog with the current name", async () => {
		stagedApi();
		const user = await renderButton();
		await screen.findByRole("button", { name: /Deploy\. 2 staged/ });
		// The project list answers after the environment; the group appears with it.
		await user.click(screen.getByRole("button", { name: "Deploy options" }));
		await user.click(
			await screen.findByRole("menuitem", { name: /Rename project/ }),
		);
		const dialog = await screen.findByRole("dialog", {
			name: "Rename project",
		});
		expect(within(dialog).getByLabelText("Name")).toHaveValue("Shop");
	});
});

describe("DeployButton opened from elsewhere", () => {
	test("openStagedReview scoped to a staged service shows only that service", async () => {
		stagedApi();
		await renderButton();
		await screen.findByRole("button", { name: /Deploy\. 2 staged/ });
		act(() => openStagedReview({ serviceId: "svc-api" }));
		const sheet = await screen.findByRole("dialog");
		expect(within(sheet).getByText("Deploy api")).toBeInTheDocument();
		expect(
			await within(sheet).findByRole("region", { name: "api" }),
		).toBeInTheDocument();
	});

	test("a scoped idle service redeploys its current release", async () => {
		const api = stagedApi({
			"POST /services/:id/deployments": {
				status: 202,
				body: deployment("svc-idle"),
			},
		});
		const user = await renderButton();
		await screen.findByRole("button", { name: /Deploy\. 2 staged/ });
		act(() => openStagedReview({ serviceId: "svc-idle" }));
		const sheet = await screen.findByRole("dialog");
		await user.type(within(sheet).getByLabelText(/Deploy message/), "again");
		await user.click(within(sheet).getByRole("button", { name: "Redeploy" }));
		expect(await screen.findByText("Redeploying")).toBeInTheDocument();
		const post = api.calls.find((c) => c.method === "POST");
		expect(post).toMatchObject({
			path: "/services/svc-idle/deployments",
			body: { message: "again" },
		});
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});

	test("a refused redeploy toasts the reason and keeps the sheet", async () => {
		stagedApi({
			"POST /services/:id/deployments": problem(500, {
				title: "Internal",
				detail: "Server offline.",
			}),
		});
		const user = await renderButton();
		await screen.findByRole("button", { name: /Deploy\. 2 staged/ });
		act(() => openStagedReview({ serviceId: "svc-idle" }));
		const sheet = await screen.findByRole("dialog");
		await user.click(within(sheet).getByRole("button", { name: "Redeploy" }));
		expect(await screen.findByText("Could not redeploy")).toBeInTheDocument();
		expect(screen.getByText("Server offline.")).toBeInTheDocument();
		expect(screen.getByRole("dialog")).toBeInTheDocument();
	});

	test("the palette command opens the review, or says nothing is staged", async () => {
		stagedApi();
		const user = await renderButton();
		const command = await screen.findByRole("button", {
			name: "palette:Deploy staged changes",
		});
		await waitFor(() =>
			expect(command).toHaveAttribute("data-hint", "2 changes · review first"),
		);
		await user.click(command);
		expect(await screen.findByRole("dialog")).toHaveTextContent(
			"Review changes",
		);
	});

	test("with nothing staged the palette command is disabled and explains itself when run", async () => {
		stubApi(
			routes({ services: [serviceView({ id: "svc-idle", name: "idle" })] }),
		);
		const user = await renderButton();
		const command = await screen.findByRole("button", {
			name: "palette:Deploy staged changes",
		});
		await waitFor(() =>
			expect(command).toHaveAttribute("data-hint", "nothing staged"),
		);
		expect(command).toHaveAttribute("data-disabled", "true");
		await user.click(command);
		expect(await screen.findByText("Nothing staged")).toBeInTheDocument();
		expect(
			screen.getByText(
				"Changes you make to a service wait here until deployed.",
			),
		).toBeInTheDocument();
		expect(screen.queryByRole("dialog")).toBeNull();
	});
});

describe("DeployButton in production", () => {
	test("shows the guarded key, which deploys only after the cover is lifted", async () => {
		const api = stagedApi(
			{
				"POST /environments/:id/deploy-staged": {
					status: 202,
					body: make("StagedResult", {
						items: [deployment("svc-api")],
						order: [],
					}),
				},
			},
			true,
		);
		const user = await renderButton(true);
		const cover = await screen.findByRole("button", {
			name: "Lift production guard",
		});
		await waitFor(() =>
			expect(
				screen.getByRole("button", { name: "Deploy · 2", hidden: true }),
			).toBeEnabled(),
		);
		const key = screen.getByRole("button", {
			name: "Deploy · 2",
			hidden: true,
		});
		fireClick(key);
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);
		await user.click(cover);
		await user.click(key);
		expect(await screen.findByText("Deploying 1 service")).toBeInTheDocument();
		expect(
			api.calls.filter((c) => c.method === "POST").map((c) => c.path),
		).toEqual(["/environments/env-1/deploy-staged"]);
	});

	test("with nothing staged the guard cannot be lifted", async () => {
		stubApi(
			routes({
				production: true,
				services: [serviceView({ id: "svc-idle", name: "idle" })],
			}),
		);
		const user = await renderButton(true);
		const cover = await screen.findByRole("button", {
			name: "Lift production guard",
		});
		await waitFor(() =>
			expect(cover).toHaveAttribute("title", "Nothing staged to deploy"),
		);
		await user.click(cover);
		expect(cover).toHaveAttribute("aria-expanded", "false");
	});

	test("Redeploy current asks first in production", async () => {
		const api = stagedApi(
			{
				"POST /services/:id/deployments": {
					status: 202,
					body: deployment("svc-idle"),
				},
			},
			true,
		);
		const user = await renderButton(true);
		await screen.findByRole("button", { name: "Lift production guard" });
		await user.click(screen.getByRole("button", { name: "Deploy options" }));
		await user.click(
			await screen.findByRole("menuitem", { name: "Redeploy current" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(
			within(dialog).getByText("Redeploy current releases in Production?"),
		).toBeInTheDocument();
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);

		await user.click(screen.getByRole("button", { name: "Deploy options" }));
		await user.click(
			await screen.findByRole("menuitem", { name: "Redeploy current" }),
		);
		await user.click(
			within(await screen.findByRole("alertdialog")).getByRole("button", {
				name: "Redeploy",
			}),
		);
		expect(
			await screen.findByText("Redeploying 1 service"),
		).toBeInTheDocument();
		expect(
			api.calls.filter((c) => c.method === "POST").map((c) => c.path),
		).toEqual(["/services/svc-idle/deployments"]);
	});
});

describe("DeployButton on a narrow bar", () => {
	test("production falls back to the split button, whose guard waits in the review", async () => {
		stagedApi({}, true);
		const rect = HTMLElement.prototype.getBoundingClientRect;
		HTMLElement.prototype.getBoundingClientRect = function () {
			return this.tagName === "HEADER"
				? ({ width: 500, height: 40 } as DOMRect)
				: rect.call(this);
		};
		try {
			await renderWithProviders(
				<header
					style={{
						padding: 0,
						borderLeftWidth: "0px",
						borderRightWidth: "0px",
					}}
				>
					<DeployButton />
				</header>,
				{ path: "/p/proj-1/production", routePath: "/p/$projectId/$envSlug" },
			);
			const key = await screen.findByRole("button", {
				name: /Deploy to production\. 2 staged changes\. Opens review\./,
			});
			expect(
				screen.queryByRole("button", { name: "Lift production guard" }),
			).toBeNull();
			await userEvent.setup().click(key);
			const sheet = await screen.findByRole("dialog");
			expect(
				within(sheet).getByRole("button", { name: "Lift production guard" }),
			).toBeInTheDocument();
		} finally {
			HTMLElement.prototype.getBoundingClientRect = rect;
		}
	});
});

function fireClick(element: HTMLElement) {
	act(() => element.click());
}
