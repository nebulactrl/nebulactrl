import { afterAll, beforeAll, describe, expect, mock, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ProjectSwitcher } from "./project-switcher";
import { ShellActionsProvider } from "./shell-actions";
import { renderAt, shellApi, where } from "./shell-test-kit";
import { useProjectContext } from "./use-project-context";

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

function Host() {
	const context = useProjectContext();
	return (
		<ShellActionsProvider>
			{context ? <ProjectSwitcher context={context} /> : null}
		</ShellActionsProvider>
	);
}

async function open(path: string) {
	const user = userEvent.setup();
	const view = await renderAt(<Host />, { path });
	const trigger = await screen.findByRole("button", {
		name: "Project: Shop. Switch project",
	});
	await user.click(trigger);
	return { user, trigger, ...view };
}

describe("ProjectSwitcher", () => {
	test("the trigger names the current project and shows its initial", async () => {
		shellApi();
		await renderAt(<Host />, { path: "/p/p1/staging" });
		const trigger = await screen.findByRole("button", {
			name: "Project: Shop. Switch project",
		});
		expect(trigger).toHaveTextContent("Shop");
		expect(trigger).toHaveTextContent("S");
	});

	test("before the project is known it says only Switch project", async () => {
		shellApi({ "GET /projects": () => new Promise<Response>(() => {}) });
		await renderAt(<Host />, { path: "/p/p1/staging" });
		const trigger = await screen.findByRole("button", {
			name: "Switch project",
		});
		expect(trigger).toHaveTextContent("…");
	});

	test("opens a menu of every project, marking the current one", async () => {
		shellApi();
		await open("/p/p1/staging");
		const current = await screen.findByRole("menuitem", { name: /Shop/ });
		const other = screen.getByRole("menuitem", { name: /Blog/ });
		expect(current).toHaveAttribute("aria-current", "true");
		expect(other).not.toHaveAttribute("aria-current");
		// The other project shows its slug.
		expect(other).toHaveTextContent("blog");
		expect(
			screen.getByRole("menuitem", { name: "All projects" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("menuitem", { name: "New project" }),
		).toBeInTheDocument();
	});

	test("choosing another project opens its production environment", async () => {
		shellApi();
		const { user, router } = await open("/p/p1/staging");
		await user.click(await screen.findByRole("menuitem", { name: /Blog/ }));
		await waitFor(() => expect(where(router)).toBe("/p/p2/production"));
	});

	test("switching keeps the page being read", async () => {
		shellApi();
		const { user, router } = await open("/p/p1/staging/deployments");
		await user.click(await screen.findByRole("menuitem", { name: /Blog/ }));
		await waitFor(() =>
			expect(where(router)).toBe("/p/p2/production/deployments"),
		);
	});

	test("choosing the current project goes nowhere", async () => {
		const api = shellApi();
		const { user, router } = await open("/p/p1/staging/variables");
		await user.click(await screen.findByRole("menuitem", { name: /Shop/ }));
		expect(where(router)).toBe("/p/p1/staging/variables");
		expect(api.unmatched).toEqual([]);
	});

	test("All projects goes home", async () => {
		shellApi();
		const { user, router } = await open("/p/p1/staging");
		await user.click(
			await screen.findByRole("menuitem", { name: "All projects" }),
		);
		await waitFor(() => expect(where(router)).toBe("/"));
	});

	test("New project opens the dialog", async () => {
		shellApi();
		const { user } = await open("/p/p1/staging");
		await user.click(
			await screen.findByRole("menuitem", { name: "New project" }),
		);
		expect(
			await screen.findByRole("dialog", { name: /new project/i }),
		).toBeInTheDocument();
	});

	test("Escape closes the menu without going anywhere", async () => {
		shellApi();
		const { user, router } = await open("/p/p1/staging");
		await screen.findByRole("menu");
		await user.keyboard("{Escape}");
		await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
		expect(where(router)).toBe("/p/p1/staging");
	});

	test("arrow keys and Enter choose a project from the keyboard", async () => {
		shellApi();
		const { user, router } = await open("/p/p1/staging");
		await screen.findByRole("menu");
		await user.keyboard("{ArrowDown}{ArrowDown}{Enter}");
		await waitFor(() => expect(where(router)).toBe("/p/p2/production"));
	});
});
