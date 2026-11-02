import { beforeEach, describe, expect, test } from "bun:test";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import {
	hrefOf,
	isNotFoundPage,
	loadRoute,
	signedInApi,
	validatedSearch,
} from "./route-harness";

// Old addresses (mail, webhooks, bookmarks) and unknown sections move to the
// page that holds them now; the search a flow carries survives the move.

beforeEach(() => {
	signedInApi();
});

describe("settings addresses", () => {
	test.each([
		["/settings", "/settings/general"],
		["/settings#members", "/settings/members"],
		["/settings?section=audit", "/settings/audit-log"],
		["/settings#mesh", "/settings/server-mesh"],
		["/settings#profile", "/account/profile"],
		["/settings#mcp", "/account/mcp-clients"],
		["/settings#version", "/admin/updates"],
		["/settings#sso", "/admin/sign-in"],
		["/settings#nothing-like-it", "/settings/general"],
	])("%s moves to %s", async (from, to) => {
		const router = await loadRoute(from);
		expect(router.state.location.pathname).toBe(to);
	});

	test("the hash wins over ?section=", async () => {
		const router = await loadRoute("/settings?section=audit#members");
		expect(router.state.location.pathname).toBe("/settings/members");
	});

	test("the git flow's parameters travel along; others are dropped", async () => {
		const router = await loadRoute(
			"/settings?section=integrations&git_connected=github&git_notice=hello&utm=x",
		);
		expect(router.state.location.pathname).toBe("/settings/integrations");
		expect(validatedSearch(router, "/settings/$section")).toEqual({
			git_connected: "github",
			git_notice: "hello",
		});
	});

	test("a renamed team section opens its new page", async () => {
		const router = await loadRoute("/settings/built-in-services");
		expect(router.state.location.pathname).toBe("/settings/headscale");
	});

	test("an unknown team section opens the first page", async () => {
		const router = await loadRoute("/settings/does-not-exist");
		expect(router.state.location.pathname).toBe("/settings/general");
	});

	test("a known team section stays where it is", async () => {
		const router = await loadRoute("/settings/members");
		expect(router.state.location.pathname).toBe("/settings/members");
	});
});

describe("account and admin addresses", () => {
	test("/account opens the first account page", async () => {
		const router = await loadRoute("/account");
		expect(router.state.location.pathname).toBe("/account/profile");
	});

	test("an unknown account section opens the first page, a known one stays", async () => {
		expect((await loadRoute("/account/nope")).state.location.pathname).toBe(
			"/account/profile",
		);
		expect((await loadRoute("/account/tokens")).state.location.pathname).toBe(
			"/account/tokens",
		);
	});

	test("/admin opens the first admin page for an instance admin", async () => {
		const router = await loadRoute("/admin");
		expect(router.state.location.pathname).toBe("/admin/updates");
	});

	test("an unknown admin section opens the first page, a known one stays", async () => {
		expect((await loadRoute("/admin/nope")).state.location.pathname).toBe(
			"/admin/updates",
		);
		expect((await loadRoute("/admin/sign-in")).state.location.pathname).toBe(
			"/admin/sign-in",
		);
	});

	test("the git flow's parameters are kept on account pages and others are dropped", async () => {
		const router = await loadRoute("/account/profile?git_error=denied&x=1");
		expect(validatedSearch(router, "/account/$section")).toEqual({
			git_error: "denied",
		});
	});
});

describe("the admin area for people who are not instance admins", () => {
	test.each(["/admin", "/admin/updates", "/admin/sign-in"])(
		"%s is the not-found page, not a forbidden one",
		async (path) => {
			signedInApi({ "GET /me": { body: meOf(false) } });
			expect(isNotFoundPage(await loadRoute(path))).toBe(true);
		},
	);
});

function meOf(instanceAdmin: boolean) {
	return make("MeBody", {
		id: "user-2",
		name: "Bob Member",
		email: "bob@example.com",
		createdAt: "2026-01-01T00:00:00Z",
		instanceAdmin,
		workspaceId: "ws-1",
		principal: {
			type: "user",
			label: "Bob Member",
			scopes: [],
			role: "developer",
		},
	});
}

describe("server addresses", () => {
	test("/servers/<id>/settings opens the first settings page", async () => {
		const router = await loadRoute("/servers/srv-1/settings");
		expect(router.state.location.pathname).toBe(
			"/servers/srv-1/settings/general",
		);
	});

	test("an unknown server settings section opens the first page, a known one stays", async () => {
		expect(
			(await loadRoute("/servers/srv-1/settings/nope")).state.location.pathname,
		).toBe("/servers/srv-1/settings/general");
		expect(
			(await loadRoute("/servers/srv-1/settings/scheduling")).state.location
				.pathname,
		).toBe("/servers/srv-1/settings/scheduling");
	});

	test.each([
		["#traffic", "/servers/srv-1/traffic"],
		["#proxy", "/servers/srv-1/proxy"],
		["#security", "/servers/srv-1/security"],
		["#updates", "/servers/srv-1/updates"],
		["#settings", "/servers/srv-1/settings/general"],
		["#mesh", "/servers/srv-1/settings/mesh"],
		["#host", "/servers/srv-1"],
		["?tab=proxy", "/servers/srv-1/proxy"],
	])("the old address /servers/srv-1%s lands on %s", async (suffix, to) => {
		const router = await loadRoute(`/servers/srv-1${suffix}`);
		expect(router.state.location.pathname).toBe(to);
	});

	test("the old hash wins over ?tab=", async () => {
		const router = await loadRoute("/servers/srv-1?tab=proxy#security");
		expect(router.state.location.pathname).toBe("/servers/srv-1/security");
	});

	test("drain=1 travels along to the tab's new address", async () => {
		const router = await loadRoute("/servers/srv-1?tab=updates&drain=1");
		expect(router.state.location.pathname).toBe("/servers/srv-1/updates");
		expect(validatedSearch(router, "/servers/$serverId")).toEqual({
			drain: true,
		});
	});

	test("an address that is not an old one stays on the overview", async () => {
		const router = await loadRoute("/servers/srv-1#not-a-tab");
		expect(router.state.location.pathname).toBe("/servers/srv-1");
		expect(validatedSearch(router, "/servers/$serverId")).toEqual({});
	});

	test.each([
		["?drain=1", { drain: true }],
		["?drain=true", { drain: true }],
		["?drain=0", {}],
		["?drain=yes", {}],
	])("%s on a server page reads as %j", async (query, search) => {
		const router = await loadRoute(`/servers/srv-1/traffic${query}`);
		expect(validatedSearch(router, "/servers/$serverId")).toEqual(search);
	});
});

describe("server list search", () => {
	test.each([
		["?enroll=1", { enroll: true }],
		["?enroll=true", { enroll: true }],
		["?enroll=no", {}],
		["?view=fleet", { view: "fleet" }],
		["?view=grid", {}],
		["?enroll=1&view=fleet", { enroll: true, view: "fleet" }],
	])("/servers%s reads as %j", async (query, search) => {
		const router = await loadRoute(`/servers${query}`);
		expect(validatedSearch(router, "/servers/")).toEqual(search);
	});
});

describe("project environment addresses", () => {
	test("the old builds page is the deployments list filtered to builds", async () => {
		const router = await loadRoute("/p/proj-1/production/builds");
		expect(router.state.location.pathname).toBe(
			"/p/proj-1/production/deployments",
		);
		expect(router.state.location.search).toEqual({ type: "builds" });
	});

	test("the old services page is the canvas", async () => {
		const router = await loadRoute("/p/proj-1/production/services");
		expect(router.state.location.pathname).toBe("/p/proj-1/production");
	});

	test("an invalid canvas search value is dropped instead of failing the page", async () => {
		const router = await loadRoute(
			"/p/proj-1/production?service=svc-1&tab=logs&section=Bad_Section&debug=dep-1",
		);
		expect(validatedSearch(router, "/p/$projectId/$envSlug")).toEqual({
			service: "svc-1",
			tab: "logs",
			debug: "dep-1",
		});
	});

	test("a well-formed section is kept", async () => {
		const router = await loadRoute(
			"/p/proj-1/production?service=svc-1&tab=settings&section=build-location",
		);
		expect(validatedSearch(router, "/p/$projectId/$envSlug")).toMatchObject({
			tab: "settings",
			section: "build-location",
		});
	});

	test("deployments filters are read from the address; type only accepts builds", async () => {
		const kept = await loadRoute(
			"/p/proj-1/production/deployments?status=failed&service=svc-1&type=builds",
		);
		expect(
			validatedSearch(kept, "/p/$projectId/$envSlug_/deployments"),
		).toEqual({
			status: "failed",
			service: "svc-1",
			type: "builds",
		});
		const dropped = await loadRoute(
			"/p/proj-1/production/deployments?type=everything",
		);
		expect(
			validatedSearch(dropped, "/p/$projectId/$envSlug_/deployments"),
		).toEqual({});
	});

	test("observability keeps its search", async () => {
		const router = await loadRoute(
			"/p/proj-1/production/observability?q=error",
		);
		expect(
			validatedSearch(router, "/p/$projectId/$envSlug_/observability"),
		).toMatchObject({ q: "error" });
	});
});

describe("other routes", () => {
	test("reset-password reads the token from the address", async () => {
		const router = await loadRoute("/reset-password?token=abc");
		expect(validatedSearch(router, "/reset-password")).toEqual({
			token: "abc",
		});
	});

	test("login search keeps a valid step and drops an invalid one", async () => {
		// Signed in: /login moves on, so look at the search a signed-out visitor keeps.
		stubApi({
			"GET /setup": {
				body: make("SetupStatusOutputBody", {
					needsSetup: false,
					tokenRequired: false,
				}),
			},
			"GET /me": problem(401, { code: "unauthorized" }),
		});
		const router = await loadRoute(
			"/login?step=2fa&returnTo=%2Fservers&notice=bogus",
		);
		expect(validatedSearch(router, "/login")).toEqual({
			step: "2fa",
			returnTo: "/servers",
		});
		expect(hrefOf(router)).toContain("step=2fa");
	});

	test("setup reads the one-time token from the address", async () => {
		stubApi({
			"GET /setup": {
				body: make("SetupStatusOutputBody", {
					needsSetup: true,
					tokenRequired: true,
				}),
			},
		});
		const router = await loadRoute("/setup?token=s3cret");
		expect(validatedSearch(router, "/setup")).toEqual({ token: "s3cret" });
	});

	test("the dither gallery is only there in a dev build", async () => {
		const router = await loadRoute("/dev/dither");
		expect(isNotFoundPage(router)).toBe(!import.meta.env.DEV);
	});
});
