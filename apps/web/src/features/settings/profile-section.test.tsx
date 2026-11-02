import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { ProfileSection } from "./profile-section";

const me = {
	id: "u1",
	name: "Ada Lovelace",
	email: "ada@example.com",
	instanceAdmin: false,
	workspaceId: "w1",
	createdAt: "2026-01-01T00:00:00Z",
	principal: { type: "user", label: "Ada", scopes: [] },
};

const problem = (status: number, detail: string, code = "bad_request") => ({
	status,
	body: { status, title: "Error", detail, code },
});

describe("ProfileSection", () => {
	test("shows the account email and the saved name once loaded", async () => {
		const api = stubApi({ "GET /me": { body: me } });
		await renderWithProviders(<ProfileSection />);
		expect(await screen.findByLabelText("Name")).toHaveValue("Ada Lovelace");
		expect(screen.getByText("ada@example.com")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Save name" })).toBeDisabled();
		expect(api.unmatched).toEqual([]);
	});

	test("offers a retry when the profile did not load, and recovers", async () => {
		let attempts = 0;
		stubApi({
			"GET /me": () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{ status: 500, title: "x", detail: "boom", code: "internal" },
							{ status: 500 },
						)
					: Response.json(me);
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<ProfileSection />);
		expect(
			await screen.findByText("Your profile did not load"),
		).toBeInTheDocument();
		expect(screen.queryByLabelText("Name")).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: /try again|retry/i }));
		expect(await screen.findByLabelText("Name")).toHaveValue("Ada Lovelace");
	});

	test("saving the name sends the trimmed name in a PATCH /me and confirms", async () => {
		const api = stubApi({
			"GET /me": { body: me },
			"PATCH /me": { body: { ...me, name: "Ada King" } },
		});
		const user = userEvent.setup();
		await renderWithProviders(<ProfileSection />);
		const name = await screen.findByLabelText("Name");
		await user.clear(name);
		await user.type(name, "  Ada King  ");
		await user.click(screen.getByRole("button", { name: "Save name" }));
		expect(await screen.findByText("Profile saved")).toBeInTheDocument();
		const patch = api.calls.find((call) => call.method === "PATCH");
		expect(patch?.path).toBe("/me");
		expect(patch?.body).toEqual({ name: "Ada King" });
		expect(api.unmatched).toEqual([]);
	});

	test("a blank or unchanged name cannot be saved", async () => {
		const api = stubApi({ "GET /me": { body: me } });
		const user = userEvent.setup();
		await renderWithProviders(<ProfileSection />);
		const name = await screen.findByLabelText("Name");
		const save = screen.getByRole("button", { name: "Save name" });
		await user.clear(name);
		expect(save).toBeDisabled();
		await user.type(name, "   ");
		expect(save).toBeDisabled();
		await user.clear(name);
		await user.type(name, "Ada Lovelace ");
		expect(save).toBeDisabled();
		await user.type(name, "X");
		expect(save).toBeEnabled();
		expect(api.calls.filter((call) => call.method !== "GET")).toEqual([]);
	});

	test("a name the server rejects shows its reason", async () => {
		stubApi({
			"GET /me": { body: me },
			"PATCH /me": problem(422, "Name is too long"),
		});
		const user = userEvent.setup();
		await renderWithProviders(<ProfileSection />);
		const name = await screen.findByLabelText("Name");
		await user.type(name, "x");
		await user.click(screen.getByRole("button", { name: "Save name" }));
		expect(await screen.findByText("Name is too long")).toBeInTheDocument();
		expect(screen.queryByText("Profile saved")).not.toBeInTheDocument();
	});

	describe("password", () => {
		async function open(routes: Parameters<typeof stubApi>[0] = {}) {
			const api = stubApi({ "GET /me": { body: me }, ...routes });
			const user = userEvent.setup();
			await renderWithProviders(<ProfileSection />);
			await screen.findByLabelText("Current password");
			return { api, user };
		}

		test("stays disabled until current and new password are typed", async () => {
			const { user } = await open();
			const button = screen.getByRole("button", { name: "Change password" });
			expect(button).toBeDisabled();
			await user.type(screen.getByLabelText("Current password"), "old-secret");
			expect(button).toBeDisabled();
			await user.type(screen.getByLabelText("New password"), "n");
			expect(button).toBeEnabled();
		});

		test("a short new password is refused without a request", async () => {
			const { api, user } = await open();
			await user.type(screen.getByLabelText("Current password"), "old-secret");
			await user.type(screen.getByLabelText("New password"), "short");
			await user.type(screen.getByLabelText("Repeat new password"), "short");
			await user.click(screen.getByRole("button", { name: "Change password" }));
			expect(
				await screen.findByText("Use at least 10 characters."),
			).toBeInTheDocument();
			expect(screen.getByLabelText("New password")).toHaveAttribute(
				"aria-invalid",
				"true",
			);
			expect(api.calls.filter((call) => call.method === "POST")).toEqual([]);
		});

		test("mismatched repeats are refused without a request", async () => {
			const { api, user } = await open();
			await user.type(screen.getByLabelText("Current password"), "old-secret");
			await user.type(screen.getByLabelText("New password"), "long-enough-1");
			await user.type(
				screen.getByLabelText("Repeat new password"),
				"long-enough-2",
			);
			await user.click(screen.getByRole("button", { name: "Change password" }));
			expect(
				await screen.findByText("The two passwords do not match."),
			).toBeInTheDocument();
			expect(api.calls.filter((call) => call.method === "POST")).toEqual([]);
		});

		test("changing the password posts it, clears the form and lists kept tokens", async () => {
			const { api, user } = await open({
				"POST /me/password": {
					body: {
						revokedTokens: 0,
						keptTokens: [
							{
								id: "t1",
								name: "ci-deploy",
								prefix: "nbl_abc",
								teamId: "team1",
								teamName: "Platform",
								createdAt: "2026-01-01T00:00:00Z",
							},
						],
					},
				},
			});
			await user.type(screen.getByLabelText("Current password"), "old-secret");
			await user.type(screen.getByLabelText("New password"), "long-enough-1");
			await user.type(
				screen.getByLabelText("Repeat new password"),
				"long-enough-1",
			);
			await user.click(screen.getByRole("button", { name: "Change password" }));
			expect(await screen.findByText("Password changed")).toBeInTheDocument();
			expect(
				screen.getByText("Your other sessions were signed out."),
			).toBeInTheDocument();
			const post = api.calls.find((call) => call.method === "POST");
			expect(post?.path).toBe("/me/password");
			expect(post?.body).toEqual({
				currentPassword: "old-secret",
				newPassword: "long-enough-1",
				revokeApiTokens: false,
			});
			expect(screen.getByLabelText("Current password")).toHaveValue("");
			expect(screen.getByLabelText("New password")).toHaveValue("");
			expect(screen.getByLabelText("Repeat new password")).toHaveValue("");
			expect(
				screen.getByText("This API token still works:"),
			).toBeInTheDocument();
			expect(screen.getByText("ci-deploy")).toBeInTheDocument();
		});

		test("ticking revoke sends revokeApiTokens and reports the count", async () => {
			const { api, user } = await open({
				"POST /me/password": { body: { revokedTokens: 2, keptTokens: null } },
			});
			await user.type(screen.getByLabelText("Current password"), "old-secret");
			await user.type(screen.getByLabelText("New password"), "long-enough-1");
			await user.type(
				screen.getByLabelText("Repeat new password"),
				"long-enough-1",
			);
			await user.click(
				screen.getByRole("checkbox", { name: "Also revoke my API tokens" }),
			);
			await user.click(screen.getByRole("button", { name: "Change password" }));
			expect(
				await screen.findByText("2 API tokens were revoked."),
			).toBeInTheDocument();
			expect(
				api.calls.find((call) => call.method === "POST")?.body,
			).toMatchObject({ revokeApiTokens: true });
			await waitFor(() =>
				expect(
					screen.getByRole("checkbox", { name: "Also revoke my API tokens" }),
				).not.toBeChecked(),
			);
		});

		test("a wrong current password shows the server's reason and keeps the input", async () => {
			const { user } = await open({
				"POST /me/password": problem(
					403,
					"Current password is wrong",
					"forbidden",
				),
			});
			await user.type(screen.getByLabelText("Current password"), "nope-nope-1");
			await user.type(screen.getByLabelText("New password"), "long-enough-1");
			await user.type(
				screen.getByLabelText("Repeat new password"),
				"long-enough-1",
			);
			await user.click(screen.getByRole("button", { name: "Change password" }));
			expect(
				await screen.findByText("Current password is wrong"),
			).toBeInTheDocument();
			expect(screen.queryByText("Password changed")).not.toBeInTheDocument();
			expect(screen.getByLabelText("Current password")).toHaveValue(
				"nope-nope-1",
			);
		});
	});
});
