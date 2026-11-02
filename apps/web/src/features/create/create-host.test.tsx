import { afterEach, describe, expect, test } from "bun:test";
import { act, screen, waitFor } from "@testing-library/react";
import { composeImportActions } from "#/features/compose-import/store";
import { coolifyImportActions } from "#/features/coolify-import/store";
import { ENGINE_CATALOG } from "#/features/databases/engines.fixture";
import { createStackActions } from "#/features/stacks/store";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { CreateHost } from "./create-host";
import { serverBody } from "./fixtures";
import { createActions } from "./store";

const ENVIRONMENT = {
	id: "env-1",
	projectId: "proj-1",
	name: "Staging",
	slug: "staging",
	isProduction: false,
	kind: "standard",
	createdAt: "2026-01-01T00:00:00Z",
	parentEnvironmentId: null,
	prNumber: null,
	prTitle: "",
};

afterEach(() => {
	act(() => {
		createActions.closeDialog();
		createStackActions.close();
		composeImportActions.close();
		coolifyImportActions.close();
	});
});

function stubScreen() {
	return stubApi({
		"GET /projects": { body: { items: [] } },
		"GET /projects/:projectId/environments": {
			body: { items: [ENVIRONMENT] },
		},
		"GET /servers": { body: { items: [serverBody()] } },
		"GET /database-engines": { body: { engines: ENGINE_CATALOG } },
	});
}

const SCREEN = {
	path: "/p/proj-1/staging",
	routePath: "/p/$projectId/$envSlug",
};

describe("CreateHost", () => {
	test("renders nothing outside an environment, so no dialog can open into the void", async () => {
		stubScreen();
		const { container } = await renderWithProviders(<CreateHost />);
		act(() => createActions.open("image"));
		expect(screen.queryByRole("dialog")).toBeNull();
		expect(container).toBeEmptyDOMElement();
	});

	test("shows nothing until something asks for a dialog", async () => {
		stubScreen();
		await renderWithProviders(<CreateHost />, SCREEN);
		expect(screen.queryByRole("dialog")).toBeNull();
	});

	test("an image request opens the service dialog on the servers of the account", async () => {
		const api = stubScreen();
		await renderWithProviders(<CreateHost />, SCREEN);
		act(() => createActions.open("image"));
		expect(
			await screen.findByText("New service from a Docker image"),
		).toBeVisible();
		expect(await screen.findByRole("radio", { name: /alpha/ })).toBeVisible();
		expect(screen.queryByText("New database")).toBeNull();
		expect(api.calls.some((call) => call.path === "/servers")).toBe(true);
	});

	test("a Git request opens the same dialog on its Git form", async () => {
		stubApi({
			"GET /projects": { body: { items: [] } },
			"GET /projects/:projectId/environments": {
				body: { items: [ENVIRONMENT] },
			},
			"GET /servers": { body: { items: [serverBody()] } },
			"GET /git/connections": { body: { items: [] } },
		});
		await renderWithProviders(<CreateHost />, SCREEN);
		act(() => createActions.open("git"));
		expect(
			await screen.findByText("New service from a Git repository"),
		).toBeVisible();
		expect(await screen.findByLabelText("Repository URL")).toBeVisible();
	});

	test("a database request opens the database dialog, preselecting the engine, not the service one", async () => {
		stubScreen();
		await renderWithProviders(<CreateHost />, SCREEN);
		act(() => createActions.open("database", { engine: "mariadb" }));
		expect(await screen.findByText("New database")).toBeVisible();
		expect(screen.queryByText("New service from a Docker image")).toBeNull();
		expect(screen.queryByText("New service from a Git repository")).toBeNull();
	});

	test("closing the dialog through the store hides it", async () => {
		stubScreen();
		await renderWithProviders(<CreateHost />, SCREEN);
		act(() => createActions.open("image"));
		await screen.findByRole("dialog");
		act(() => createActions.closeDialog());
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});

	test("the Compose stack flow opens its own dialog", async () => {
		stubScreen();
		await renderWithProviders(<CreateHost />, SCREEN);
		act(() => createStackActions.open({ x: 1, y: 2 }));
		expect(await screen.findByText("New Compose stack")).toBeVisible();
		expect(screen.queryByText("New service from a Docker image")).toBeNull();
	});

	test("the Compose import flow is mounted", async () => {
		stubScreen();
		await renderWithProviders(<CreateHost />, SCREEN);
		act(() => composeImportActions.open());
		expect(await screen.findByText("Import Docker Compose")).toBeVisible();
	});

	test("the Coolify import flow is mounted", async () => {
		stubApi({
			"GET /projects": { body: { items: [] } },
			"GET /projects/:projectId/environments": {
				body: { items: [ENVIRONMENT] },
			},
			"GET /servers": { body: { items: [serverBody()] } },
		});
		await renderWithProviders(<CreateHost />, SCREEN);
		act(() => coolifyImportActions.open());
		expect(await screen.findByText("Import from Coolify")).toBeVisible();
	});
});
