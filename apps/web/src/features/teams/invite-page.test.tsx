import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { InvitePreview } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, type Overrides, problem } from "#/test/factories";
import { renderWithProviders, testQueryClient } from "#/test/render";
import { InvitePage } from "./invite-page";

function preview(over: Overrides<InvitePreview> = {}): InvitePreview {
	return make("InvitePreview", {
		expiresAt: "2026-02-01T00:00:00Z",
		role: "developer",
		teamName: "Acme",
		teamSlug: "acme",
		invitedBy: "Ada",
		...over,
	});
}

/** What a successful `POST /invites/:token/accept` answers. */
const joined = {
	body: make("AcceptedInvite", { team: { name: "Acme", slug: "acme" } }),
};

const previewRoute = (body: InvitePreview) => ({
	"GET /invites/:token": { body },
});

const accepted = (api: ReturnType<typeof stubApi>) =>
	api.calls.find((c) => c.method === "POST");

describe("InvitePage", () => {
	test("says it is checking while the preview loads", async () => {
		stubApi({
			"GET /invites/:token": () => new Promise<Response>(() => undefined),
		});
		await renderWithProviders(<InvitePage token="tok" signedIn={false} />);
		expect(screen.getByText(/Checking the invite/)).toBeInTheDocument();
	});

	test.each([
		[
			"invite_expired",
			"This invite has expired. Ask the person who invited you for a new one.",
		],
		[
			"invite_used",
			"This invite has already been used. Ask for a new one, or sign in if you already joined.",
		],
		[
			"not_found",
			"This invite link is not valid. It may have been revoked or mistyped.",
		],
	])("an unavailable invite (%s) explains itself", async (code, text) => {
		stubApi({
			"GET /invites/:token": problem(404, { code, title: "Not found" }),
		});
		await renderWithProviders(<InvitePage token="tok" signedIn={false} />);

		expect(await screen.findByText("Invite unavailable")).toBeInTheDocument();
		expect(screen.getByText(text)).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Go to sign in" }),
		).toHaveAttribute("href", "/login");
		expect(screen.queryByLabelText("Password")).not.toBeInTheDocument();
	});

	test("a signed-in visitor with a dead invite is sent to the projects", async () => {
		stubApi({
			"GET /invites/:token": problem(404, {
				code: "invite_used",
				title: "Gone",
			}),
		});
		await renderWithProviders(<InvitePage token="tok" signedIn />);
		expect(
			await screen.findByRole("button", { name: "Go to projects" }),
		).toHaveAttribute("href", "/");
	});

	test("a signed-in visitor joins with one click and an empty body", async () => {
		const api = stubApi({
			...previewRoute(preview()),
			"POST /invites/:token/accept": joined,
		});
		const queryClient = testQueryClient();
		queryClient.setQueryData(["stale"], "previous team");
		const user = userEvent.setup();
		await renderWithProviders(<InvitePage token="tok-1" signedIn />, {
			queryClient,
		});

		expect(
			await screen.findByRole("heading", { name: "Join Acme" }),
		).toBeInTheDocument();
		expect(screen.getByText("Ada")).toBeInTheDocument();
		expect(screen.getByText(/invited you/)).toBeInTheDocument();
		expect(screen.getByText("Developer")).toBeInTheDocument();
		expect(screen.queryByLabelText("Password")).not.toBeInTheDocument();
		expect(
			screen.queryByText("Already have an account?"),
		).not.toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "Join Acme" }));

		await waitFor(() => expect(accepted(api)).toBeDefined());
		expect(accepted(api)).toMatchObject({
			path: "/invites/tok-1/accept",
			body: {},
		});
		// What was cached belongs to the previous account/team.
		await waitFor(() =>
			expect(queryClient.getQueryData(["stale"])).toBeUndefined(),
		);
	});

	test("a new visitor must give a name and a password of 10+ characters before joining", async () => {
		const api = stubApi({
			...previewRoute(preview()),
			"POST /invites/:token/accept": joined,
		});
		const user = userEvent.setup();
		await renderWithProviders(<InvitePage token="tok-2" signedIn={false} />);

		const join = await screen.findByRole("button", {
			name: "Create account and join",
		});
		expect(join).toBeDisabled();
		await user.type(screen.getByLabelText("Your name"), "  Grace  ");
		await user.type(screen.getByLabelText("Email"), "grace@example.com");
		await user.type(screen.getByLabelText("Password"), "123456789");
		expect(join).toBeDisabled();
		await user.type(screen.getByLabelText("Password"), "0");
		expect(join).toBeEnabled();
		await user.click(join);

		await waitFor(() => expect(accepted(api)).toBeDefined());
		expect(accepted(api)).toMatchObject({
			path: "/invites/tok-2/accept",
			body: {
				name: "Grace",
				password: "1234567890",
				email: "grace@example.com",
			},
		});
	});

	test("an invite bound to an address fixes the email and does not send one", async () => {
		const api = stubApi({
			...previewRoute(preview({ email: "bound@example.com" })),
			"POST /invites/:token/accept": joined,
		});
		const user = userEvent.setup();
		await renderWithProviders(<InvitePage token="tok-3" signedIn={false} />);

		const email = await screen.findByLabelText("Email");
		expect(email).toHaveValue("bound@example.com");
		expect(email).toHaveAttribute("readonly");
		await user.type(email, "other@example.com");
		expect(email).toHaveValue("bound@example.com");

		await user.type(screen.getByLabelText("Your name"), "Grace");
		await user.type(screen.getByLabelText("Password"), "a-long-password");
		await user.click(
			screen.getByRole("button", { name: "Create account and join" }),
		);

		await waitFor(() => expect(accepted(api)).toBeDefined());
		expect(accepted(api)?.body).toEqual({
			name: "Grace",
			password: "a-long-password",
		});
	});

	test("without an inviter name the header skips the avatar line", async () => {
		stubApi(previewRoute(preview({ role: "viewer", invitedBy: undefined })));
		await renderWithProviders(<InvitePage token="tok" signedIn={false} />);
		expect(
			await screen.findByRole("heading", { name: "Join Acme" }),
		).toBeInTheDocument();
		expect(screen.queryByText(/invited you/)).not.toBeInTheDocument();
		expect(screen.getByText("Viewer")).toBeInTheDocument();
	});

	test("an existing account gets pointed to sign in", async () => {
		stubApi({
			...previewRoute(preview()),
			"POST /invites/:token/accept": problem(409, {
				code: "account_exists",
				title: "Conflict",
				detail: "email taken",
			}),
		});
		const user = userEvent.setup();
		await renderWithProviders(<InvitePage token="tok" signedIn={false} />);
		await user.type(await screen.findByLabelText("Your name"), "Grace");
		await user.type(screen.getByLabelText("Email"), "grace@example.com");
		await user.type(screen.getByLabelText("Password"), "a-long-password");
		await user.click(
			screen.getByRole("button", { name: "Create account and join" }),
		);

		expect(
			await screen.findByText(
				"An account with that email already exists. Sign in, then open this link again.",
			),
		).toBeInTheDocument();
		expect(screen.queryByText("email taken")).not.toBeInTheDocument();
	});

	test("shows the server's message for any other refusal", async () => {
		stubApi({
			...previewRoute(preview()),
			"POST /invites/:token/accept": problem(422, {
				code: "weak_password",
				title: "Unprocessable",
				detail: "password is too common",
			}),
		});
		const user = userEvent.setup();
		await renderWithProviders(<InvitePage token="tok" signedIn={false} />);
		await user.type(await screen.findByLabelText("Your name"), "Grace");
		await user.type(screen.getByLabelText("Email"), "grace@example.com");
		await user.type(screen.getByLabelText("Password"), "password123");
		await user.click(
			screen.getByRole("button", { name: "Create account and join" }),
		);
		expect(
			await screen.findByText("password is too common"),
		).toBeInTheDocument();
	});

	test("signed out visitors can sign in first and come back to the invite", async () => {
		stubApi(previewRoute(preview()));
		await renderWithProviders(<InvitePage token="tok-9" signedIn={false} />);
		const link = await screen.findByRole("link", { name: "Sign in" });
		const href = link.getAttribute("href") ?? "";
		expect(href.startsWith("/login")).toBe(true);
		expect(decodeURIComponent(href)).toContain("returnTo=/invite/tok-9");
	});
});
