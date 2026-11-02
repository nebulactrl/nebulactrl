import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { TeamBuildsSection } from "./team-builds-section";
import { wireMe, wireRegistry, wireServer } from "./test-fixtures";

const settings = (buildLocation: string, registryId = "") => ({
	buildLocation,
	registryId,
	updatedAt: null,
});

const common = {
	"GET /me": { body: wireMe(["admin"]) },
	"GET /servers": {
		body: { items: [wireServer({ id: "b1", name: "build-01" })] },
	},
	"GET /registries": {
		body: { items: [wireRegistry({ id: "reg-1", name: "GHCR" })] },
	},
};

const saveButton = () => screen.getByRole("button", { name: /Save changes/ });

describe("TeamBuildsSection", () => {
	test("shows the saved default, clean", async () => {
		stubApi({
			...common,
			"GET /team/build-settings": { body: settings("build_server", "reg-1") },
		});
		await renderWithProviders(<TeamBuildsSection />);
		expect(
			await screen.findByRole("button", {
				name: "On a build server",
				pressed: true,
			}),
		).toBeInTheDocument();
		expect(
			await screen.findByRole("combobox", { name: "Registry" }),
		).toHaveTextContent("GHCR");
		expect(screen.getByText("No unsaved changes.")).toBeInTheDocument();
		expect(saveButton()).toBeDisabled();
	});

	test("a change makes the form dirty; Discard puts the saved default back", async () => {
		stubApi({
			...common,
			"GET /team/build-settings": { body: settings("target") },
		});
		await renderWithProviders(<TeamBuildsSection />);
		const user = userEvent.setup();
		await user.click(
			await screen.findByRole("button", { name: "On a build server" }),
		);
		expect(screen.getByText("You have unsaved changes.")).toBeInTheDocument();
		expect(saveButton()).toBeEnabled();
		await user.click(screen.getByRole("button", { name: "Discard" }));
		expect(
			screen.getByRole("button", {
				name: "On the app's server",
				pressed: true,
			}),
		).toBeInTheDocument();
		expect(screen.getByText("No unsaved changes.")).toBeInTheDocument();
	});

	test("Save PUTs the draft, confirms in a toast and leaves the form clean", async () => {
		const api = stubApi({
			...common,
			"GET /team/build-settings": { body: settings("target") },
			"PUT /team/build-settings": (request) =>
				request
					.json()
					.then((body) =>
						Response.json({ ...body, updatedAt: "2026-02-02T00:00:00Z" }),
					),
		});
		await renderWithProviders(<TeamBuildsSection />);
		const user = userEvent.setup();
		await user.click(
			await screen.findByRole("button", { name: "On a build server" }),
		);
		const registry = await screen.findByRole("combobox", { name: "Registry" });
		await user.click(registry);
		await user.click(await screen.findByRole("option", { name: /GHCR/ }));
		await user.click(saveButton());
		expect(await screen.findByText("Build settings saved")).toBeInTheDocument();
		const put = api.calls.find((call) => call.method === "PUT");
		expect(put).toMatchObject({
			path: "/team/build-settings",
			body: { buildLocation: "build_server", registryId: "reg-1" },
		});
		await waitFor(() =>
			expect(screen.getByText("No unsaved changes.")).toBeInTheDocument(),
		);
		expect(saveButton()).toBeDisabled();
		expect(api.unmatched).toEqual([]);
	});

	test("saving no registry sends the location alone", async () => {
		const api = stubApi({
			...common,
			"GET /team/build-settings": { body: settings("target") },
			"PUT /team/build-settings": {
				body: settings("build_server"),
			},
		});
		await renderWithProviders(<TeamBuildsSection />);
		const user = userEvent.setup();
		await user.click(
			await screen.findByRole("button", { name: "On a build server" }),
		);
		await user.click(saveButton());
		await screen.findByText("Build settings saved");
		expect(api.calls.find((call) => call.method === "PUT")?.body).toEqual({
			buildLocation: "build_server",
		});
	});

	test("a refused save shows the server's reason and keeps the draft", async () => {
		stubApi({
			...common,
			"GET /team/build-settings": { body: settings("target") },
			"PUT /team/build-settings": problem(409, {
				title: "Conflict",
				status: 409,
				detail: "Registry reg-1 was removed",
			}),
		});
		await renderWithProviders(<TeamBuildsSection />);
		const user = userEvent.setup();
		await user.click(
			await screen.findByRole("button", { name: "On a build server" }),
		);
		await user.click(saveButton());
		expect(
			await screen.findByText("Registry reg-1 was removed"),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "On a build server", pressed: true }),
		).toBeInTheDocument();
		expect(screen.queryByText("Build settings saved")).not.toBeInTheDocument();
	});

	test("a failed load says so and Try again reloads", async () => {
		let attempts = 0;
		const api = stubApi({
			...common,
			"GET /team/build-settings": () => {
				attempts += 1;
				return attempts === 1
					? Response.json({ title: "Boom", status: 500 }, { status: 500 })
					: Response.json(settings("build_server"));
			},
		});
		await renderWithProviders(<TeamBuildsSection />);
		expect(
			await screen.findByText("Build settings did not load"),
		).toBeInTheDocument();
		expect(screen.getByRole("alert")).toHaveTextContent(
			/Build settings are for team admins/,
		);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: /retry|try again/i }));
		expect(
			await screen.findByRole("button", {
				name: "On a build server",
				pressed: true,
			}),
		).toBeInTheDocument();
		expect(
			api.calls.filter((c) => c.path === "/team/build-settings"),
		).toHaveLength(2);
	});
});
