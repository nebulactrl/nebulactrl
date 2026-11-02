import { afterEach, describe, expect, test } from "bun:test";
import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { templatesActions } from "./store";
import { TemplatesHost } from "./templates-host";
import {
	contextRoutes,
	ENV_PATH,
	ENV_ROUTE,
	instance,
	resetTemplatesStore,
	summary,
	template,
	update,
} from "./test-fixtures";

afterEach(() => {
	act(() => {
		resetTemplatesStore();
	});
});

const routes = () => ({
	...contextRoutes(),
	"GET /servers": { body: { items: [] } },
	"GET /catalog/templates": {
		body: {
			categories: [{ id: "analytics", label: "Analytics", count: 1 }],
			items: [
				summary({ id: "umami", name: "Umami", summary: "Web analytics" }),
			],
		},
	},
	"GET /catalog/templates/:templateId": {
		body: template({ id: "umami", name: "Umami" }),
	},
	"GET /template-instances/:instanceId/update": { body: update() },
});

const render = () =>
	renderWithProviders(<TemplatesHost />, {
		path: ENV_PATH,
		routePath: ENV_ROUTE,
	});

describe("TemplatesHost", () => {
	test("shows nothing while closed", async () => {
		stubApi(routes());
		await render();
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
	});

	test("opening shows the gallery, and picking a card moves to that template's plan step", async () => {
		const user = userEvent.setup();
		const api = stubApi(routes());
		await render();
		act(() => templatesActions.open());
		expect(await screen.findByText("New from template")).toBeInTheDocument();
		await user.click(
			await screen.findByRole("button", { name: "Umami: Web analytics" }),
		);
		expect(
			await screen.findByRole("button", { name: "Back to all templates" }),
		).toBeInTheDocument();
		expect(screen.queryByText("New from template")).not.toBeInTheDocument();
		expect(api.calls.some((c) => c.path === "/catalog/templates/umami")).toBe(
			true,
		);
	});

	test("opening with an id goes straight to that template", async () => {
		stubApi(routes());
		await render();
		act(() => templatesActions.open("umami"));
		expect(
			await screen.findByRole("button", { name: "Back to all templates" }),
		).toBeInTheDocument();
	});

	test("Escape on the plan step goes back to the gallery first, and a second Escape closes", async () => {
		const user = userEvent.setup();
		stubApi(routes());
		await render();
		act(() => templatesActions.open("umami"));
		await screen.findByRole("button", { name: "Back to all templates" });
		await user.keyboard("{Escape}");
		expect(await screen.findByText("New from template")).toBeInTheDocument();
		await user.keyboard("{Escape}");
		await waitFor(() =>
			expect(screen.queryByText("New from template")).not.toBeInTheDocument(),
		);
	});

	test("renders nothing outside an environment", async () => {
		stubApi({ ...routes() });
		const { container } = await renderWithProviders(<TemplatesHost />, {
			path: "/",
		});
		act(() => templatesActions.open());
		expect(screen.queryByText("New from template")).not.toBeInTheDocument();
		expect(container.querySelector("[role=dialog]")).toBeNull();
	});

	test("a requested update opens the update dialog, independent of the gallery", async () => {
		stubApi(routes());
		await render();
		act(() =>
			templatesActions.requestUpdate(instance({ id: "i1", name: "Umami" })),
		);
		expect(await screen.findByText("Update Umami")).toBeInTheDocument();
		expect(screen.queryByText("New from template")).not.toBeInTheDocument();
	});

	test("a requested delete opens the delete confirmation", async () => {
		stubApi(routes());
		await render();
		act(() =>
			templatesActions.requestDelete(instance({ id: "i1", name: "Umami" })),
		);
		expect(
			await screen.findByText("Delete the Umami stack?"),
		).toBeInTheDocument();
	});
});
