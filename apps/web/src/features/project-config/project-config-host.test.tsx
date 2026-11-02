import { describe, expect, test } from "bun:test";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { openProjectConfig } from "./open";
import { ProjectConfigHost } from "./project-config-host";
import { connectionsReply, environment, envSource } from "./test-support";

function routes() {
	return {
		"GET /environments/:id/config-source": { body: envSource() },
		"GET /git/connections": connectionsReply(),
		"GET /projects/:id/config/export": {
			body: make("ConfigExportResult", { file: "[project]\n", warnings: [] }),
		},
		"POST /projects/:id/config/plan": {
			body: make("ConfigPlan", {
				planId: "pl_1",
				valid: true,
				source: { kind: "git" },
				summary: { unchanged: 1 },
				services: [],
			}),
		},
	};
}

describe("ProjectConfigHost", () => {
	test("mounts every dialog closed and asks for nothing", async () => {
		const api = stubApi({});
		await renderWithProviders(
			<ProjectConfigHost environment={environment()} />,
		);
		expect(screen.queryByRole("dialog")).toBeNull();
		expect(api.calls).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});

	test.each([
		["review", "Review nebula.toml"],
		["export", "Export nebula.toml"],
		["source", "Config from git"],
	] as const)(
		"openProjectConfig(%p) opens the %p dialog",
		async (which, title) => {
			stubApi(routes());
			await renderWithProviders(
				<ProjectConfigHost environment={environment()} />,
			);
			act(() => openProjectConfig(which));
			const dialog = await screen.findByRole("dialog");
			expect(within(dialog).getByText(title)).toBeInTheDocument();
			expect(screen.getAllByRole("dialog")).toHaveLength(1);
		},
	);

	test("closing the open dialog leaves none open", async () => {
		stubApi(routes());
		await renderWithProviders(
			<ProjectConfigHost environment={environment()} />,
		);
		act(() => openProjectConfig("export"));
		const dialog = await screen.findByRole("dialog");
		await userEvent.setup().click(
			within(dialog).getAllByRole("button", {
				name: "Close",
			})[0] as HTMLElement,
		);
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});

	test("opening another dialog replaces the first", async () => {
		stubApi(routes());
		await renderWithProviders(
			<ProjectConfigHost environment={environment()} />,
		);
		act(() => openProjectConfig("export"));
		await screen.findByText("Export nebula.toml");
		act(() => openProjectConfig("source"));
		expect(await screen.findByText("Config from git")).toBeInTheDocument();
		await waitFor(() =>
			expect(screen.queryByText("Export nebula.toml")).toBeNull(),
		);
	});

	test("a preview environment has no dialogs, whatever asks for one", async () => {
		const api = stubApi({});
		await renderWithProviders(
			<ProjectConfigHost environment={environment({ kind: "preview" })} />,
		);
		act(() => openProjectConfig("review"));
		expect(screen.queryByRole("dialog")).toBeNull();
		expect(api.calls).toEqual([]);
	});

	test("stops listening once unmounted", async () => {
		const api = stubApi({});
		const view = await renderWithProviders(
			<ProjectConfigHost environment={environment()} />,
		);
		view.unmount();
		act(() => openProjectConfig("export"));
		expect(screen.queryByRole("dialog")).toBeNull();
		expect(api.calls).toEqual([]);
	});
});
