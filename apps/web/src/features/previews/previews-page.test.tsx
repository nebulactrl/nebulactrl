import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { PreviewsPage } from "./previews-page";
import { wireEnv, wirePreview, wireService, wireSettings } from "./test-data";

const ENVS = [wireEnv()];

function routes(extra: Parameters<typeof stubApi>[0] = {}) {
	return stubApi({
		"GET /projects/:id/environments": { body: { items: ENVS } },
		"GET /projects/:id/previews": {
			body: make("ListOutputBody2", { items: [wirePreview()] }),
		},
		"GET /projects/:id/previews/settings": { body: wireSettings() },
		"GET /environments/:id/services": {
			body: make("PageServiceView", { items: [wireService("web")] }),
		},
		...extra,
	});
}

const page = (envSlug = "production") => (
	<PreviewsPage projectId="proj-1" envSlug={envSlug} />
);

describe("PreviewsPage", () => {
	test("shows the open previews above the settings form", async () => {
		const api = routes();

		await renderWithProviders(page());

		expect(
			await screen.findByRole("heading", { level: 1, name: "Previews" }),
		).toBeInTheDocument();
		const open = await screen.findByRole("region", { name: "Open previews" });
		expect(
			await within(open).findByText(/#12 Add checkout/),
		).toBeInTheDocument();
		expect(
			screen.getByText(/Deleted automatically when the pull request closes/),
		).toBeInTheDocument();
		expect(
			await screen.findByRole("switch", { name: "Previews" }),
		).toBeChecked();
		expect(await screen.findByRole("checkbox", { name: /Web/ })).toBeChecked();
		expect(api.unmatched).toEqual([]);
	});

	test("an unknown environment says so and loads no previews", async () => {
		const api = routes();

		await renderWithProviders(page("nope"));

		expect(
			await screen.findByText("Environment not found"),
		).toBeInTheDocument();
		expect(
			screen.getByText(/no environment called “nope”/),
		).toBeInTheDocument();
		expect(api.calls.some((c) => c.path.includes("/previews"))).toBe(false);
	});

	test("a failure loading the settings shows the reason and Try again reloads them", async () => {
		let calls = 0;
		routes({
			"GET /projects/:id/previews/settings": () => {
				calls += 1;
				return calls === 1
					? Response.json(
							{ code: "boom", detail: "settings store down" },
							{ status: 500 },
						)
					: Response.json(wireSettings());
			},
		});
		const user = userEvent.setup();

		await renderWithProviders(page());

		expect(
			await screen.findByText("Couldn't load the preview settings"),
		).toBeInTheDocument();
		expect(screen.getByText("settings store down")).toBeInTheDocument();
		// The previews list is independent of the settings.
		expect(await screen.findByText(/#12 Add checkout/)).toBeInTheDocument();
		const buttons = screen.getAllByRole("button", { name: "Try again" });
		await user.click(buttons[buttons.length - 1] as HTMLElement);

		expect(
			await screen.findByRole("switch", { name: "Previews" }),
		).toBeInTheDocument();
		expect(
			screen.queryByText("Couldn't load the preview settings"),
		).not.toBeInTheDocument();
	});

	test("a failing previews list does not hide the settings", async () => {
		routes({
			"GET /projects/:id/previews": problem(500, {
				code: "boom",
				detail: "previews store down",
			}),
		});

		await renderWithProviders(page());

		expect(
			await screen.findByText("Couldn't load previews"),
		).toBeInTheDocument();
		expect(
			await screen.findByRole("switch", { name: "Previews" }),
		).toBeInTheDocument();
	});
});
