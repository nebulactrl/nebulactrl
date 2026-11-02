import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { make } from "#/test/factories";
import { allowSlowRoutes, renderApp, signedInApi } from "./route-harness";

const project = (id: string, name: string) =>
	make("Project", { id, name, slug: id });

const servers = make("PageServer", {
	items: [make("Server", { id: "srv-1", name: "edge-1", serviceCount: 0 })],
});
const noEnvironments = make("PageEnvironment", { items: [] });

allowSlowRoutes();

const page = () => screen.getByRole("main");
const EMPTY_TITLE = "Create your first project";

describe("the projects page", () => {
	test("lists each project as a link to its canvas", async () => {
		signedInApi({
			"GET /projects": {
				body: make("PageProject", {
					items: [project("shop", "Shop"), project("blog", "Blog")],
				}),
			},
			"GET /servers": { body: servers },
			"GET /projects/:id/environments": {
				body: make("PageEnvironment", {
					items: [
						make("Environment", {
							id: "env-1",
							name: "production",
							slug: "production",
							isProduction: true,
						}),
					],
				}),
			},
			"GET /environments/:id/services": { body: { items: [] } },
		});
		await renderApp("/");
		const shop = await within(await screen.findByRole("main")).findByRole(
			"link",
			{
				name: /Shop/,
			},
		);
		expect(shop).toHaveAttribute("href", "/p/shop/production");
		expect(within(page()).getByRole("link", { name: /Blog/ })).toHaveAttribute(
			"href",
			"/p/blog/production",
		);
		expect(document.title).toBe("Projects · NebulaCtrl");
		expect(screen.queryByText(EMPTY_TITLE)).not.toBeInTheDocument();
	});

	test("with projects it offers New project in the header", async () => {
		signedInApi({
			"GET /projects": {
				body: make("PageProject", { items: [project("shop", "Shop")] }),
			},
			"GET /servers": { body: servers },
			"GET /projects/:id/environments": { body: noEnvironments },
		});
		await renderApp("/");
		await within(await screen.findByRole("main")).findByRole("link", {
			name: /Shop/,
		});
		expect(
			within(page()).getByRole("button", { name: "New project" }),
		).toBeInTheDocument();
	});

	test("with no project it invites the first one, and points at docs when a server exists", async () => {
		signedInApi({ "GET /servers": { body: servers } });
		await renderApp("/");
		expect(await screen.findByText(EMPTY_TITLE)).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "New project" }),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				/split into environments such as production and staging\.$/,
			),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Add a server" }),
		).not.toBeInTheDocument();
		expect(screen.getByText("Read about projects")).toBeInTheDocument();
	});

	test("with no project and no server it says a server is needed and offers to add one", async () => {
		signedInApi();
		const { router } = await renderApp("/");
		expect(
			await screen.findByText(
				/this team has none yet: add one before you deploy/,
			),
		).toBeInTheDocument();
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Add a server" }));
		await waitFor(() =>
			expect(router.state.location.pathname).toBe("/servers"),
		);
		expect(router.state.location.search).toMatchObject({ enroll: true });
	});

	test("a failed load shows the error with a retry that asks again", async () => {
		let calls = 0;
		signedInApi({
			"GET /projects": () => {
				calls++;
				// The route guard reads the list once; the page's own request is the next.
				return calls <= 2
					? Response.json({ code: "internal", title: "Boom" }, { status: 500 })
					: Response.json({ items: [project("shop", "Shop")] });
			},
			"GET /projects/:id/environments": { body: noEnvironments },
		});
		await renderApp("/");
		expect(
			await screen.findByText("Projects did not load"),
		).toBeInTheDocument();
		expect(screen.queryByText(EMPTY_TITLE)).not.toBeInTheDocument();
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: /try again|retry/i }));
		expect(
			await within(await screen.findByRole("main")).findByRole("link", {
				name: /Shop/,
			}),
		).toBeInTheDocument();
	});
});
