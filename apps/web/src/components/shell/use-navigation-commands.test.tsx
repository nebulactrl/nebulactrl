import { afterAll, beforeAll, describe, expect, mock, test } from "bun:test";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { make, problem } from "#/test/factories";
import { useCommandRegistry } from "./palette-registry";
import { ShellActionsProvider } from "./shell-actions";
import { ADMIN, renderAt, shellApi, where } from "./shell-test-kit";
import { useNavigationCommands } from "./use-navigation-commands";

beforeAll(() => {
	mock.module("virtual:docs-search", () => ({
		default: { pages: [], rows: [] },
	}));
});
afterAll(() => {
	mock.module("virtual:docs-search", () => ({
		default: { pages: [], rows: [] },
	}));
});

function Registrar() {
	useNavigationCommands();
	return null;
}

function Commands() {
	const commands = useCommandRegistry();
	return (
		<output aria-label="ids">
			{commands
				.filter((c) => !c.id.startsWith("settings:"))
				.map((c) => c.id)
				.join(",")}
		</output>
	);
}

function Host() {
	return (
		<ShellActionsProvider>
			<Registrar />
			<Commands />
		</ShellActionsProvider>
	);
}

const ids = () =>
	(screen.getByLabelText("ids").textContent ?? "").split(",").filter(Boolean);

async function openPalette() {
	fireEvent.keyDown(window, { key: "k", ctrlKey: true });
	return screen.findByRole("combobox");
}

async function run(query: string) {
	const input = await openPalette();
	fireEvent.change(input, { target: { value: query } });
	fireEvent.keyDown(input, { key: "Enter" });
}

describe("navigation commands outside a project", () => {
	test("registers the team-level entries and one per project", async () => {
		shellApi();
		await renderAt(<Host />, { path: "/servers" });
		await waitFor(() => expect(ids()).toContain("project:p2"));
		expect(ids()).toEqual([
			"go:projects",
			"go:servers",
			"action:add-server",
			"go:status-pages",
			"action:new-project",
			"action:copy-mcp",
			"action:sign-out",
			"project:p1",
			"project:p2",
		]);
	});

	test("Open servers goes to /servers", async () => {
		shellApi();
		const { router } = await renderAt(<Host />, { path: "/" });
		await run("Open servers");
		await waitFor(() => expect(where(router)).toBe("/servers"));
		expect(
			screen.queryByRole("dialog", { name: "Command palette" }),
		).toBeNull();
	});

	test("Add server opens the servers page on the enrollment form", async () => {
		shellApi();
		const { router } = await renderAt(<Host />, { path: "/" });
		await run("Add server");
		await waitFor(() => expect(where(router)).toBe("/servers?enroll=true"));
	});

	test("Open status pages and Open projects navigate", async () => {
		shellApi();
		const { router } = await renderAt(<Host />, { path: "/servers" });
		await run("Open status pages");
		await waitFor(() => expect(where(router)).toBe("/status-pages"));
		await run("Open projects");
		await waitFor(() => expect(where(router)).toBe("/"));
	});

	test("choosing a project opens its production environment", async () => {
		shellApi();
		const { router } = await renderAt(<Host />, { path: "/servers" });
		await waitFor(() => expect(ids()).toContain("project:p2"));
		await run("Blog");
		await waitFor(() => expect(where(router)).toBe("/p/p2/production"));
	});

	test("New project opens the new project dialog", async () => {
		shellApi();
		await renderAt(<Host />, { path: "/servers" });
		await run("New project");
		expect(
			await screen.findByRole("dialog", { name: /new project/i }),
		).toBeInTheDocument();
	});

	test("Sign out posts to /auth/logout and lands on /login", async () => {
		const api = shellApi({ "POST /auth/logout": { status: 204 } });
		const { router } = await renderAt(<Host />, { path: "/servers" });
		await run("Sign out");
		await waitFor(() => expect(where(router)).toBe("/login"));
		expect(
			api.calls.filter((c) => c.method === "POST").map((c) => c.path),
		).toEqual(["/auth/logout"]);
	});

	test("Copy MCP endpoint says MCP is off when it is", async () => {
		shellApi();
		await renderAt(<Host />, { path: "/servers" });
		await run("Copy MCP endpoint");
		expect(await screen.findByText("MCP is not enabled")).toBeInTheDocument();
		expect(
			screen.getByText("The instance admin turns it on in Admin → MCP."),
		).toBeInTheDocument();
	});

	test("Copy MCP endpoint copies the URL and says so when MCP is on", async () => {
		shellApi({
			"GET /system": {
				body: make("SystemInfo", {
					version: "v1",
					mcpEnabled: true,
					mcpUrl: "https://nebula.test/mcp",
				}),
			},
		});
		const written: string[] = [];
		Object.defineProperty(navigator, "clipboard", {
			configurable: true,
			value: { writeText: async (text: string) => void written.push(text) },
		});
		await renderAt(<Host />, { path: "/servers" });
		await waitFor(() => expect(ids()).toContain("action:copy-mcp"));
		// The endpoint is the row's hint once /system has answered.
		const input = await openPalette();
		await waitFor(() =>
			expect(screen.getByText("https://nebula.test/mcp")).toBeInTheDocument(),
		);
		fireEvent.change(input, { target: { value: "Copy MCP" } });
		fireEvent.keyDown(input, { key: "Enter" });
		expect(await screen.findByText("Copied")).toBeInTheDocument();
		expect(written).toEqual(["https://nebula.test/mcp"]);
	});

	test("a failed sign-out keeps the session and says so", async () => {
		shellApi({
			"POST /auth/logout": problem(500, { title: "boom" }),
		});
		const { router } = await renderAt(<Host />, { path: "/servers" });
		await run("Sign out");
		expect(await screen.findByText("Sign-out failed")).toBeInTheDocument();
		expect(where(router)).toBe("/servers");
	});
});

describe("navigation commands inside an environment", () => {
	test("adds the environment's pages, leaves out its own project and switches to the others", async () => {
		shellApi();
		await renderAt(<Host />, { path: "/p/p1/staging" });
		await waitFor(() => expect(ids()).toContain("env:p1-production"));
		const list = ids();
		expect(list).toEqual(
			expect.arrayContaining([
				"go:canvas",
				"go:deployments",
				"go:previews",
				"go:observability",
				"go:logs",
				"project:p2",
				"env:p1-production",
			]),
		);
		expect(list).not.toContain("project:p1");
		expect(list).not.toContain("env:p1-staging");
	});

	test.each([
		["Open canvas", "/p/p1/staging"],
		["Open deployments", "/p/p1/staging/deployments"],
		["Open previews", "/p/p1/staging/previews"],
		["Open observability", "/p/p1/staging/observability"],
		["Open logs", "/p/p1/staging/logs"],
	])("%s goes to %s", async (label, expected) => {
		shellApi();
		const { router } = await renderAt(<Host />, {
			path: "/p/p1/staging/variables",
		});
		await waitFor(() => expect(ids()).toContain("go:canvas"));
		await run(label);
		await waitFor(() => expect(where(router)).toBe(expected));
	});

	test("Switch to <environment> opens that environment's canvas", async () => {
		shellApi();
		const { router } = await renderAt(<Host />, {
			path: "/p/p1/staging/variables",
		});
		await waitFor(() => expect(ids()).toContain("env:p1-production"));
		await run("Switch to production");
		await waitFor(() => expect(where(router)).toBe("/p/p1/production"));
	});

	test("the environment rows are labelled by what they are", async () => {
		shellApi();
		await renderAt(<Host />, { path: "/p/p1/staging" });
		await waitFor(() => expect(ids()).toContain("env:p1-production"));
		const input = await openPalette();
		fireEvent.change(input, { target: { value: "Switch to" } });
		const row = await screen.findByRole("option", {
			name: /Switch to production/,
		});
		expect(within(row).getByText("environment")).toBeInTheDocument();
		expect(within(row).getByText("Go to")).toBeInTheDocument();
	});

	test("a project's own settings page has project-level entries only", async () => {
		shellApi();
		await renderAt(<Host />, { path: "/p/p1/settings" });
		await waitFor(() => expect(ids()).toContain("env:p1-production"));
		expect(ids()).not.toContain("go:canvas");
		expect(ids()).toContain("env:p1-staging");
	});

	test("a preview environment is hinted by its name", async () => {
		shellApi({
			"GET /projects/:id/environments": {
				body: make("PageEnvironment", {
					items: [
						make("Environment", {
							id: "e1",
							projectId: "p1",
							slug: "production",
							name: "Production",
							kind: "standard",
							isProduction: true,
						}),
						make("Environment", {
							id: "e2",
							projectId: "p1",
							slug: "pr-7",
							name: "PR #7",
							kind: "preview",
							isProduction: false,
						}),
					],
				}),
			},
		});
		await renderAt(<Host />, { path: "/p/p1/production" });
		await waitFor(() => expect(ids()).toContain("env:e2"));
		const input = await openPalette();
		fireEvent.change(input, { target: { value: "Switch to" } });
		const row = await screen.findByRole("option", { name: /Switch to pr-7/ });
		expect(within(row).getByText("PR #7")).toBeInTheDocument();
	});
});

describe("settings commands registered alongside", () => {
	test("an instance admin finds Admin pages by typing, never in the empty prompt", async () => {
		shellApi({ "GET /me": { body: ADMIN } });
		await renderAt(<Host />, { path: "/servers" });
		await waitFor(() => expect(ids()).toContain("project:p2"));
		const input = await openPalette();
		await waitFor(() =>
			expect(screen.getAllByRole("option").length).toBeGreaterThan(0),
		);
		expect(screen.queryByText(/Admin: /)).toBeNull();
		fireEvent.change(input, { target: { value: "smtp" } });
		expect(
			(await screen.findAllByRole("option")).some((o) =>
				/Admin: /.test(o.textContent ?? ""),
			),
		).toBe(true);
	});
});
