import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import {
	allowSlowRoutes,
	ME,
	renderApp,
	SHELL_ROUTES,
	signedInApi,
} from "./route-harness";

allowSlowRoutes();

const notFound = problem(404, { code: "not_found", title: "Not found" });

const invite = make("InvitePreview", {
	teamName: "Acme",
	teamSlug: "acme",
	role: "developer",
	invitedBy: "Grace",
	expiresAt: "2026-12-01T00:00:00Z",
});

/** Nobody signed in; the invite is previewable. */
function signedOut(extra: Parameters<typeof stubApi>[0] = {}) {
	return stubApi({
		...SHELL_ROUTES,
		"GET /me": problem(401, { code: "unauthorized" }),
		...extra,
	});
}

describe("the invite page", () => {
	test("a signed-out visitor creates an account with the invited email and joins", async () => {
		const api = signedOut({
			"GET /invites/:token": {
				body: { ...invite, email: "new@example.com" },
			},
			"POST /invites/:token/accept": { body: make("AcceptedInvite") },
		});
		await renderApp("/invite/tok-1");
		expect(
			await screen.findByRole("heading", { name: "Join Acme" }),
		).toBeInTheDocument();
		expect(screen.getByText("Grace")).toBeInTheDocument();
		// The invite is bound to an email: it is shown, not editable.
		expect(screen.getByLabelText("Email")).toHaveValue("new@example.com");
		expect(screen.getByLabelText("Email")).toHaveAttribute("readonly");

		const user = userEvent.setup();
		await user.type(screen.getByLabelText("Your name"), " New Person ");
		await user.type(screen.getByLabelText("Password"), "a-long-password");
		await user.click(
			screen.getByRole("button", { name: "Create account and join" }),
		);
		await waitFor(() =>
			expect(
				api.calls.find((c) => c.path === "/invites/tok-1/accept")?.body,
			).toEqual({
				name: "New Person",
				password: "a-long-password",
			}),
		);
	});

	test("an invite not bound to an email asks for one", async () => {
		const api = signedOut({
			"GET /invites/:token": { body: invite },
			"POST /invites/:token/accept": { body: make("AcceptedInvite") },
		});
		await renderApp("/invite/tok-2");
		const user = userEvent.setup();
		await user.type(await screen.findByLabelText("Your name"), "Nia");
		await user.type(screen.getByLabelText("Email"), " nia@example.com ");
		await user.type(screen.getByLabelText("Password"), "a-long-password");
		await user.click(
			screen.getByRole("button", { name: "Create account and join" }),
		);
		await waitFor(() =>
			expect(
				api.calls.find((c) => c.path === "/invites/tok-2/accept")?.body,
			).toEqual({
				name: "Nia",
				email: "nia@example.com",
				password: "a-long-password",
			}),
		);
	});

	test("a signed-in visitor only confirms, with an empty body", async () => {
		const api = signedInApi({
			"GET /invites/:token": { body: invite },
			"POST /invites/:token/accept": { body: make("AcceptedInvite") },
		});
		await renderApp("/invite/tok-3");
		expect(
			await screen.findByRole("button", { name: "Join Acme" }),
		).toBeInTheDocument();
		expect(screen.queryByLabelText("Password")).not.toBeInTheDocument();
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Join Acme" }));
		await waitFor(() =>
			expect(
				api.calls.find((c) => c.path === "/invites/tok-3/accept")?.body,
			).toEqual({}),
		);
	});

	test.each([
		[
			"invite_expired",
			"This invite has expired. Ask the person who invited you for a new one.",
		],
		[
			"invite_used",
			"This invite has already been used. Ask for a new one, or sign in if you already joined.",
		],
		[
			"not_found",
			"This invite link is not valid. It may have been revoked or mistyped.",
		],
	])("a %s invite says why it cannot be used", async (code, text) => {
		signedOut({
			"GET /invites/:token": {
				status: 410,
				body: { status: 410, code, title: code },
			},
		});
		await renderApp("/invite/dead");
		expect(
			await screen.findByRole("heading", { name: "Invite unavailable" }),
		).toBeInTheDocument();
		expect(screen.getByText(text)).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Go to sign in" }),
		).toBeInTheDocument();
	});

	test("the invite page has no console around it even when signed in", async () => {
		signedInApi({ "GET /invites/:token": { body: invite } });
		await renderApp("/invite/tok-4");
		await screen.findByRole("heading", { name: "Join Acme" });
		expect(screen.queryByText(ME.email)).not.toBeInTheDocument();
	});
});

describe("the password reset pages", () => {
	test("a reset link is checked against the token in the address", async () => {
		const api = signedOut({
			"POST /auth/password-reset/check": problem(400, {
				code: "reset_token_invalid",
			}),
		});
		await renderApp("/reset-password?token=tk-9");
		await waitFor(() =>
			expect(
				api.calls.find((c) => c.path === "/auth/password-reset/check")?.body,
			).toEqual({
				token: "tk-9",
			}),
		);
	});

	test("without a token nothing is checked", async () => {
		const api = signedOut();
		await renderApp("/reset-password");
		await waitFor(() => expect(api.calls.length).toBeGreaterThan(0));
		expect(api.calls.some((c) => c.path === "/auth/password-reset/check")).toBe(
			false,
		);
	});

	test("the forgot-password page asks whether reset by email is available", async () => {
		const api = signedOut({
			"GET /auth/password-reset": { body: { available: true } },
		});
		await renderApp("/forgot-password");
		await waitFor(() =>
			expect(api.calls.some((c) => c.path === "/auth/password-reset")).toBe(
				true,
			),
		);
	});
});

describe("pages that take an id from the address", () => {
	test("an approval request is fetched by the id in the address", async () => {
		const api = signedInApi({ "GET /approvals/:id": notFound });
		await renderApp("/approvals/ap-42");
		await waitFor(() =>
			expect(api.calls.some((c) => c.path === "/approvals/ap-42")).toBe(true),
		);
	});

	test("a status page is fetched by the id in the address", async () => {
		const api = signedInApi({ "GET /status-pages/:id": notFound });
		await renderApp("/status-pages/sp-7");
		await waitFor(() =>
			expect(api.calls.some((c) => c.path === "/status-pages/sp-7")).toBe(true),
		);
	});

	test("an incident is fetched by the id in the address", async () => {
		const api = signedInApi({
			"GET /status-incidents/:id": notFound,
			"GET /status-pages": { body: { items: [] } },
		});
		await renderApp("/status-pages/incidents/inc-3");
		await waitFor(() =>
			expect(api.calls.some((c) => c.path === "/status-incidents/inc-3")).toBe(
				true,
			),
		);
	});

	test("a server page says so when the server does not exist", async () => {
		signedInApi({ "GET /servers/:id": notFound });
		await renderApp("/servers/ghost");
		expect(
			await screen.findByText("This server does not exist"),
		).toBeInTheDocument();
	});

	test("every server tab loads the server named in the address", async () => {
		for (const tab of [
			"traffic",
			"proxy",
			"security",
			"updates",
			"settings/general",
		]) {
			const api = signedInApi({ "GET /servers/:id": notFound });
			const { unmount } = await renderApp(`/servers/srv-5/${tab}`);
			await waitFor(() =>
				expect(api.calls.some((c) => c.path === "/servers/srv-5")).toBe(true),
			);
			unmount();
		}
	});

	test("a project's settings page loads that project's environments", async () => {
		const api = signedInApi({
			"GET /projects/:id/environments": {
				body: make("PageEnvironment", { items: [] }),
			},
		});
		await renderApp("/p/proj-8/settings");
		await waitFor(() =>
			expect(
				api.calls.some((c) => c.path === "/projects/proj-8/environments"),
			).toBe(true),
		);
	});
});

describe("the project canvas route", () => {
	const canvasApi = (extra: Parameters<typeof stubApi>[0] = {}) =>
		signedInApi({
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
			"GET /projects/:p/environments/:e/canvas": {
				body: make("Canvas", {
					version: 0,
					layout: { nodes: {}, groups: [], viewport: { x: 0, y: 0, zoom: 1 } },
				}),
			},
			...extra,
		});

	test("an address naming a service that does not exist drops it, with the tab, search and section that belonged to it", async () => {
		canvasApi({ "GET /services/:id": notFound });
		const { router } = await renderApp(
			"/p/proj-1/production?service=ghost&tab=logs&q=oops&section=general&debug=dep-1",
		);
		await waitFor(() =>
			expect(router.state.location.href).toBe("/p/proj-1/production"),
		);
	});

	test("a ?template= link opens that template and leaves the address clean", async () => {
		const api = canvasApi({
			"GET /catalog/templates/:id": problem(404, { code: "not_found" }),
		});
		const { router } = await renderApp(
			"/p/proj-1/production?template=postgres",
		);
		await waitFor(() =>
			expect(
				api.calls.some((c) => c.path === "/catalog/templates/postgres"),
			).toBe(true),
		);
		await waitFor(() =>
			expect(router.state.location.href).toBe("/p/proj-1/production"),
		);
	});

	test("the canvas loads the project's environment and its saved layout from the address", async () => {
		const api = canvasApi();
		await renderApp("/p/proj-1/production");
		expect(
			await screen.findByText("Create your first service"),
		).toBeInTheDocument();
		await waitFor(() =>
			expect(
				api.calls.some(
					(c) => c.path === "/projects/proj-1/environments/env-1/canvas",
				),
			).toBe(true),
		);
		expect(
			api.calls.some((c) => c.path === "/projects/proj-1/environments"),
		).toBe(true);
	});
});

describe("the server route", () => {
	const server = make("Server", {
		id: "srv-1",
		name: "edge-1",
		status: "online",
		createdAt: "2026-01-01T00:00:00Z",
		serviceCount: 0,
		drainState: "none",
		drainMessage: "",
		acceptWorkloads: true,
		buildEnabled: false,
		buildMaxConcurrent: 2,
		buildAllowEmulation: false,
		buildAllowPreviews: false,
		controlPlane: false,
		unrestricted: false,
		canDecommission: true,
		proxy: { status: "running", v2: true, generation: 1, appliedGeneration: 1 },
		patches: {
			known: false,
			supported: false,
			updates: 0,
			security: 0,
			rebootRequired: false,
			checkedAt: null,
			busy: "",
		},
	});
	const serverApi = () =>
		signedInApi({
			"GET /servers/:id": { body: server },
			"GET /servers": { body: make("PageServer", { items: [server] }) },
		});

	test("?drain=1 opens the drain sheet for that server on whichever tab is open", async () => {
		serverApi();
		await renderApp("/servers/srv-1/traffic?drain=1");
		expect(await screen.findByText("Hold to drain edge-1")).toBeInTheDocument();
	});

	test("without ?drain the sheet stays closed", async () => {
		serverApi();
		await renderApp("/servers/srv-1/traffic");
		expect(
			await screen.findByRole("heading", { name: "edge-1" }),
		).toBeInTheDocument();
		expect(screen.queryByText("Hold to drain edge-1")).not.toBeInTheDocument();
	});

	test("closing the drain sheet takes ?drain out of the address and keeps the tab", async () => {
		serverApi();
		const { router } = await renderApp("/servers/srv-1/traffic?drain=1");
		await screen.findByText("Hold to drain edge-1");
		const close = screen
			.getAllByRole("button", { name: "Close" })
			.find((button) => button.closest("[role=dialog]"));
		expect(close).toBeDefined();
		await userEvent.setup().click(close as HTMLElement);
		await waitFor(() =>
			expect(router.state.location.href).toBe("/servers/srv-1/traffic"),
		);
		await waitFor(() =>
			expect(
				screen.queryByText("Hold to drain edge-1"),
			).not.toBeInTheDocument(),
		);
	});

	test("the tabs of one server link to its own pages", async () => {
		serverApi();
		await renderApp("/servers/srv-1");
		await screen.findByRole("heading", { name: "edge-1" });
		const href = (name: string) =>
			screen.getByRole("link", { name }).getAttribute("href");
		expect(href("Overview")).toBe("/servers/srv-1");
		expect(href("Traffic")).toBe("/servers/srv-1/traffic");
		expect(href("Proxy")).toBe("/servers/srv-1/proxy");
		expect(href("Security")).toBe("/servers/srv-1/security");
		expect(href("Updates")).toBe("/servers/srv-1/updates");
	});
});

describe("the servers list route", () => {
	const aServer = make("Server", {
		id: "srv-1",
		name: "edge-1",
		status: "online",
		createdAt: "2026-01-01T00:00:00Z",
		serviceCount: 0,
		drainState: "none",
		drainMessage: "",
		acceptWorkloads: true,
		buildEnabled: false,
		buildMaxConcurrent: 2,
		buildAllowEmulation: false,
		buildAllowPreviews: false,
		controlPlane: false,
		unrestricted: false,
		proxy: { status: "running", v2: true, generation: 1, appliedGeneration: 1 },
	});

	test("?enroll=1 opens the enrolment dialog and closing it takes the parameter out of the address", async () => {
		signedInApi();
		const { router } = await renderApp("/servers?enroll=1");
		expect(await screen.findByText("Add a server")).toBeInTheDocument();
		await userEvent.setup().keyboard("{Escape}");
		await waitFor(() => expect(router.state.location.href).toBe("/servers"));
		await waitFor(() =>
			expect(screen.queryByText("Add a server")).not.toBeInTheDocument(),
		);
	});

	test("Add server puts ?enroll=1 in the address and keeps the chosen view", async () => {
		signedInApi({
			"GET /servers": { body: make("PageServer", { items: [aServer] }) },
		});
		const { router } = await renderApp("/servers?view=fleet");
		await screen.findByRole("heading", { name: "Servers" });
		await userEvent
			.setup()
			.click(
				screen.getAllByRole("button", { name: "Add server" })[0] as HTMLElement,
			);
		await waitFor(() =>
			expect(router.state.location.href).toBe(
				"/servers?view=fleet&enroll=true",
			),
		);
	});
});
