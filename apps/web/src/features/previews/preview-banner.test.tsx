import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { PreviewBanner } from "./preview-banner";
import { Where, wireEnv, wirePreview, wirePreviewEnv } from "./test-data";

const ENVIRONMENTS = [wireEnv(), wirePreviewEnv()];

function stubPreviewApi(extra: Parameters<typeof stubApi>[0] = {}) {
	return stubApi({
		"GET /projects/:id/environments": { body: { items: ENVIRONMENTS } },
		"GET /projects/:id/previews": { body: { items: [wirePreview()] } },
		...extra,
	});
}

function banner(environment = wirePreviewEnv()) {
	return (
		<>
			<PreviewBanner environment={environment} />
			<Where />
		</>
	);
}

describe("PreviewBanner", () => {
	test("a standard environment shows nothing and loads nothing", async () => {
		const api = stubApi({});

		const { container } = await renderWithProviders(banner(wireEnv()));

		expect(
			screen.queryByRole("status", { name: "Pull-request preview" }),
		).not.toBeInTheDocument();
		expect(container.textContent).not.toContain("Preview");
		expect(api.calls).toEqual([]);
	});

	test("names the base environment, the status, the PR and the last push", async () => {
		stubPreviewApi();

		await renderWithProviders(banner());

		const status = screen.getByRole("status", { name: "Pull-request preview" });
		expect(
			await within(status).findByText("Preview of Production"),
		).toBeInTheDocument();
		expect(await within(status).findByText("Ready")).toBeInTheDocument();
		const pr = within(status).getByRole("link", {
			name: /PR #12 · Add checkout/,
		});
		expect(pr).toHaveAttribute("href", "https://github.com/acme/shop/pull/12");
		expect(within(status).getByText(/pushed/)).toBeInTheDocument();
		expect(
			within(status).getByRole("link", { name: "Open Production" }),
		).toHaveAttribute("href", "/p/proj-1/production");
	});

	test("shows the preview's status detail", async () => {
		stubApi({
			"GET /projects/:id/environments": { body: { items: ENVIRONMENTS } },
			"GET /projects/:id/previews": {
				body: {
					items: [
						wirePreview({
							status: "failed",
							statusMessage: "seed command exited 1",
						}),
					],
				},
			},
		});

		await renderWithProviders(banner());

		expect(await screen.findByText("Failed")).toBeInTheDocument();
		expect(screen.getByText("seed command exited 1")).toBeInTheDocument();
	});

	test("without a matching preview it still names the environment but cannot delete", async () => {
		stubApi({
			"GET /projects/:id/environments": { body: { items: ENVIRONMENTS } },
			"GET /projects/:id/previews": {
				body: { items: [wirePreview({ environmentId: "env-other" })] },
			},
		});

		await renderWithProviders(banner());

		await screen.findByText("Preview of Production");
		expect(screen.getByText("PR #12 · Add checkout")).toBeInTheDocument();
		expect(
			screen.queryByRole("link", { name: /PR #12/ }),
		).not.toBeInTheDocument();
		expect(screen.queryByText(/pushed/)).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Delete" })).toBeDisabled();
	});

	test("without a PR link the name is plain text; without a base it offers no way back", async () => {
		stubApi({
			"GET /projects/:id/environments": {
				body: { items: [wirePreviewEnv({ parentEnvironmentId: null })] },
			},
			"GET /projects/:id/previews": {
				body: { items: [wirePreview({ prUrl: "" })] },
			},
		});

		await renderWithProviders(
			banner(wirePreviewEnv({ parentEnvironmentId: null })),
		);

		expect(await screen.findByText("Ready")).toBeInTheDocument();
		expect(screen.getByText("Preview")).toBeInTheDocument();
		expect(screen.queryByRole("link")).not.toBeInTheDocument();
		expect(screen.getByText("PR #12 · Add checkout")).toBeInTheDocument();
	});

	test("a preview already being deleted cannot be deleted again", async () => {
		stubApi({
			"GET /projects/:id/environments": { body: { items: ENVIRONMENTS } },
			"GET /projects/:id/previews": {
				body: { items: [wirePreview({ status: "deleting" })] },
			},
		});

		await renderWithProviders(banner());

		expect(await screen.findByText("Deleting")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Delete" })).toBeDisabled();
	});

	test("Delete asks first, deletes the preview and goes back to the base environment", async () => {
		const api = stubPreviewApi({
			"DELETE /previews/:id": () => new Response(null, { status: 202 }),
		});
		const user = userEvent.setup();

		await renderWithProviders(banner(), { path: "/p/proj-1/pr-12" });
		await screen.findByText("Ready");
		await waitFor(() =>
			expect(screen.getByRole("button", { name: "Delete" })).toBeEnabled(),
		);
		await user.click(screen.getByRole("button", { name: "Delete" }));

		expect(
			await screen.findByText("Delete the preview of PR #12?"),
		).toBeInTheDocument();
		expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
		await user.click(screen.getByRole("button", { name: "Delete preview" }));

		await waitFor(() =>
			expect(screen.getByLabelText("location")).toHaveTextContent(
				"/p/proj-1/production",
			),
		);
		expect(api.calls.find((c) => c.method === "DELETE")?.path).toBe(
			"/previews/pv-1",
		);
		expect(await screen.findByText("Deleting PR #12")).toBeInTheDocument();
	});

	test("backing out leaves the preview and the page alone", async () => {
		const api = stubPreviewApi();
		const user = userEvent.setup();

		await renderWithProviders(banner(), { path: "/p/proj-1/pr-12" });
		await screen.findByText("Ready");
		await user.click(screen.getByRole("button", { name: "Delete" }));
		await screen.findByText("Delete the preview of PR #12?");
		await user.click(screen.getByRole("button", { name: "Cancel" }));

		await waitFor(() =>
			expect(
				screen.queryByText("Delete the preview of PR #12?"),
			).not.toBeInTheDocument(),
		);
		expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
		expect(screen.getByLabelText("location")).toHaveTextContent(
			"/p/proj-1/pr-12",
		);
	});
});
