import { afterEach, describe, expect, mock, test } from "bun:test";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createActions, useCreateState } from "#/features/create/store";
import {
	templatesActions,
	useTemplatesState,
} from "#/features/templates/store";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { CanvasEmptyState } from "./empty-state";

afterEach(() => {
	act(() => {
		createActions.closeDialog();
		createActions.setMenuOpen(false);
		templatesActions.close();
	});
});

/** Prints what the module-level stores hold so a test sees what a click opened. */
function Probe() {
	const create = useCreateState();
	const templates = useTemplatesState();
	return (
		<output data-testid="probe">
			{JSON.stringify({
				request: create.request,
				menu: create.menu !== null,
				templates: templates.open,
			})}
		</output>
	);
}

function probed(): {
	request: { kind: string } | null;
	menu: boolean;
	templates: boolean;
} {
	return JSON.parse(screen.getByTestId("probe").textContent ?? "{}");
}

const ENV = {
	id: "env-1",
	projectId: "p1",
	slug: "production",
	name: "Production",
	isProduction: true,
	kind: "standard",
	parentEnvironmentId: null,
	prNumber: null,
	prTitle: "",
	createdAt: "2026-01-01T00:00:00Z",
};

async function show(error?: { message: string; onRetry(): void }) {
	return renderWithProviders(
		<>
			<CanvasEmptyState error={error} />
			<Probe />
		</>,
	);
}

describe("CanvasEmptyState", () => {
	test("invites the first service with the primary and secondary actions", async () => {
		await show();
		expect(
			screen.getByRole("heading", { name: "Create your first service" }),
		).toBeInTheDocument();
		expect(
			screen.getByText(/Deploy a Docker image or a Git repository/),
		).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Create" })).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Browse templates" }),
		).toBeEnabled();
		expect(screen.getByRole("button", { name: "Add database" })).toBeEnabled();
		expect(screen.getByText(/right-click to create here/)).toBeInTheDocument();
		expect(
			screen.getByText(/right-click to create here/).parentElement,
		).toHaveTextContent("⌘K actions");
	});

	test("shows no error alert while the environment loaded", async () => {
		await show();
		expect(screen.queryByRole("alert")).not.toBeInTheDocument();
	});

	test("Browse templates opens the template gallery", async () => {
		const user = userEvent.setup();
		await show();
		expect(probed().templates).toBe(false);
		await user.click(screen.getByRole("button", { name: "Browse templates" }));
		expect(probed().templates).toBe(true);
	});

	test("Add database opens the database dialog, not the generic menu", async () => {
		const user = userEvent.setup();
		await show();
		await user.click(screen.getByRole("button", { name: "Add database" }));
		expect(probed().request?.kind).toBe("database");
		expect(probed().menu).toBe(false);
	});

	test("Create is off outside a project environment", async () => {
		await show();
		expect(screen.getByRole("button", { name: "Create" })).toBeDisabled();
	});

	test("inside an environment Create opens the source menu", async () => {
		const user = userEvent.setup();
		stubApi({
			"GET /projects": {
				body: make("PageProject", {
					items: [make("Project", { id: "p1", name: "Shop" })],
				}),
			},
			"GET /projects/:projectId/environments": { body: { items: [ENV] } },
			"GET /database-engines": { body: { engines: [] } },
		});
		await renderWithProviders(
			<>
				<CanvasEmptyState />
				<Probe />
			</>,
			{ path: "/p/p1/production", routePath: "/p/$projectId/$envSlug" },
		);
		const create = screen.getByRole("button", { name: "Create" });
		await waitFor(() => expect(create).toBeEnabled());
		await user.click(create);
		expect(
			await screen.findByRole("combobox", { name: "Filter sources" }),
		).toBeInTheDocument();
		expect(probed().menu).toBe(true);
	});

	test("a load failure sits beside the invitation with its message and a retry", async () => {
		const user = userEvent.setup();
		const onRetry = mock(() => {});
		await show({ message: "database is locked", onRetry });
		const alert = screen.getByRole("alert");
		expect(alert).toHaveTextContent(
			"Couldn't load this environment's services: database is locked",
		);
		// The empty-state invitation is still there.
		expect(
			screen.getByRole("heading", { name: "Create your first service" }),
		).toBeInTheDocument();
		await user.click(within(alert).getByRole("button", { name: "Try again" }));
		expect(onRetry).toHaveBeenCalledTimes(1);
	});
});
