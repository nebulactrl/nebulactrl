import { describe, expect, it } from "bun:test";
import { useLocation } from "@tanstack/react-router";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { useDeleteProject } from "./delete-project-dialog";

const PROJECT = { id: "p1", name: "Shop" };

const PLAN = make("DeletionPlan", {
	backupDestinationsKept: true,
	databases: 1,
	domains: 2,
	environments: [
		{ id: "e1", name: "Production", production: true },
		{ id: "e2", name: "Staging", production: false },
	],
	managedDnsRecords: 0,
	previews: 0,
	project: { id: "p1", name: "Shop", slug: "shop" },
	services: 3,
	statusPageComponents: 0,
	volumes: 1,
});

function Harness() {
	const { ask, dialog } = useDeleteProject();
	const pathname = useLocation({ select: (l) => l.pathname });
	return (
		<>
			<p data-testid="where">{pathname}</p>
			<button type="button" onClick={() => ask(PROJECT)}>
				Ask
			</button>
			{dialog}
		</>
	);
}

/** Holds the dialog's confirm key for the full 1.3 s the guard demands. */
async function holdToDelete() {
	const button = await screen.findByRole("button", {
		name: /hold to delete project/i,
	});
	fireEvent.keyDown(button, { key: " " });
	return button;
}

describe("useDeleteProject", () => {
	it("renders no dialog and requests no plan until asked", async () => {
		const api = stubApi({});
		await renderWithProviders(<Harness />, { path: "/p/p1/settings" });
		expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	it("lists what the server would delete and keeps the action locked until the name is typed", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"GET /projects/:id/deletion-plan": { body: PLAN },
		});
		await renderWithProviders(<Harness />, { path: "/p/p1/settings" });
		await user.click(screen.getByRole("button", { name: "Ask" }));

		const dialog = await screen.findByRole("alertdialog");
		expect(within(dialog).getByText("Delete Shop?")).toBeInTheDocument();
		expect(
			await within(dialog).findByText("2 environments (Production, Staging)"),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText("3 services, 1 database"),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText("1 volume and its data"),
		).toBeInTheDocument();
		expect(within(dialog).getByText("2 domains")).toBeInTheDocument();
		expect(
			within(dialog).getByText(
				"Shared variables of the project and its environments",
			),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText(
				"Any backups already written to your destinations are kept.",
			),
		).toBeInTheDocument();
		expect(api.calls[0]?.path).toBe("/projects/p1/deletion-plan");

		// Wrong name: the hold is refused and nothing is sent.
		fireEvent.change(within(dialog).getByLabelText(/to confirm/), {
			target: { value: "shop" },
		});
		const hold = await holdToDelete();
		expect(hold).toBeEnabled();
		expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
	});

	it("omits the backups note when the project has no data", async () => {
		const user = userEvent.setup();
		stubApi({
			"GET /projects/:id/deletion-plan": {
				body: make("DeletionPlan", { ...PLAN, databases: 0, volumes: 0 }),
			},
		});
		await renderWithProviders(<Harness />, { path: "/p/p1/settings" });
		await user.click(screen.getByRole("button", { name: "Ask" }));
		await screen.findByText("3 services");
		expect(
			screen.queryByText(/backups already written/),
		).not.toBeInTheDocument();
	});

	it("shows a loading state and no hold key action while the plan is pending", async () => {
		const user = userEvent.setup();
		stubApi({
			"GET /projects/:id/deletion-plan": () => new Promise<Response>(() => {}),
		});
		await renderWithProviders(<Harness />, { path: "/p/p1/settings" });
		await user.click(screen.getByRole("button", { name: "Ask" }));
		expect(
			await screen.findByRole("status", {
				name: "Counting what this project contains",
			}),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: /hold to delete project/i }),
		).toBeDisabled();
	});

	it("says nothing can be deleted when the plan fails to load", async () => {
		const user = userEvent.setup();
		stubApi({
			"GET /projects/:id/deletion-plan": problem(500, {
				title: "Internal",
				status: 500,
				detail: "plan exploded",
			}),
		});
		await renderWithProviders(<Harness />, { path: "/p/p1/settings" });
		await user.click(screen.getByRole("button", { name: "Ask" }));
		expect(
			await screen.findByText(/so nothing can be deleted yet\. plan exploded/),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: /hold to delete project/i }),
		).toBeDisabled();
	});

	it("deletes with cascade and the typed name, then leaves for the home page", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"GET /projects/:id/deletion-plan": { body: PLAN },
			"DELETE /projects/:id": { status: 204 },
			"GET /projects": { body: { items: [] } },
		});
		await renderWithProviders(<Harness />, { path: "/p/p1/settings" });
		expect(screen.getByTestId("where")).toHaveTextContent("/p/p1/settings");
		await user.click(screen.getByRole("button", { name: "Ask" }));
		await screen.findByText("3 services, 1 database");
		fireEvent.change(screen.getByLabelText(/to confirm/), {
			target: { value: "Shop" },
		});
		await holdToDelete();

		await waitFor(
			() => expect(screen.getByTestId("where")).toHaveTextContent(/^\/$/),
			{ timeout: 4000 },
		);
		const del = api.calls.find((c) => c.method === "DELETE");
		expect(del?.path).toBe("/projects/p1");
		expect(del?.query.get("cascade")).toBe("true");
		expect(del?.query.get("confirm")).toBe("Shop");
	}, 10_000);

	it("keeps the dialog open with the server's reason when the delete is refused", async () => {
		const user = userEvent.setup();
		stubApi({
			"GET /projects/:id/deletion-plan": { body: PLAN },
			"DELETE /projects/:id": problem(409, {
				title: "Conflict",
				status: 409,
				detail: "Deploy in flight",
			}),
		});
		await renderWithProviders(<Harness />, { path: "/p/p1/settings" });
		await user.click(screen.getByRole("button", { name: "Ask" }));
		await screen.findByText("3 services, 1 database");
		fireEvent.change(screen.getByLabelText(/to confirm/), {
			target: { value: "Shop" },
		});
		await holdToDelete();

		expect(
			await screen.findByText("Deploy in flight", {}, { timeout: 4000 }),
		).toBeInTheDocument();
		expect(screen.getByRole("alertdialog")).toBeInTheDocument();
		expect(screen.getByTestId("where")).toHaveTextContent("/p/p1/settings");
	}, 10_000);
});
