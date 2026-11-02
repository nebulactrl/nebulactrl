import { afterEach, describe, expect, test } from "bun:test";
import {
	act,
	renderHook,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import type { Environment } from "#/api/gen/types.gen";
import { ENGINE_CATALOG } from "#/features/databases/engines.fixture";
import {
	routeCreate,
	useRouteCreateRequest,
} from "#/features/proxy/create-store";
import { createStackActions } from "#/features/stacks/store";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { CreateButton } from "./create-menu";
import { createActions, useCreateState } from "./store";

const ENVIRONMENT: Environment = {
	id: "env-1",
	projectId: "proj-1",
	name: "Production",
	slug: "prod",
	isProduction: true,
	kind: "standard",
	createdAt: "2026-01-01T00:00:00Z",
	parentEnvironmentId: null,
	prNumber: null,
	prTitle: "",
};

afterEach(() => {
	act(() => {
		createActions.closeDialog();
		createActions.setMenuOpen(false);
		routeCreate.close();
		createStackActions.close();
	});
});

function stubCatalog(engines: unknown = { engines: ENGINE_CATALOG }) {
	return stubApi({
		"GET /projects": { body: { items: [] } },
		"GET /projects/:projectId/environments": {
			body: { items: [ENVIRONMENT] },
		},
		"GET /database-engines":
			engines === "fail"
				? {
						status: 500,
						body: { title: "boom", status: 500, code: "internal" },
					}
				: { body: engines },
	});
}

/** The Create button inside the route a canvas screen lives under. */
function renderButton() {
	return renderWithProviders(
		<>
			<CreateButton />
			<StateProbe />
		</>,
		{ path: "/p/proj-1/prod", routePath: "/p/$projectId/$envSlug" },
	);
}

/** Shows the store so a test can read what a pick asked for. */
function StateProbe() {
	const { request, menu } = useCreateState();
	return (
		<output aria-label="create state">
			{JSON.stringify({ request, menu: menu !== null })}
		</output>
	);
}

function storeState(): { request: unknown; menu: boolean } {
	return JSON.parse(screen.getByLabelText("create state").textContent ?? "");
}

async function openMenu(user: UserEvent) {
	const button = await screen.findByRole("button", { name: "Create" });
	await waitFor(() => expect(button).toBeEnabled());
	await user.click(button);
	return screen.findByRole("listbox", { name: "Create a service" });
}

const labels = (list: HTMLElement) =>
	within(list)
		.getAllByRole("option")
		.map((option) => option.querySelector("span.truncate")?.textContent);

describe("CreateButton", () => {
	test("is disabled outside an environment: there is nowhere to create", async () => {
		stubCatalog();
		await renderWithProviders(<CreateButton />);
		expect(screen.getByRole("button", { name: "Create" })).toBeDisabled();
	});

	test("enables once the environment on screen is known", async () => {
		stubCatalog();
		await renderButton();
		const button = screen.getByRole("button", { name: "Create" });
		await waitFor(() => expect(button).toBeEnabled());
	});

	test("opens the menu listing every way to start, in order, with Database standing for its engines", async () => {
		stubCatalog();
		const user = userEvent.setup();
		await renderButton();
		const list = await openMenu(user);
		expect(labels(list)).toEqual([
			"Git repository",
			"Docker image",
			"Database",
			"Templates",
			"Import Docker Compose",
			"Import from Coolify",
			"Compose stack",
			"External service",
			"TCP/UDP port",
		]);
		expect(
			screen.getByRole("combobox", { name: "Filter sources" }),
		).toHaveAttribute("placeholder", "What are you creating?");
		// The first row is the active one.
		expect(within(list).getAllByRole("option")[0]).toHaveAttribute(
			"aria-selected",
			"true",
		);
	});

	test("clicking Docker image opens that dialog and closes the menu", async () => {
		stubCatalog();
		const user = userEvent.setup();
		await renderButton();
		const list = await openMenu(user);
		await user.click(
			within(list).getByRole("option", { name: /Docker image/ }),
		);
		expect(storeState()).toEqual({ request: { kind: "image" }, menu: false });
		await waitFor(() =>
			expect(
				screen.queryByRole("listbox", { name: "Create a service" }),
			).toBeNull(),
		);
	});

	test("the menu opened from a context menu hands its drop position to the dialog", async () => {
		stubCatalog();
		const user = userEvent.setup();
		await renderButton();
		const button = await screen.findByRole("button", { name: "Create" });
		await waitFor(() => expect(button).toBeEnabled());
		act(() => createActions.open(undefined, { position: { x: 120, y: 80 } }));
		const list = await screen.findByRole("listbox", {
			name: "Create a service",
		});
		await user.click(
			within(list).getByRole("option", { name: /Git repository/ }),
		);
		expect(storeState().request).toEqual({
			kind: "git",
			position: { x: 120, y: 80 },
		});
	});

	test("with a Create button mounted, open() shows the menu instead of the image dialog", async () => {
		stubCatalog();
		await renderButton();
		act(() => createActions.open());
		expect(
			await screen.findByRole("listbox", { name: "Create a service" }),
		).toBeVisible();
		expect(storeState().request).toBeNull();
	});

	test("after the button unmounts, open() goes straight to the image dialog", async () => {
		stubCatalog();
		const view = await renderWithProviders(
			<>
				<CreateButton />
				<StateProbe />
			</>,
			{ path: "/p/proj-1/prod", routePath: "/p/$projectId/$envSlug" },
		);
		view.unmount();
		const probe = await renderWithProviders(<StateProbe />);
		act(() => createActions.open());
		expect(storeState()).toEqual({ request: { kind: "image" }, menu: false });
		probe.unmount();
	});

	test("Escape closes the menu", async () => {
		stubCatalog();
		const user = userEvent.setup();
		await renderButton();
		await openMenu(user);
		await user.keyboard("{Escape}");
		await waitFor(() => expect(storeState().menu).toBe(false));
	});

	describe("filtering", () => {
		test("a query searches every entry, engines included: 'post' reaches PostgreSQL without opening Database", async () => {
			stubCatalog();
			const user = userEvent.setup();
			await renderButton();
			const list = await openMenu(user);
			await user.type(
				screen.getByRole("combobox", { name: "Filter sources" }),
				"post",
			);
			await waitFor(() => expect(labels(list)).toEqual(["PostgreSQL"]));
			await user.keyboard("{Enter}");
			expect(storeState().request).toEqual({
				kind: "database",
				engine: "postgres",
			});
		});

		test("a query without a match says so, and Enter does nothing", async () => {
			stubCatalog();
			const user = userEvent.setup();
			await renderButton();
			await openMenu(user);
			await user.type(
				screen.getByRole("combobox", { name: "Filter sources" }),
				"  zzzz ",
			);
			expect(await screen.findByText("Nothing matches “zzzz”.")).toBeVisible();
			await user.keyboard("{Enter}");
			expect(storeState().request).toBeNull();
			expect(screen.queryAllByRole("option")).toEqual([]);
		});

		test("searching before the catalog arrived says the engines are loading", async () => {
			const gate = Promise.withResolvers<void>();
			stubApi({
				"GET /projects": { body: { items: [] } },
				"GET /projects/:projectId/environments": {
					body: { items: [ENVIRONMENT] },
				},
				"GET /database-engines": async () => {
					await gate.promise;
					return Response.json({ engines: ENGINE_CATALOG });
				},
			});
			const user = userEvent.setup();
			await renderButton();
			await openMenu(user);
			await user.type(
				screen.getByRole("combobox", { name: "Filter sources" }),
				"valkey",
			);
			expect(await screen.findByText("Loading engines…")).toBeVisible();
			gate.resolve();
			expect(
				await screen.findByRole("option", { name: /Valkey/ }),
			).toBeVisible();
			expect(screen.queryByText("Loading engines…")).toBeNull();
		});

		test("an engine under a licence shows a chip so the choice is informed", async () => {
			stubCatalog();
			const user = userEvent.setup();
			await renderButton();
			await openMenu(user);
			await user.type(
				screen.getByRole("combobox", { name: "Filter sources" }),
				"redis",
			);
			const option = await screen.findByRole("option", { name: /Redis/ });
			expect(within(option).getByText("licence")).toBeVisible();
		});
	});

	describe("Database submenu", () => {
		test("clicking Database lists the engines, Valkey (the default key-value store) first", async () => {
			stubCatalog();
			const user = userEvent.setup();
			await renderButton();
			const list = await openMenu(user);
			await user.click(within(list).getByRole("option", { name: /Database/ }));
			const filter = screen.getByRole("combobox", {
				name: "Filter databases",
			});
			expect(filter).toHaveAttribute("placeholder", "Database…");
			await waitFor(() => expect(labels(list)[0]).toBe("Valkey"));
			expect(labels(list)).toEqual(
				expect.arrayContaining(["PostgreSQL", "MySQL", "MariaDB", "MongoDB"]),
			);
			expect(labels(list)).not.toContain("Git repository");
			expect(
				within(list).getByRole("option", { name: /Valkey/ }),
			).toHaveTextContent("default");
		});

		test("picking an engine opens the database dialog on it and closes the menu", async () => {
			stubCatalog();
			const user = userEvent.setup();
			await renderButton();
			const list = await openMenu(user);
			await user.click(within(list).getByRole("option", { name: /Database/ }));
			await user.click(
				await within(list).findByRole("option", { name: /MariaDB/ }),
			);
			expect(storeState()).toEqual({
				request: { kind: "database", engine: "mariadb" },
				menu: false,
			});
		});

		test("the back button returns to all sources", async () => {
			stubCatalog();
			const user = userEvent.setup();
			await renderButton();
			const list = await openMenu(user);
			await user.click(within(list).getByRole("option", { name: /Database/ }));
			await user.click(
				screen.getByRole("button", { name: "Back to all sources" }),
			);
			expect(labels(list)[0]).toBe("Git repository");
			expect(
				screen.getByRole("combobox", { name: "Filter sources" }),
			).toHaveValue("");
		});

		test("a catalog that failed to load says so inside Database", async () => {
			stubCatalog("fail");
			const user = userEvent.setup();
			await renderButton();
			const list = await openMenu(user);
			await user.click(within(list).getByRole("option", { name: /Database/ }));
			expect(
				await screen.findByText("The engine list did not load."),
			).toBeVisible();
		});
	});

	describe("keyboard", () => {
		test("arrows move the active row and wrap at both ends; Enter picks it", async () => {
			stubCatalog();
			const user = userEvent.setup();
			await renderButton();
			const list = await openMenu(user);
			const active = () =>
				within(list)
					.getAllByRole("option")
					.findIndex((o) => o.getAttribute("aria-selected") === "true");
			const combobox = screen.getByRole("combobox", { name: "Filter sources" });
			await user.click(combobox);
			await user.keyboard("{ArrowDown}");
			expect(active()).toBe(1);
			expect(combobox).toHaveAttribute(
				"aria-activedescendant",
				within(list).getAllByRole("option")[1]?.id,
			);
			await user.keyboard("{ArrowUp}{ArrowUp}");
			expect(active()).toBe(8);
			await user.keyboard("{ArrowDown}");
			expect(active()).toBe(0);
			await user.keyboard("{ArrowDown}{Enter}");
			expect(storeState().request).toEqual({ kind: "image" });
		});

		test("ArrowRight on Database opens it; ArrowLeft and Backspace on an empty field go back", async () => {
			stubCatalog();
			const user = userEvent.setup();
			await renderButton();
			const list = await openMenu(user);
			await user.click(
				screen.getByRole("combobox", { name: "Filter sources" }),
			);
			// Git, Image, then Database.
			await user.keyboard("{ArrowDown}{ArrowDown}{ArrowRight}");
			expect(
				screen.getByRole("combobox", { name: "Filter databases" }),
			).toBeVisible();
			await user.keyboard("{ArrowLeft}");
			expect(labels(list)[0]).toBe("Git repository");

			await user.keyboard("{ArrowDown}{ArrowDown}{ArrowRight}");
			expect(
				screen.getByRole("combobox", { name: "Filter databases" }),
			).toBeVisible();
			await user.keyboard("{Backspace}");
			expect(labels(list)[0]).toBe("Git repository");
		});

		test("ArrowRight on another row, or with text typed, does not enter Database", async () => {
			stubCatalog();
			const user = userEvent.setup();
			await renderButton();
			await openMenu(user);
			const combobox = screen.getByRole("combobox", { name: "Filter sources" });
			await user.click(combobox);
			await user.keyboard("{ArrowRight}");
			expect(screen.getByRole("combobox", { name: "Filter sources" })).toBe(
				combobox,
			);
			await user.type(combobox, "data");
			await user.keyboard("{ArrowRight}");
			expect(
				screen.queryByRole("combobox", { name: "Filter databases" }),
			).toBeNull();
		});

		test("Backspace with text typed edits the text instead of going back", async () => {
			stubCatalog();
			const user = userEvent.setup();
			await renderButton();
			const list = await openMenu(user);
			await user.click(within(list).getByRole("option", { name: /Database/ }));
			const combobox = screen.getByRole("combobox", {
				name: "Filter databases",
			});
			await user.type(combobox, "my");
			await user.keyboard("{Backspace}");
			expect(combobox).toHaveValue("m");
		});

		test("arrows with nothing to move over leave the field alone", async () => {
			stubCatalog();
			const user = userEvent.setup();
			await renderButton();
			await openMenu(user);
			const combobox = screen.getByRole("combobox", { name: "Filter sources" });
			await user.type(combobox, "zzzz");
			await user.keyboard("{ArrowDown}{ArrowUp}");
			expect(combobox).toHaveValue("zzzz");
			expect(combobox).not.toHaveAttribute("aria-activedescendant");
		});
	});

	test("picking External service opens the route dialog, not a service dialog, and closes the menu", async () => {
		stubCatalog();
		const user = userEvent.setup();
		await renderButton();
		const route = renderHook(() => useRouteCreateRequest());
		const list = await openMenu(user);
		await user.click(
			within(list).getByRole("option", { name: /External service/ }),
		);
		expect(route.result.current).toEqual({ mode: "external" });
		expect(storeState()).toEqual({ request: null, menu: false });
	});
});
