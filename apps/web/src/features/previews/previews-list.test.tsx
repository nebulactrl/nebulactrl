import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { PreviewsList } from "./previews-list";
import { Where, wirePreview } from "./test-data";

function page() {
	return (
		<>
			<PreviewsList projectId="proj-1" />
			<Where />
		</>
	);
}

async function rowOf(text: string | RegExp) {
	return (await screen.findByText(text)).closest("tr") as HTMLElement;
}

describe("PreviewsList", () => {
	test("shows a loading placeholder until the list arrives", async () => {
		stubApi({
			"GET /projects/:id/previews": () =>
				new Promise<Response>(() => undefined),
		});

		await renderWithProviders(page());

		expect(
			screen.getByRole("status", { name: "Loading previews" }),
		).toBeInTheDocument();
		expect(screen.queryByRole("table")).not.toBeInTheDocument();
	});

	test("a project without previews explains when they appear", async () => {
		stubApi({ "GET /projects/:id/previews": { body: { items: [] } } });

		await renderWithProviders(page());

		expect(await screen.findByText("No previews yet")).toBeInTheDocument();
		expect(screen.getByText(/When a pull request opens/)).toBeInTheDocument();
		expect(screen.queryByRole("table")).not.toBeInTheDocument();
	});

	test("a failed load shows the reason and Try again loads the list", async () => {
		let calls = 0;
		stubApi({
			"GET /projects/:id/previews": () => {
				calls += 1;
				return calls === 1
					? Response.json(
							{ code: "boom", detail: "database is locked" },
							{ status: 500 },
						)
					: Response.json({ items: [wirePreview()] });
			},
		});
		const user = userEvent.setup();

		await renderWithProviders(page());

		expect(
			await screen.findByText("Couldn't load previews"),
		).toBeInTheDocument();
		expect(screen.getByText("database is locked")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Try again" }));

		expect(await screen.findByText(/#12 Add checkout/)).toBeInTheDocument();
		expect(
			screen.queryByText("Couldn't load previews"),
		).not.toBeInTheDocument();
	});

	test("a row names the pull request, repository, branch, short sha, status and links", async () => {
		stubApi({
			"GET /projects/:id/previews": {
				body: {
					items: [
						wirePreview({
							urls: [
								"https://pr-12.shop.example.com",
								"https://api-pr-12.shop.example.com",
							],
						}),
					],
				},
			},
		});

		await renderWithProviders(page());

		const row = await rowOf(/#12 Add checkout/);
		expect(within(row).getByText("Ready")).toBeInTheDocument();
		expect(
			within(row).getByText("acme/shop · feat/checkout · 0123456"),
		).toBeInTheDocument();
		expect(within(row).queryByText("fork")).not.toBeInTheDocument();
		const link = within(row).getByRole("link", {
			name: "pr-12.shop.example.com",
		});
		expect(link).toHaveAttribute("href", "https://pr-12.shop.example.com");
		expect(link).toHaveAttribute("target", "_blank");
		// The scheme is dropped from the label, not from the link.
		expect(link).toHaveTextContent(/^pr-12\.shop\.example\.com$/);
		expect(
			within(row).getByRole("link", { name: /api-pr-12/ }),
		).toHaveAttribute("href", "https://api-pr-12.shop.example.com");
		expect(within(row).getByRole("link", { name: "PR" })).toHaveAttribute(
			"href",
			"https://github.com/acme/shop/pull/12",
		);
	});

	test("marks fork previews, and leaves out the sha, PR link and URLs when there are none", async () => {
		stubApi({
			"GET /projects/:id/previews": {
				body: {
					items: [
						wirePreview({ fromFork: true, headSha: "", urls: [], prUrl: "" }),
					],
				},
			},
		});

		await renderWithProviders(page());

		const row = await rowOf(/#12 Add checkout/);
		expect(within(row).getByText("fork")).toBeInTheDocument();
		expect(
			within(row).getByText("acme/shop · feat/checkout"),
		).toBeInTheDocument();
		expect(within(row).getByText("—")).toBeInTheDocument();
		expect(within(row).queryByRole("link")).not.toBeInTheDocument();
	});

	test("shows why a preview is paused or failed", async () => {
		stubApi({
			"GET /projects/:id/previews": {
				body: {
					items: [
						wirePreview({
							id: "a",
							prNumber: 1,
							prTitle: "One",
							status: "paused",
						}),
						wirePreview({
							id: "b",
							prNumber: 2,
							prTitle: "Two",
							status: "failed",
							statusMessage: "seed command exited 1",
						}),
						wirePreview({
							id: "c",
							prNumber: 3,
							prTitle: "Three",
							status: "seeding",
						}),
					],
				},
			},
		});

		await renderWithProviders(page());

		const paused = await rowOf(/#1 One/);
		expect(within(paused).getByText("Paused")).toBeInTheDocument();
		expect(
			within(paused).getByText(/Paused to stay within the limit/),
		).toBeInTheDocument();
		const failed = await rowOf(/#2 Two/);
		expect(within(failed).getByText("Failed")).toBeInTheDocument();
		expect(
			within(failed).getByText("seed command exited 1"),
		).toBeInTheDocument();
		const seeding = await rowOf(/#3 Three/);
		expect(within(seeding).getByText("Seeding database")).toBeInTheDocument();
		expect(
			within(seeding).queryByText(/Paused to stay/),
		).not.toBeInTheDocument();
	});

	test("clicking a row opens that preview's environment", async () => {
		stubApi({
			"GET /projects/:id/previews": { body: { items: [wirePreview()] } },
		});
		const user = userEvent.setup();

		await renderWithProviders(page(), {
			path: "/p/proj-1/production/previews",
		});
		await user.click(await screen.findByText(/#12 Add checkout/));

		await waitFor(() =>
			expect(screen.getByLabelText("location")).toHaveTextContent(
				"/p/proj-1/pr-12",
			),
		);
	});

	test("clicking a link or button inside the row does not open the environment", async () => {
		stubApi({
			"GET /projects/:id/previews": { body: { items: [wirePreview()] } },
		});
		const user = userEvent.setup();

		await renderWithProviders(page(), { path: "/start" });
		await screen.findByText(/#12 Add checkout/);
		// The links open in a new tab; stop the click from leaving the page.
		for (const link of screen.getAllByRole("link"))
			link.addEventListener("click", (e) => e.preventDefault());
		await user.click(screen.getByRole("link", { name: "PR" }));
		await user.click(
			screen.getByRole("button", { name: "Delete the preview of PR #12" }),
		);

		expect(screen.getByLabelText("location")).toHaveTextContent("/start");
	});

	test("a preview being deleted cannot be opened or deleted again", async () => {
		stubApi({
			"GET /projects/:id/previews": {
				body: { items: [wirePreview({ status: "deleting" })] },
			},
		});
		const user = userEvent.setup();

		await renderWithProviders(page(), { path: "/start" });
		const row = await rowOf(/#12 Add checkout/);
		await user.click(within(row).getByText(/#12 Add checkout/));

		expect(within(row).getByText("Deleting")).toBeInTheDocument();
		expect(screen.getByLabelText("location")).toHaveTextContent("/start");
		expect(
			screen.getByRole("button", { name: "Delete the preview of PR #12" }),
		).toBeDisabled();
	});

	test("Delete asks first, then deletes the preview, reports it and refreshes the list", async () => {
		let deleted = false;
		const api = stubApi({
			"GET /projects/:id/previews": () =>
				Response.json({ items: deleted ? [] : [wirePreview()] }),
			"DELETE /previews/:id": () => {
				deleted = true;
				return new Response(null, { status: 202 });
			},
		});
		const user = userEvent.setup();

		await renderWithProviders(page());
		await user.click(
			await screen.findByRole("button", {
				name: "Delete the preview of PR #12",
			}),
		);

		expect(
			await screen.findByText("Delete the preview of PR #12?"),
		).toBeInTheDocument();
		expect(
			screen.getByText(/including the preview database/),
		).toBeInTheDocument();
		expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
		await user.click(screen.getByRole("button", { name: "Delete preview" }));

		expect(await screen.findByText("Deleting PR #12")).toBeInTheDocument();
		expect(
			screen.getByText(
				"Its containers, volumes and domains are being removed.",
			),
		).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "DELETE")?.path).toBe(
			"/previews/pv-1",
		);
		expect(await screen.findByText("No previews yet")).toBeInTheDocument();
	});

	test("backing out of the confirmation deletes nothing", async () => {
		const api = stubApi({
			"GET /projects/:id/previews": { body: { items: [wirePreview()] } },
		});
		const user = userEvent.setup();

		await renderWithProviders(page());
		await user.click(
			await screen.findByRole("button", {
				name: "Delete the preview of PR #12",
			}),
		);
		await screen.findByText("Delete the preview of PR #12?");
		await user.click(screen.getByRole("button", { name: "Cancel" }));

		await waitFor(() =>
			expect(
				screen.queryByText("Delete the preview of PR #12?"),
			).not.toBeInTheDocument(),
		);
		expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
	});
});
