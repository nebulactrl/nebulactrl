import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import {
	allowSlowRoutes,
	hrefOf,
	loadRoute,
	ME,
	renderApp,
	signedInApi,
} from "./route-harness";

allowSlowRoutes();

const unauthorized = problem(401, {
	code: "unauthorized",
	title: "Unauthorized",
});

describe("the session gate", () => {
	test("a fresh install sends every page to /setup", async () => {
		const api = stubApi({
			"GET /setup": {
				body: make("SetupStatusOutputBody", {
					needsSetup: true,
					tokenRequired: true,
				}),
			},
		});
		const router = await loadRoute("/projects-or-anything");
		expect(hrefOf(router)).toBe("/setup");
		expect(api.calls.map((c) => c.path)).toEqual(["/setup"]);
	});

	test("/setup itself opens on a fresh install, without asking who the user is", async () => {
		const api = stubApi({
			"GET /setup": {
				body: make("SetupStatusOutputBody", {
					needsSetup: true,
					tokenRequired: false,
				}),
			},
		});
		const router = await loadRoute("/setup");
		expect(hrefOf(router)).toBe("/setup");
		expect(api.unmatched).toEqual([]);
	});

	test("/setup on an installation that is set up moves to sign in with a notice", async () => {
		stubApi({
			"GET /setup": {
				body: make("SetupStatusOutputBody", {
					needsSetup: false,
					tokenRequired: false,
				}),
			},
		});
		const router = await loadRoute("/setup");
		expect(hrefOf(router)).toBe("/login?notice=already-set-up");
	});

	test("a signed-out visitor is sent to sign in, remembering the page and its query", async () => {
		stubApi({
			"GET /setup": {
				body: make("SetupStatusOutputBody", {
					needsSetup: false,
					tokenRequired: false,
				}),
			},
			"GET /me": unauthorized,
		});
		const router = await loadRoute("/servers?view=fleet");
		expect(router.state.location.pathname).toBe("/login");
		expect(router.state.location.search).toEqual({
			returnTo: "/servers?view=fleet",
		});
	});

	test.each([
		"/login",
		"/invite/abc123",
		"/forgot-password",
		"/reset-password",
	])("%s opens without a session", async (path) => {
		stubApi({
			"GET /setup": {
				body: make("SetupStatusOutputBody", {
					needsSetup: false,
					tokenRequired: false,
				}),
			},
			"GET /me": unauthorized,
			"GET /auth/providers": { body: { providers: [] } },
			"GET /auth/password-reset": { body: { available: false } },
		});
		const router = await loadRoute(path);
		expect(router.state.location.pathname).toBe(path);
	});

	test("a signed-in visitor who opens /login goes to the projects page", async () => {
		signedInApi();
		const router = await loadRoute("/login");
		expect(hrefOf(router)).toBe("/");
	});

	test("an error other than 401 from /me is not mistaken for a signed-out visitor", async () => {
		stubApi({
			"GET /setup": {
				body: make("SetupStatusOutputBody", {
					needsSetup: false,
					tokenRequired: false,
				}),
			},
			"GET /me": problem(500, { code: "internal", title: "Boom" }),
		});
		const router = await loadRoute("/servers");
		expect(router.state.location.pathname).toBe("/servers");
		expect(router.state.matches[0]?.status).toBe("error");
	});

	test("the docs open without waiting for setup or a session", async () => {
		const api = stubApi({
			"GET /me": unauthorized,
		});
		const router = await loadRoute("/docs");
		expect(router.state.location.pathname).toBe("/docs");
		expect(api.calls.map((c) => c.path)).toEqual(["/me"]);
	});

	test("the docs open when the control plane cannot be reached at all", async () => {
		globalThis.fetch = (() =>
			Promise.reject(
				new TypeError("Failed to fetch"),
			)) as unknown as typeof fetch;
		const router = await loadRoute("/docs");
		expect(router.state.location.pathname).toBe("/docs");
	});

	test("a failing shell query (projects, servers, system) does not block the page", async () => {
		signedInApi({
			"GET /projects": problem(500, { code: "internal" }),
			"GET /servers": problem(500, { code: "internal" }),
			"GET /system": problem(500, { code: "internal" }),
		});
		const router = await loadRoute("/");
		expect(router.state.location.pathname).toBe("/");
		expect(router.state.matches[0]?.status).toBe("success");
	});
});

describe("the root component", () => {
	test("a signed-in page sits inside the console shell with the account in it", async () => {
		signedInApi();
		await renderApp("/");
		expect(
			await screen.findByRole("heading", { name: "Projects" }),
		).toBeInTheDocument();
		expect(
			screen.getAllByText(ME.name).length +
				screen.getAllByText(ME.email).length,
		).toBeGreaterThan(0);
	});

	test("a signed-out page has no shell around it", async () => {
		stubApi({
			"GET /setup": {
				body: make("SetupStatusOutputBody", {
					needsSetup: false,
					tokenRequired: false,
				}),
			},
			"GET /me": unauthorized,
			"GET /auth/providers": { body: { providers: [] } },
			"GET /auth/password-reset": { body: { available: true } },
		});
		await renderApp("/login");
		expect(
			await screen.findByRole("heading", { name: "Sign in" }),
		).toBeInTheDocument();
		expect(screen.queryByText(ME.name)).not.toBeInTheDocument();
		expect(
			screen.queryByRole("navigation", { name: "Primary" }),
		).not.toBeInTheDocument();
	});

	test("an unknown address shows the not-found page, with a way back to the projects", async () => {
		signedInApi();
		const { router } = await renderApp("/nowhere/at/all");
		expect(
			await screen.findByRole("button", { name: "Go to projects" }),
		).toBeInTheDocument();
		expect(document.title).toBe("Page not found · NebulaCtrl");
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Go to projects" }));
		await waitFor(() => expect(router.state.location.pathname).toBe("/"));
	});
});

describe("when the page cannot load", () => {
	test("a control-plane failure shows the error panel, and Try again loads the page once it is back", async () => {
		let meCalls = 0;
		signedInApi({
			"GET /me": () => {
				meCalls++;
				return meCalls === 1
					? Response.json(
							{
								code: "internal",
								title: "Internal",
								detail: "The control plane is restarting.",
							},
							{ status: 500 },
						)
					: Response.json(ME);
			},
		});
		await renderApp("/");
		expect(
			await screen.findByText("NebulaCtrl could not load this page"),
		).toBeInTheDocument();
		expect(
			screen.getByText("The control plane is restarting."),
		).toBeInTheDocument();
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Try again" }));
		expect(
			await screen.findByRole("heading", { name: "Projects" }),
		).toBeInTheDocument();
		expect(
			screen.queryByText("NebulaCtrl could not load this page"),
		).not.toBeInTheDocument();
	});
});
