import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { ProjectBuildLocation } from "./project-build-location";
import { wireMe, wireProject, wireRegistry, wireServer } from "./test-fixtures";

const routes = (project: Record<string, unknown> = wireProject()) => ({
	"GET /me": { body: wireMe(["admin"]) },
	"GET /projects": {
		body: { items: [project, wireProject({ id: "proj-2", name: "Other" })] },
	},
	"GET /team/build-settings": {
		body: { buildLocation: "target", registryId: "reg-1", updatedAt: null },
	},
	"GET /servers": {
		body: { items: [wireServer({ id: "b1", name: "build-01" })] },
	},
	"GET /registries": {
		body: {
			items: [
				wireRegistry({ id: "reg-1", name: "GHCR" }),
				wireRegistry({ id: "reg-2", name: "Quay", host: "quay.io" }),
			],
		},
	},
});

const saveButton = () => screen.getByRole("button", { name: /Save changes/ });

describe("ProjectBuildLocation", () => {
	test("shows the project's own setting and what an inherit means", async () => {
		stubApi(routes());
		await renderWithProviders(<ProjectBuildLocation projectId="proj-1" />);
		expect(
			await screen.findByRole("button", { name: "Inherit", pressed: true }),
		).toBeInTheDocument();
		await waitFor(() =>
			expect(screen.getByRole("status")).toHaveTextContent(
				"Uses the team setting: builds run on the app's server.",
			),
		);
		expect(screen.getByText("No unsaved changes.")).toBeInTheDocument();
	});

	test("a project that is not in the list is reported as not found", async () => {
		stubApi(routes());
		await renderWithProviders(<ProjectBuildLocation projectId="missing" />);
		expect(await screen.findByText("Project not found")).toBeInTheDocument();
	});

	test("a failed project list shows the error and Try again reloads it", async () => {
		let attempts = 0;
		const base = routes();
		const api = stubApi({
			...base,
			"GET /projects": () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{ title: "Boom", status: 500, detail: "database down" },
							{ status: 500 },
						)
					: Response.json({ items: [wireProject()] });
			},
		});
		await renderWithProviders(<ProjectBuildLocation projectId="proj-1" />);
		expect(
			await screen.findByText("Couldn't load the project"),
		).toBeInTheDocument();
		expect(screen.getByText("database down")).toBeInTheDocument();
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: /retry|try again/i }));
		expect(
			await screen.findByRole("button", { name: "Inherit", pressed: true }),
		).toBeInTheDocument();
		expect(api.calls.filter((c) => c.path === "/projects")).toHaveLength(2);
	});

	test("the registry entry names the team default it inherits", async () => {
		stubApi(routes(wireProject({ buildLocation: "build_server" })));
		await renderWithProviders(<ProjectBuildLocation projectId="proj-1" />);
		expect(
			await screen.findByRole("combobox", { name: "Registry" }),
		).toHaveTextContent("Inherit (team default: GHCR)");
	});

	test("saving PATCHes the project with its location, pin and registry, then reloads and toasts", async () => {
		let saved: Record<string, unknown> | null = null;
		const base = routes(wireProject({ buildLocation: "build_server" }));
		const api = stubApi({
			...base,
			"GET /projects": () =>
				Response.json({
					items: [
						saved
							? wireProject(saved)
							: wireProject({ buildLocation: "build_server" }),
					],
				}),
			"PATCH /projects/:id": async (request) => {
				saved = await request.json();
				return Response.json(wireProject(saved ?? {}));
			},
		});
		await renderWithProviders(<ProjectBuildLocation projectId="proj-1" />);
		const user = userEvent.setup();
		await user.click(
			await screen.findByRole("combobox", { name: "Build server" }),
		);
		await user.click(await screen.findByRole("option", { name: "build-01" }));
		await user.click(screen.getByRole("combobox", { name: "Registry" }));
		await user.click(await screen.findByRole("option", { name: /Quay/ }));
		expect(screen.getByText("You have unsaved changes.")).toBeInTheDocument();
		await user.click(saveButton());
		expect(
			await screen.findByText("Project build settings saved"),
		).toBeInTheDocument();
		const patch = api.calls.find((call) => call.method === "PATCH");
		expect(patch).toMatchObject({
			path: "/projects/proj-1",
			body: {
				buildLocation: "build_server",
				buildServerId: "b1",
				registryId: "reg-2",
			},
		});
		// The refetched project is the new baseline: the form is clean again.
		await waitFor(() =>
			expect(screen.getByText("No unsaved changes.")).toBeInTheDocument(),
		);
		expect(api.unmatched).toEqual([]);
	});

	test("clearing the pin and the registry omits their ids so the server clears them", async () => {
		const api = stubApi({
			...routes(
				wireProject({
					buildLocation: "build_server",
					buildServerId: "b1",
					registryId: "reg-2",
				}),
			),
			"PATCH /projects/:id": { body: wireProject() },
		});
		await renderWithProviders(<ProjectBuildLocation projectId="proj-1" />);
		const user = userEvent.setup();
		const pin = await screen.findByRole("combobox", { name: "Build server" });
		await waitFor(() => expect(pin).toHaveTextContent("build-01"));
		await user.click(pin);
		await user.click(await screen.findByRole("option", { name: /Automatic/ }));
		const registry = screen.getByRole("combobox", { name: "Registry" });
		await user.click(registry);
		await user.click(await screen.findByRole("option", { name: /^Inherit/ }));
		await user.click(saveButton());
		await screen.findByText("Project build settings saved");
		expect(api.calls.find((call) => call.method === "PATCH")?.body).toEqual({
			buildLocation: "build_server",
		});
	});

	test("choosing Inherit clears the pin and the registry so the team default applies", async () => {
		const api = stubApi({
			...routes(
				wireProject({
					buildLocation: "build_server",
					buildServerId: "b1",
					registryId: "reg-2",
				}),
			),
			"PATCH /projects/:id": { body: wireProject() },
		});
		await renderWithProviders(<ProjectBuildLocation projectId="proj-1" />);
		const user = userEvent.setup();
		const pin = await screen.findByRole("combobox", { name: "Build server" });
		await waitFor(() => expect(pin).toHaveTextContent("build-01"));
		await user.click(screen.getByRole("button", { name: "Inherit" }));
		expect(
			screen.queryByRole("combobox", { name: "Build server" }),
		).not.toBeInTheDocument();
		await user.click(saveButton());
		await screen.findByText("Project build settings saved");
		expect(api.calls.find((call) => call.method === "PATCH")?.body).toEqual({
			buildLocation: "inherit",
		});
	});

	test("Discard restores the saved setting", async () => {
		stubApi(routes());
		await renderWithProviders(<ProjectBuildLocation projectId="proj-1" />);
		const user = userEvent.setup();
		await user.click(
			await screen.findByRole("button", { name: "On the app's server" }),
		);
		await user.click(screen.getByRole("button", { name: "Discard" }));
		expect(
			screen.getByRole("button", { name: "Inherit", pressed: true }),
		).toBeInTheDocument();
		expect(screen.getByText("No unsaved changes.")).toBeInTheDocument();
	});

	test("a refused save shows the reason and no success toast", async () => {
		stubApi({
			...routes(),
			"PATCH /projects/:id": problem(409, {
				title: "Conflict",
				status: 409,
				detail: "Build server b1 cannot build",
			}),
		});
		await renderWithProviders(<ProjectBuildLocation projectId="proj-1" />);
		const user = userEvent.setup();
		await user.click(
			await screen.findByRole("button", { name: "On the app's server" }),
		);
		await user.click(saveButton());
		expect(
			await screen.findByText("Build server b1 cannot build"),
		).toBeInTheDocument();
		expect(
			screen.queryByText("Project build settings saved"),
		).not.toBeInTheDocument();
	});

	test("someone else's save replaces a clean form but not one being edited", async () => {
		let current = wireProject();
		const api = stubApi({
			...routes(),
			"GET /projects": () => Response.json({ items: [current] }),
		});
		const { queryClient } = await renderWithProviders(
			<ProjectBuildLocation projectId="proj-1" />,
		);
		await screen.findByRole("button", { name: "Inherit", pressed: true });
		current = wireProject({ buildLocation: "target" });
		await queryClient
			.invalidateQueries({ queryKey: ["listProjects"] })
			.catch(() => {});
		await queryClient.refetchQueries();
		expect(
			await screen.findByRole("button", {
				name: "On the app's server",
				pressed: true,
			}),
		).toBeInTheDocument();
		expect(
			api.calls.filter((c) => c.path === "/projects").length,
		).toBeGreaterThan(1);
		// Now edit locally; a further outside change must not overwrite the draft.
		const user = userEvent.setup();
		await user.click(screen.getByRole("button", { name: "On a build server" }));
		current = wireProject({ buildLocation: "inherit" });
		await queryClient.refetchQueries();
		await waitFor(() =>
			expect(
				api.calls.filter((c) => c.path === "/projects").length,
			).toBeGreaterThan(2),
		);
		expect(
			screen.getByRole("button", { name: "On a build server", pressed: true }),
		).toBeInTheDocument();
		expect(screen.getByText("You have unsaved changes.")).toBeInTheDocument();
	});
});
