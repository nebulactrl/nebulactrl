import { describe, expect, test } from "bun:test";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { TeamMember } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { ResetLinkDialog } from "./reset-link-dialog";

const member: TeamMember = {
	userId: "u-2",
	name: "Ada Lovelace",
	email: "ada@example.com",
	role: "developer",
	joinedAt: "2026-01-01T00:00:00Z",
	you: false,
};

function Harness({ initial = member }: { initial?: TeamMember | null }) {
	const [open, setOpen] = useState<TeamMember | null>(initial);
	return (
		<>
			<span data-testid="state">{open ? "open" : "closed"}</span>
			<button type="button" onClick={() => setOpen(member)}>
				reopen
			</button>
			<ResetLinkDialog
				teamId="t-1"
				member={open}
				onClose={() => setOpen(null)}
			/>
		</>
	);
}

describe("ResetLinkDialog", () => {
	test("renders nothing without a member", async () => {
		await renderWithProviders(<Harness initial={null} />);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
	});

	test("asks before creating anything, then shows the one-time link", async () => {
		const api = stubApi({
			"POST /teams/:teamId/members/:userId/password-reset": {
				status: 201,
				body: {
					link: "/reset-password/tok123",
					token: "tok123",
					expiresAt: "2026-01-01T00:30:00Z",
				},
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<Harness />);

		expect(
			await screen.findByText("Reset password for Ada Lovelace"),
		).toBeInTheDocument();
		expect(
			screen.getByText(/It works once and expires after 30 minutes/),
		).toBeInTheDocument();
		expect(api.calls).toEqual([]);

		await user.click(screen.getByRole("button", { name: "Generate link" }));

		// A path link is made absolute with the page's origin.
		expect(await screen.findByTestId("reset-link")).toHaveTextContent(
			`${window.location.origin}/reset-password/tok123`,
		);
		expect(screen.getByText("Shown once")).toBeInTheDocument();
		expect(
			screen.getByText("Send this link to Ada Lovelace yourself."),
		).toBeInTheDocument();
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]).toMatchObject({
			method: "POST",
			path: "/teams/t-1/members/u-2/password-reset",
		});
		expect(screen.queryByRole("button", { name: "Generate link" })).toBeNull();

		// By keyboard: the confirmation then swaps without the text-morph
		// animation, which happy-dom cannot cancel cleanly at unmount.
		screen.getByRole("button", { name: /copy/i }).focus();
		await user.keyboard("{Enter}");
		// user-event installs its own clipboard on setup().
		expect(await navigator.clipboard.readText()).toBe(
			`${window.location.origin}/reset-password/tok123`,
		);
	});

	test("keeps an absolute link as the server sent it", async () => {
		stubApi({
			"POST /teams/:teamId/members/:userId/password-reset": {
				status: 201,
				body: {
					link: "https://nebula.example.com/reset-password/abc",
					token: "abc",
					expiresAt: "2026-01-01T00:30:00Z",
				},
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<Harness />);
		await user.click(
			await screen.findByRole("button", { name: "Generate link" }),
		);
		expect(await screen.findByTestId("reset-link")).toHaveTextContent(
			/^https:\/\/nebula\.example\.com\/reset-password\/abc$/,
		);
	});

	test("says an identity-provider member has no password to reset", async () => {
		stubApi({
			"POST /teams/:teamId/members/:userId/password-reset": {
				status: 409,
				body: {
					status: 409,
					code: "no_password",
					title: "Conflict",
					detail: "member has no password",
				},
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<Harness />);
		await user.click(
			await screen.findByRole("button", { name: "Generate link" }),
		);

		expect(
			await screen.findByText(
				"Ada Lovelace signs in through an identity provider and has no password to reset.",
			),
		).toBeInTheDocument();
		// Still retryable.
		expect(screen.getByRole("button", { name: "Generate link" })).toBeEnabled();
	});

	test("shows the server's message for other refusals", async () => {
		stubApi({
			"POST /teams/:teamId/members/:userId/password-reset": {
				status: 409,
				body: {
					status: 409,
					code: "member_of_other_teams",
					title: "Conflict",
					detail: "only the instance admin can reset this member",
				},
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<Harness />);
		await user.click(
			await screen.findByRole("button", { name: "Generate link" }),
		);

		expect(
			await screen.findByText("only the instance admin can reset this member"),
		).toBeInTheDocument();
		expect(screen.queryByText(/identity provider/)).not.toBeInTheDocument();
	});

	test("Cancel closes; reopening forgets an earlier error", async () => {
		stubApi({
			"POST /teams/:teamId/members/:userId/password-reset": {
				status: 500,
				body: { status: 500, code: "internal", title: "Boom", detail: "boom" },
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<Harness />);
		await user.click(
			await screen.findByRole("button", { name: "Generate link" }),
		);
		expect(await screen.findByText("boom")).toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(screen.getByTestId("state")).toHaveTextContent("closed");

		await user.click(screen.getByRole("button", { name: "reopen" }));
		await screen.findByText("Reset password for Ada Lovelace");
		expect(screen.queryByText("boom")).not.toBeInTheDocument();
	});

	test("Done closes after the link was shown", async () => {
		stubApi({
			"POST /teams/:teamId/members/:userId/password-reset": {
				status: 201,
				body: { link: "/r/x", token: "x", expiresAt: "2026-01-01T00:30:00Z" },
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<Harness />);
		await user.click(
			await screen.findByRole("button", { name: "Generate link" }),
		);
		await user.click(await screen.findByRole("button", { name: "Done" }));
		expect(screen.getByTestId("state")).toHaveTextContent("closed");
	});
});
