import { describe, expect, it } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import type { SharedVariableView } from "#/api/gen/types.gen";
import { type ApiStub, stubApi } from "#/test/api";
import { make, type Overrides, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { SharedVariablesPage } from "./shared-variables-page";

const ENVIRONMENT = make("Environment", {
	id: "e1",
	projectId: "p1",
	slug: "prod",
	name: "Production",
	kind: "standard",
	isProduction: true,
});

/** `value: undefined` is a secret: the server never returns its value. */
function shared(over: Overrides<SharedVariableView> = {}): SharedVariableView {
	return make("SharedVariableView", {
		key: "REGION",
		value: "eu-1",
		isSecret: false,
		computedSecret: false,
		comment: "",
		generated: [],
		inherited: false,
		layer: "environment",
		overrides: false,
		set: true,
		source: "manual",
		updatedAt: "2026-01-01T00:00:00Z",
		usedBy: 0,
		version: 1,
		...over,
	});
}

const list = (items: SharedVariableView[] = []) =>
	make("SharedVariableList", { items });

function serviceView(id: string, name: string, slug: string) {
	return make("ServiceView", {
		id,
		name,
		slug,
		environmentId: "e1",
		projectId: "p1",
		kind: "app",
		sourceType: "image",
	});
}

const SERVICES = make("PageServiceView", {
	items: [
		serviceView("s-web", "Web", "web"),
		serviceView("s-api", "API", "api"),
	],
});

type Routes = Parameters<typeof stubApi>[0];

function stubPage(routes: Routes = {}): ApiStub {
	return stubApi({
		"GET /projects/p1/environments": {
			body: make("PageEnvironment", { items: [ENVIRONMENT] }),
		},
		"GET /environments/e1/services": { body: SERVICES },
		...routes,
	});
}

const renderPage = () =>
	renderWithProviders(<SharedVariablesPage projectId="p1" envSlug="prod" />);

const rowOf = (key: string) => {
	const row = screen.getByTitle(key).closest("li");
	if (!row) throw new Error(`no row for ${key}`);
	return row;
};

async function openMenu(user: UserEvent, key: string) {
	await user.click(screen.getByRole("button", { name: `Actions for ${key}` }));
}

describe("SharedVariablesPage: loading and failure", () => {
	it("shows a loading status, then the frame title and description", async () => {
		stubPage({
			"GET /environments/e1/shared-variables": { body: list() },
		});
		await renderPage();
		expect(
			await screen.findByRole("heading", { name: "Shared variables" }),
		).toBeInTheDocument();
		expect(
			await screen.findByText(/Values shared across Production/),
		).toHaveTextContent(/KEY=\$\{\{shared\.KEY\}\}.*\$\{\{team\.KEY\}\}/);
	});

	it("says the environment does not exist for an unknown slug", async () => {
		stubApi({
			"GET /projects/p1/environments": {
				body: make("PageEnvironment", { items: [ENVIRONMENT] }),
			},
		});
		await renderWithProviders(
			<SharedVariablesPage projectId="p1" envSlug="nope" />,
		);
		expect(
			await screen.findByText("Environment not found"),
		).toBeInTheDocument();
		expect(
			screen.getByText(/no environment called “nope”/),
		).toBeInTheDocument();
	});

	it("shows the load error and Try again refetches", async () => {
		const user = userEvent.setup();
		let calls = 0;
		const api = stubPage({
			"GET /environments/e1/shared-variables": () => {
				calls++;
				return calls === 1
					? Response.json(
							problem(500, {
								code: "internal_error",
								title: "Boom",
								detail: "database is down",
							}).body,
							{ status: 500 },
						)
					: Response.json(list([shared()]));
			},
		});
		await renderPage();
		expect(
			await screen.findByText("Shared variables did not load"),
		).toBeInTheDocument();
		expect(screen.getByText(/database is down/)).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: /try again|retry/i }));
		expect(await screen.findByTitle("REGION")).toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});
});

describe("SharedVariablesPage: listing", () => {
	it("offers the first variable from the empty state", async () => {
		const user = userEvent.setup();
		stubPage({
			"GET /environments/e1/shared-variables": { body: list() },
		});
		await renderPage();
		expect(
			await screen.findByText("No shared variables yet"),
		).toBeInTheDocument();
		await user.click(
			screen.getByRole("button", { name: "New shared variable" }),
		);
		expect(
			await screen.findByRole("heading", { name: "New shared variable" }),
		).toBeVisible();
	});

	it("renders each variable with its badges, hides secrets and counts only own variables", async () => {
		stubPage({
			"GET /environments/e1/shared-variables": {
				body: list([
					shared({ key: "REGION", comment: "where we run", usedBy: 2 }),
					shared({
						key: "SESSION",
						isSecret: true,
						value: undefined,
						source: "generated",
						generated: [{ fn: "secret", args: ["32", "hex"] }],
					}),
					shared({ key: "DSN", computedSecret: true, value: "pg://x" }),
					shared({ key: "EMPTY", value: "" }),
					shared({
						key: "HOST",
						layer: "project",
						inherited: true,
						value: "h",
					}),
					shared({ key: "TIER", overrides: true, value: "gold" }),
				]),
			},
		});
		await renderPage();
		await screen.findByTitle("REGION");

		// 5 of the 6 are this environment's own; HOST is inherited.
		expect(
			screen.getByText("5 variables shared with Production."),
		).toBeInTheDocument();

		const region = within(rowOf("REGION"));
		expect(region.getByText("eu-1")).toBeInTheDocument();
		expect(region.getByText("used by 2")).toBeInTheDocument();
		expect(region.getByText("where we run")).toBeInTheDocument();

		const session = within(rowOf("SESSION"));
		expect(session.getByText("Secret value, hidden")).toBeInTheDocument();
		expect(session.getByText("secret")).toBeInTheDocument();
		expect(
			session.getByText("generated · secret(32, hex)"),
		).toBeInTheDocument();
		expect(session.getByText("used by 0")).toBeInTheDocument();

		expect(
			within(rowOf("DSN")).getByText("contains a secret"),
		).toBeInTheDocument();
		expect(within(rowOf("DSN")).getByText("pg://x")).toBeInTheDocument();
		expect(within(rowOf("EMPTY")).getByText("empty")).toBeInTheDocument();
		expect(within(rowOf("HOST")).getByText("from project")).toBeInTheDocument();
		expect(
			within(rowOf("TIER")).getByText("overrides project"),
		).toBeInTheDocument();
	});

	it("uses the singular for a single variable", async () => {
		stubPage({
			"GET /environments/e1/shared-variables": { body: list([shared()]) },
		});
		await renderPage();
		expect(
			await screen.findByText("1 variable shared with Production."),
		).toBeInTheDocument();
	});

	it("switches layers: the project list, then the team list", async () => {
		const user = userEvent.setup();
		const api = stubPage({
			"GET /environments/e1/shared-variables": { body: list([shared()]) },
			"GET /projects/p1/shared-variables": {
				body: list([shared({ key: "PROJ_KEY", layer: "project", value: "p" })]),
			},
			"GET /team/shared-variables": {
				body: list([shared({ key: "TEAM_KEY", layer: "team", value: "t" })]),
			},
		});
		await renderPage();
		await screen.findByTitle("REGION");

		await user.click(screen.getByRole("button", { name: "Whole project" }));
		expect(await screen.findByTitle("PROJ_KEY")).toBeInTheDocument();
		expect(screen.queryByTitle("REGION")).toBeNull();
		expect(
			screen.getByText(
				"1 variable shared with every environment of this project.",
			),
		).toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "Whole team" }));
		expect(await screen.findByTitle("TEAM_KEY")).toBeInTheDocument();
		expect(
			screen.getByText("1 variable shared with every project of this team."),
		).toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});

	it("shows why the team list did not load (a member without access)", async () => {
		const user = userEvent.setup();
		stubPage({
			"GET /environments/e1/shared-variables": { body: list([shared()]) },
			"GET /team/shared-variables": problem(403, {
				code: "forbidden",
				title: "Forbidden",
				detail: "team variables need team access",
			}),
		});
		await renderPage();
		await screen.findByTitle("REGION");
		await user.click(screen.getByRole("button", { name: "Whole team" }));
		expect(
			await screen.findByText("Shared variables did not load"),
		).toBeInTheDocument();
		expect(
			screen.getByText(/team variables need team access/),
		).toBeInTheDocument();
	});
});

describe("SharedVariablesPage: row actions", () => {
	it("Edit opens the variable's dialog; an inherited variable cannot be edited or deleted", async () => {
		const user = userEvent.setup();
		stubPage({
			"GET /environments/e1/shared-variables": {
				body: list([
					shared(),
					shared({ key: "HOST", layer: "project", inherited: true }),
				]),
			},
		});
		await renderPage();
		await screen.findByTitle("REGION");

		await openMenu(user, "HOST");
		expect(
			await screen.findByRole("menuitem", { name: "Edit" }),
		).toHaveAttribute("aria-disabled", "true");
		expect(screen.getByRole("menuitem", { name: "Delete" })).toHaveAttribute(
			"aria-disabled",
			"true",
		);
		expect(
			screen.getByRole("menuitem", { name: "Share to services…" }),
		).not.toHaveAttribute("aria-disabled", "true");
		await user.keyboard("{Escape}");

		await openMenu(user, "REGION");
		await user.click(await screen.findByRole("menuitem", { name: "Edit" }));
		expect(
			await screen.findByRole("heading", { name: "Edit REGION" }),
		).toBeVisible();
		expect(screen.getByLabelText("Value")).toHaveValue("eu-1");
	});

	it("Share to services stages the reference on the chosen service", async () => {
		const user = userEvent.setup();
		const api = stubPage({
			"GET /environments/e1/shared-variables": { body: list([shared()]) },
			"POST /environments/e1/shared-variables/:key/share": {
				body: make("SharedVariableList"),
			},
		});
		await renderPage();
		await screen.findByTitle("REGION");
		await openMenu(user, "REGION");
		await user.click(
			await screen.findByRole("menuitem", { name: "Share to services…" }),
		);
		expect(
			await screen.findByRole("heading", { name: "Share REGION" }),
		).toBeVisible();
		await user.click(await screen.findByRole("checkbox", { name: /API/ }));
		await user.click(screen.getByRole("button", { name: /^Stage on/ }));
		expect(await screen.findByText("Shared REGION")).toBeInTheDocument();
		const post = api.calls.find((call) => call.path.endsWith("/share"));
		expect(post?.path).toBe("/environments/e1/shared-variables/REGION/share");
		expect(post?.body).toEqual({ serviceIds: ["s-api"] });
	});

	it("Rotate is offered only for a generated, non-inherited variable", async () => {
		const user = userEvent.setup();
		stubPage({
			"GET /environments/e1/shared-variables": {
				body: list([
					shared(),
					shared({ key: "SESSION", source: "generated", isSecret: true }),
					shared({
						key: "PROJ_GEN",
						source: "generated",
						layer: "project",
						inherited: true,
					}),
				]),
			},
		});
		await renderPage();
		await screen.findByTitle("REGION");
		for (const key of ["REGION", "PROJ_GEN"]) {
			await openMenu(user, key);
			await screen.findByRole("menuitem", { name: "Edit" });
			expect(screen.queryByRole("menuitem", { name: "Rotate…" })).toBeNull();
			await user.keyboard("{Escape}");
			await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
		}
		await openMenu(user, "SESSION");
		expect(
			await screen.findByRole("menuitem", { name: "Rotate…" }),
		).toBeInTheDocument();
	});

	it("Rotate plans with a dry run first, then stages the real rotation", async () => {
		const user = userEvent.setup();
		const api = stubPage({
			"GET /environments/e1/shared-variables": {
				body: list([
					shared({ key: "SESSION", source: "generated", isSecret: true }),
				]),
			},
			"POST /environments/e1/shared-variables/:key/rotate": async (request) => {
				const body = (await request.clone().json()) as { dryRun: boolean };
				return Response.json(
					make("VariableRotationPlan", {
						key: "SESSION",
						kind: "app-secret",
						staged: !body.dryRun,
						affected: [
							{
								serviceId: "s-web",
								slug: "web",
								via: ["SESSION"],
								strategy: "rolling",
								downtime: "none",
							},
						],
						blockers: [],
						deployOrder: ["web", "api"],
						warnings: ["workers keep the old value until restarted"],
					}),
				);
			},
		});
		await renderPage();
		await screen.findByTitle("SESSION");
		await openMenu(user, "SESSION");
		await user.click(await screen.findByRole("menuitem", { name: "Rotate…" }));

		expect(
			await screen.findByRole("heading", { name: "Rotate SESSION" }),
		).toBeVisible();
		expect(await screen.findByText("web")).toBeInTheDocument();
		expect(
			screen.getByText("workers keep the old value until restarted"),
		).toBeInTheDocument();
		const rotations = () =>
			api.calls.filter((call) => call.path.endsWith("/rotate"));
		expect(rotations().map((call) => call.body)).toEqual([{ dryRun: true }]);

		await user.click(screen.getByRole("button", { name: "Stage rotation" }));
		expect(await screen.findByText("Rotated SESSION")).toBeInTheDocument();
		expect(rotations().map((call) => call.body)).toEqual([
			{ dryRun: true },
			{ dryRun: false },
		]);
		expect(rotations()[1]?.path).toBe(
			"/environments/e1/shared-variables/SESSION/rotate",
		);
	});

	it("Rotate on the team layer plans against the team endpoint", async () => {
		const user = userEvent.setup();
		const api = stubPage({
			"GET /environments/e1/shared-variables": { body: list() },
			"GET /team/shared-variables": {
				body: list([
					shared({
						key: "ORG_TOKEN",
						layer: "team",
						source: "generated",
						isSecret: true,
					}),
				]),
			},
			"POST /team/shared-variables/:key/rotate": {
				body: make("VariableRotationPlan", {
					key: "ORG_TOKEN",
					kind: "app-secret",
					staged: false,
					affected: [],
					blockers: [],
					deployOrder: [],
					warnings: [],
				}),
			},
		});
		await renderPage();
		await screen.findByText("No shared variables yet");
		await user.click(screen.getByRole("button", { name: "Whole team" }));
		await screen.findByTitle("ORG_TOKEN");
		await openMenu(user, "ORG_TOKEN");
		await user.click(await screen.findByRole("menuitem", { name: "Rotate…" }));
		await screen.findByRole("heading", { name: "Rotate ORG_TOKEN" });
		await waitFor(() =>
			expect(api.calls.map((c) => `${c.method} ${c.path}`)).toContain(
				"POST /team/shared-variables/ORG_TOKEN/rotate",
			),
		);
		expect(api.calls.find((c) => c.path.endsWith("/rotate"))?.body).toEqual({
			dryRun: true,
		});
	});

	it("Delete asks first, names the references that will break, then deletes", async () => {
		const user = userEvent.setup();
		let items = [shared({ usedBy: 2 }), shared({ key: "OTHER", value: "o" })];
		const api = stubPage({
			"GET /environments/e1/shared-variables": () => Response.json(list(items)),
			"DELETE /environments/e1/shared-variables/:key": () => {
				items = items.filter((item) => item.key !== "REGION");
				return Response.json(list(items));
			},
		});
		await renderPage();
		await screen.findByTitle("REGION");
		await openMenu(user, "REGION");
		await user.click(await screen.findByRole("menuitem", { name: "Delete" }));

		expect(await screen.findByText("Delete REGION?")).toBeInTheDocument();
		expect(
			screen.getByText(
				/2 variables reference it\. Services that resolved to it are restaged and will fail to deploy until the references are fixed, unless a project value of the same name exists\./,
			),
		).toBeInTheDocument();
		expect(api.calls.some((call) => call.method === "DELETE")).toBe(false);

		await user.click(screen.getByRole("button", { name: "Delete variable" }));
		expect(await screen.findByText("Deleted REGION")).toBeInTheDocument();
		const del = api.calls.find((call) => call.method === "DELETE");
		expect(del?.path).toBe("/environments/e1/shared-variables/REGION");
		await waitFor(() => expect(screen.queryByTitle("REGION")).toBeNull());
		expect(screen.getByTitle("OTHER")).toBeInTheDocument();
	});

	it("Delete of an unreferenced project variable says nothing references it", async () => {
		const user = userEvent.setup();
		const api = stubPage({
			"GET /environments/e1/shared-variables": { body: list() },
			"GET /projects/p1/shared-variables": {
				body: list([shared({ layer: "project", usedBy: 1 })]),
			},
		});
		await renderPage();
		await screen.findByText("No shared variables yet");
		await user.click(screen.getByRole("button", { name: "Whole project" }));
		await screen.findByTitle("REGION");
		await openMenu(user, "REGION");
		await user.click(await screen.findByRole("menuitem", { name: "Delete" }));
		// One referencing variable: singular, and no "unless a project value" hint.
		expect(
			await screen.findByText(
				/^1 variable reference it\. Services that resolved to it are restaged and will fail to deploy until the references are fixed\.$/,
			),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(api.calls.some((call) => call.method === "DELETE")).toBe(false);
	});

	it("Delete of an unused variable says so, and a failing delete keeps the row", async () => {
		const user = userEvent.setup();
		const api = stubPage({
			"GET /environments/e1/shared-variables": { body: list([shared()]) },
			"DELETE /environments/e1/shared-variables/:key": problem(500, {
				code: "internal_error",
				title: "Boom",
				detail: "cannot delete right now",
			}),
		});
		await renderPage();
		await screen.findByTitle("REGION");
		await openMenu(user, "REGION");
		await user.click(await screen.findByRole("menuitem", { name: "Delete" }));
		expect(
			await screen.findByText("Nothing references it."),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Delete variable" }));
		await waitFor(() =>
			expect(api.calls.some((call) => call.method === "DELETE")).toBe(true),
		);
		expect(screen.queryByText("Deleted REGION")).toBeNull();
		expect(screen.getByTitle("REGION")).toBeInTheDocument();
	});
});
