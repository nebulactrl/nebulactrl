import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { RegistriesSection } from "./registries-section";
import { wireBuiltin, wireRegistry } from "./test-data";

const quiet = {
	"GET /servers": { body: { items: [] } },
};

describe("RegistriesSection", () => {
	test("shows a placeholder while the list loads", async () => {
		stubApi({
			"GET /registries": () => new Promise<Response>(() => undefined),
		});

		const { container } = await renderWithProviders(<RegistriesSection />);

		expect(screen.queryByRole("article")).not.toBeInTheDocument();
		expect(screen.queryByText("Add registry")).not.toBeInTheDocument();
		expect(container.querySelector("[data-slot='skeleton']")).not.toBeNull();
	});

	test("a team without registries gets an empty state whose button opens the add dialog", async () => {
		stubApi({ ...quiet, "GET /registries": { body: { items: [] } } });
		const user = userEvent.setup();

		await renderWithProviders(<RegistriesSection />);

		expect(
			await screen.findByText("Add a registry to build on a build server"),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Add registry" }));
		expect(await screen.findByText("Add a registry")).toBeInTheDocument();
		expect(screen.getByLabelText("Host")).toBeInTheDocument();
	});

	test("lists each registry with a count line, singular for one", async () => {
		stubApi({
			...quiet,
			"GET /registries": { body: { items: [wireBuiltin()] } },
		});

		await renderWithProviders(<RegistriesSection />);

		expect(await screen.findByText(/^1 registry\./)).toBeInTheDocument();
		expect(
			screen.getByRole("article", { name: "Built-in registry" }),
		).toBeInTheDocument();
	});

	test("lists several registries with a plural count", async () => {
		stubApi({
			...quiet,
			"GET /registries": {
				body: {
					items: [
						wireBuiltin(),
						wireRegistry({ id: "reg-2", name: "Docker Hub" }),
					],
				},
			},
		});

		await renderWithProviders(<RegistriesSection />);

		expect(await screen.findByText(/^2 registries\./)).toBeInTheDocument();
		expect(screen.getAllByRole("article")).toHaveLength(2);
	});

	test("a failed load explains who can see this and retries on demand", async () => {
		let calls = 0;
		stubApi({
			"GET /registries": () => {
				calls += 1;
				return calls === 1
					? Response.json(
							{ code: "forbidden", detail: "admin only" },
							{ status: 403 },
						)
					: Response.json({ items: [wireRegistry()] });
			},
		});
		const user = userEvent.setup();

		await renderWithProviders(<RegistriesSection />);

		expect(
			await screen.findByText("Registries did not load"),
		).toBeInTheDocument();
		expect(
			screen.getByText(/Registries are for team admins/),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: /retry|try again/i }));

		expect(
			await screen.findByRole("article", { name: "GitHub registry" }),
		).toBeInTheDocument();
		expect(
			screen.queryByText("Registries did not load"),
		).not.toBeInTheDocument();
	});

	test("Edit opens the dialog on that registry", async () => {
		stubApi({
			...quiet,
			"GET /registries": {
				body: {
					items: [wireRegistry({ name: "Docker Hub", host: "docker.io" })],
				},
			},
		});
		const user = userEvent.setup();

		await renderWithProviders(<RegistriesSection />);
		await user.click(await screen.findByRole("button", { name: "Edit" }));

		expect(await screen.findByText("Edit Docker Hub")).toBeInTheDocument();
		expect(screen.getByLabelText("Host")).toHaveValue("docker.io");
	});

	test("Delete asks first, then deletes, reports it and drops the card", async () => {
		let deleted = false;
		const api = stubApi({
			...quiet,
			"GET /registries": () =>
				Response.json({
					items: deleted
						? [wireBuiltin()]
						: [wireBuiltin(), wireRegistry({ name: "Docker Hub" })],
				}),
			"DELETE /registries/:id": () => {
				deleted = true;
				return new Response(null, { status: 204 });
			},
		});
		const user = userEvent.setup();

		await renderWithProviders(<RegistriesSection />);
		const card = await screen.findByRole("article", { name: "Docker Hub" });
		await user.click(within(card).getByRole("button", { name: "Delete" }));

		expect(await screen.findByText("Delete Docker Hub?")).toBeInTheDocument();
		expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
		await user.click(screen.getByRole("button", { name: "Delete registry" }));

		expect(await screen.findByText("Deleted Docker Hub")).toBeInTheDocument();
		await waitFor(() =>
			expect(
				screen.queryByRole("article", { name: "Docker Hub" }),
			).not.toBeInTheDocument(),
		);
		const del = api.calls.find((c) => c.method === "DELETE");
		expect(del?.path).toBe("/registries/reg-1");
	});

	test("backing out of the confirmation deletes nothing", async () => {
		const api = stubApi({
			...quiet,
			"GET /registries": { body: { items: [wireRegistry()] } },
		});
		const user = userEvent.setup();

		await renderWithProviders(<RegistriesSection />);
		await user.click(await screen.findByRole("button", { name: "Delete" }));
		await screen.findByText("Delete GitHub registry?");
		await user.click(screen.getByRole("button", { name: "Cancel" }));

		await waitFor(() =>
			expect(
				screen.queryByText("Delete GitHub registry?"),
			).not.toBeInTheDocument(),
		);
		expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
		expect(
			screen.getByRole("article", { name: "GitHub registry" }),
		).toBeInTheDocument();
	});

	test("a refused delete keeps the card and shows the reason in the dialog", async () => {
		stubApi({
			...quiet,
			"GET /registries": { body: { items: [wireRegistry()] } },
			"DELETE /registries/:id": problem(409, {
				code: "in_use",
				detail: "A project still builds with it.",
			}),
		});
		const user = userEvent.setup();

		await renderWithProviders(<RegistriesSection />);
		await user.click(await screen.findByRole("button", { name: "Delete" }));
		await user.click(
			await screen.findByRole("button", { name: "Delete registry" }),
		);

		const dialog = await screen.findByRole("alertdialog");
		expect(
			await within(dialog).findByText("A project still builds with it."),
		).toBeInTheDocument();
		expect(
			screen.queryByText("Deleted GitHub registry"),
		).not.toBeInTheDocument();
		// The page behind the open dialog is hidden from assistive tech.
		expect(
			screen.getByRole("article", { name: "GitHub registry", hidden: true }),
		).toBeInTheDocument();
	});
});
