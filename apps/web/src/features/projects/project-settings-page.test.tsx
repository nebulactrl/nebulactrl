import { describe, expect, it } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { ProjectSettingsPage } from "./project-settings-page";

const PROJECT = {
	buildLocation: "inherit",
	buildServerId: "",
	createdAt: "2026-01-01T00:00:00Z",
	description: "The storefront",
	id: "p1",
	name: "Shop",
	registryId: "",
	slug: "shop",
	system: false,
	updatedAt: "2026-01-01T00:00:00Z",
};

function environment(over: Record<string, unknown>) {
	return {
		createdAt: "2026-01-01T00:00:00Z",
		id: "e0",
		isProduction: false,
		kind: "standard",
		name: "Staging",
		parentEnvironmentId: null,
		prNumber: null,
		prTitle: "",
		projectId: "p1",
		slug: "staging",
		...over,
	};
}

function me(role: string, scopes: string[] = []) {
	return {
		createdAt: "2026-01-01T00:00:00Z",
		email: "a@example.com",
		id: "u1",
		instanceAdmin: false,
		name: "Ada",
		principal: { label: "Ada", role, scopes, type: "user" },
		workspaceId: "w1",
	};
}

const OWNER = { "GET /me": { body: me("owner", ["admin"]) } };
const PROJECTS = { "GET /projects": { body: { items: [PROJECT] } } };

function render(hash = "", routes: Parameters<typeof stubApi>[0] = {}) {
	const api = stubApi({ ...OWNER, ...PROJECTS, ...routes });
	return renderWithProviders(<ProjectSettingsPage projectId="p1" />, {
		path: `/p/p1/settings${hash}`,
	}).then((view) => ({ api, ...view }));
}

describe("ProjectSettingsPage", () => {
	it("shows a not-found state when the project is not in the list", async () => {
		stubApi({ ...OWNER, "GET /projects": { body: { items: [] } } });
		await renderWithProviders(<ProjectSettingsPage projectId="gone" />);
		expect(await screen.findByText("Project not found")).toBeInTheDocument();
		expect(screen.getByText(/may have been deleted/)).toBeInTheDocument();
	});

	it("explains a failed project load and retries", async () => {
		const user = userEvent.setup();
		let fail = true;
		const api = stubApi({
			...OWNER,
			"GET /projects": () =>
				fail
					? Response.json(
							{ title: "Down", status: 503, detail: "control plane down" },
							{ status: 503 },
						)
					: Response.json({ items: [PROJECT] }),
		});
		await renderWithProviders(<ProjectSettingsPage projectId="p1" />);
		expect(
			await screen.findByText("Couldn't load the project"),
		).toBeInTheDocument();
		expect(screen.getByText("control plane down")).toBeInTheDocument();
		fail = false;
		await user.click(screen.getByRole("button", { name: "Try again" }));
		expect(await screen.findByLabelText("Name")).toHaveValue("Shop");
		expect(api.unmatched).toEqual([]);
	});

	describe("General", () => {
		it("shows the project's fields, slug and id, with Save locked until something changes", async () => {
			await render();
			expect(await screen.findByLabelText("Name")).toHaveValue("Shop");
			expect(screen.getByLabelText("Description")).toHaveValue(
				"The storefront",
			);
			expect(screen.getByText("shop")).toBeInTheDocument();
			expect(screen.getByText("p1")).toBeInTheDocument();
			expect(
				screen.getByText("Project · Shop · applies to all its environments"),
			).toBeInTheDocument();
			expect(
				screen.getByRole("button", { name: "Save changes" }),
			).toBeDisabled();
		});

		it("does not enable Save for a blank name", async () => {
			const user = userEvent.setup();
			await render();
			await user.clear(await screen.findByLabelText("Name"));
			expect(
				screen.getByRole("button", { name: "Save changes" }),
			).toBeDisabled();
		});

		it("saves the trimmed name with the description, then reloads the project list", async () => {
			const user = userEvent.setup();
			let saved = false;
			const api = stubApi({
				...OWNER,
				"GET /projects": () =>
					Response.json({
						items: [
							saved
								? { ...PROJECT, name: "Store", description: "New copy" }
								: PROJECT,
						],
					}),
				"PATCH /projects/:id": () => {
					saved = true;
					return Response.json({ ...PROJECT, name: "Store" });
				},
			});
			await renderWithProviders(<ProjectSettingsPage projectId="p1" />, {
				path: "/p/p1/settings",
			});
			const name = await screen.findByLabelText("Name");
			await user.clear(name);
			await user.type(name, " Store ");
			const description = screen.getByLabelText("Description");
			await user.clear(description);
			await user.type(description, "New copy");
			await user.click(screen.getByRole("button", { name: "Save changes" }));

			expect(await screen.findByText("Project saved")).toBeInTheDocument();
			const patch = api.calls.find((c) => c.method === "PATCH");
			expect(patch?.path).toBe("/projects/p1");
			expect(patch?.body).toEqual({ name: "Store", description: "New copy" });
			// The refetched list replaces the form and the title line.
			await waitFor(() =>
				expect(
					screen.getByText("Project · Store · applies to all its environments"),
				).toBeInTheDocument(),
			);
			expect(screen.getByLabelText("Name")).toHaveValue("Store");
			expect(
				screen.getByRole("button", { name: "Save changes" }),
			).toBeDisabled();
		});

		it("saves a description change alone", async () => {
			const user = userEvent.setup();
			const api = await render("", {
				"PATCH /projects/:id": { body: PROJECT },
			}).then((v) => v.api);
			await user.type(await screen.findByLabelText("Description"), "!");
			await user.click(screen.getByRole("button", { name: "Save changes" }));
			await waitFor(() =>
				expect(api.calls.find((c) => c.method === "PATCH")?.body).toEqual({
					name: "Shop",
					description: "The storefront!",
				}),
			);
		});

		it("shows the server's reason when saving fails and keeps the edit", async () => {
			const user = userEvent.setup();
			await render("", {
				"PATCH /projects/:id": problem(409, {
					title: "Conflict",
					status: 409,
					detail: "Name taken",
				}),
			});
			const name = await screen.findByLabelText("Name");
			await user.type(name, "x");
			await user.click(screen.getByRole("button", { name: "Save changes" }));
			expect(await screen.findByText("Name taken")).toBeInTheDocument();
			expect(screen.getByLabelText("Name")).toHaveValue("Shopx");
		});
	});

	describe("Environments", () => {
		it("lists own environments production first and leaves previews out", async () => {
			await render("#environments", {
				"GET /projects/:id/environments": {
					body: {
						items: [
							environment({ id: "e1", name: "Staging", slug: "staging" }),
							environment({
								id: "e2",
								name: "Production",
								slug: "production",
								isProduction: true,
							}),
							environment({
								id: "e3",
								name: "PR 7",
								slug: "pr-7",
								kind: "preview",
							}),
						],
					},
				},
			});
			const links = await screen.findAllByRole("link", {
				name: /Staging|Production|PR 7/,
			});
			expect(links.map((l) => l.getAttribute("href"))).toEqual([
				"/p/p1/production",
				"/p/p1/staging",
			]);
			// Only the production environment carries the badge.
			expect(
				within(links[0] as HTMLElement).getAllByText("Production"),
			).toHaveLength(2);
			expect(
				within(links[1] as HTMLElement).getAllByText("Staging"),
			).toHaveLength(1);
			expect(screen.queryByText("PR 7")).not.toBeInTheDocument();
		});

		it("explains a failed environments load and retries", async () => {
			const user = userEvent.setup();
			let fail = true;
			await render("#environments", {
				"GET /projects/:id/environments": () =>
					fail
						? Response.json(
								{ title: "Down", status: 500, detail: "envs unavailable" },
								{ status: 500 },
							)
						: Response.json({
								items: [environment({ name: "Staging", slug: "staging" })],
							}),
			});
			expect(
				await screen.findByText("Environments did not load"),
			).toBeInTheDocument();
			expect(screen.getByText("envs unavailable")).toBeInTheDocument();
			fail = false;
			await user.click(screen.getByRole("button", { name: "Try again" }));
			expect(
				await screen.findByRole("link", { name: /Staging/ }),
			).toBeInTheDocument();
		});
	});

	describe("Danger zone", () => {
		it("offers deleting the project and opens the confirmation with its plan", async () => {
			const user = userEvent.setup();
			const api = await render("#danger", {
				"GET /projects/:id/deletion-plan": {
					body: make("DeletionPlan", {
						backupDestinationsKept: false,
						databases: 0,
						domains: 0,
						environments: [{ id: "e1", name: "Production", production: true }],
						managedDnsRecords: 0,
						previews: 0,
						project: { id: "p1", name: "Shop", slug: "shop" },
						services: 2,
						statusPageComponents: 0,
						volumes: 0,
					}),
				},
			}).then((v) => v.api);
			expect(
				await screen.findByRole("heading", { name: "Delete this project" }),
			).toBeInTheDocument();
			await user.click(screen.getByRole("button", { name: "Delete project" }));
			const dialog = await screen.findByRole("alertdialog");
			expect(within(dialog).getByText("Delete Shop?")).toBeInTheDocument();
			expect(await within(dialog).findByText("2 services")).toBeInTheDocument();
			expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
		});
	});

	describe("sections by role", () => {
		it("shows the build location page to an owner", async () => {
			await render("#build-location", {
				"GET /team/build-settings": {
					body: make("TeamSettings", {
						buildLocation: "target",
						registryId: "",
					}),
				},
				"GET /registries": { body: { items: [] } },
			});
			expect(
				await screen.findByRole("heading", { name: "Build location" }),
			).toBeInTheDocument();
			expect(
				await screen.findByText("No unsaved changes."),
			).toBeInTheDocument();
		});

		it("hides the build location page from a developer", async () => {
			await render("", { "GET /me": { body: me("developer") } });
			await screen.findByLabelText("Name");
			await waitFor(() =>
				expect(
					screen.queryByRole("link", { name: "Build location" }),
				).not.toBeInTheDocument(),
			);
			expect(
				screen.getByRole("link", { name: "Environments" }),
			).toBeInTheDocument();
			expect(
				screen.getByRole("link", { name: "Danger zone" }),
			).toBeInTheDocument();
		});

		it("falls back to General for an unknown section", async () => {
			await render("#nonsense");
			expect(await screen.findByLabelText("Name")).toHaveValue("Shop");
		});
	});
});
