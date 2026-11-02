import { describe, expect, it } from "bun:test";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { ForgotPasswordPage } from "./forgot-password-page";

const email = () => screen.getByLabelText("Email");
const send = () => screen.getByRole("button", { name: "Send reset link" });

describe("ForgotPasswordPage", () => {
	it("asks for an email and keeps Send disabled until one is typed", async () => {
		const user = userEvent.setup();
		stubApi({ "GET /auth/password-reset": { body: { available: true } } });
		await renderWithProviders(<ForgotPasswordPage />);
		expect(
			screen.getByRole("heading", { name: "Reset your password" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("link", { name: "Back to sign in" }),
		).toHaveAttribute("href", "/login");
		await user.type(email(), "   ");
		expect(send()).toBeDisabled();
		await user.type(email(), "ada@example.com");
		await screen.findByRole("button", { name: "Send reset link" });
		expect(send()).toBeEnabled();
	});

	it("sends the trimmed email and then says the same thing for every address", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"GET /auth/password-reset": { body: { available: true } },
			"POST /auth/password-reset/request": {
				status: 202,
				body: make("PasswordResetRequestOutputBody", { status: "requested" }),
			},
		});
		await renderWithProviders(<ForgotPasswordPage />);
		await user.type(email(), "  ada@example.com ");
		await user.click(send());
		expect(
			await screen.findByText(/If that account can reset its password/),
		).toHaveTextContent("expires after 30 minutes");
		const post = api.calls.filter((c) => c.method === "POST");
		expect(post).toHaveLength(1);
		expect(post[0]).toMatchObject({
			path: "/auth/password-reset/request",
			body: { email: "ada@example.com" },
		});
		// The form and its intro line are gone.
		expect(screen.queryByLabelText("Email")).toBeNull();
		expect(screen.queryByText(/we send you a link/)).toBeNull();
		expect(api.unmatched).toEqual([]);
	});

	it("keeps the form and tells a rate-limited person to wait", async () => {
		const user = userEvent.setup();
		stubApi({
			"GET /auth/password-reset": { body: { available: true } },
			"POST /auth/password-reset/request": problem(429, {
				detail: "slow down",
			}),
		});
		await renderWithProviders(<ForgotPasswordPage />);
		await user.type(email(), "ada@example.com");
		await user.click(send());
		expect(
			await screen.findByText(
				"Too many reset requests. Wait a while, then try again.",
			),
		).toBeInTheDocument();
		expect(email()).toHaveValue("ada@example.com");
	});

	it("shows the server's own message for other failures", async () => {
		const user = userEvent.setup();
		stubApi({
			"GET /auth/password-reset": { body: { available: true } },
			"POST /auth/password-reset/request": problem(500, {
				detail: "Mail relay refused the message.",
			}),
		});
		await renderWithProviders(<ForgotPasswordPage />);
		await user.type(email(), "ada@example.com");
		await user.click(send());
		expect(
			await screen.findByText("Mail relay refused the message."),
		).toBeInTheDocument();
	});

	it("replaces the form with the host instructions when reset by email is off", async () => {
		stubApi({ "GET /auth/password-reset": { body: { available: false } } });
		await renderWithProviders(<ForgotPasswordPage />);
		const notice = await screen.findByRole("alert");
		expect(notice).toHaveTextContent(
			"Password reset by email is not set up on this instance. Ask an admin to send you a reset link.",
		);
		expect(notice).toHaveTextContent(
			"nebulactrl-server reset-password <email>",
		);
		expect(screen.queryByLabelText("Email")).toBeNull();
		expect(screen.queryByText(/we send you a link/)).toBeNull();
		expect(screen.getByRole("link", { name: "How" })).toHaveAttribute(
			"href",
			expect.stringContaining("operations/troubleshooting"),
		);
	});

	it("switches to the host instructions when the send answers password_reset_unavailable", async () => {
		const user = userEvent.setup();
		stubApi({
			"GET /auth/password-reset": { body: { available: true } },
			"POST /auth/password-reset/request": problem(409, {
				code: "password_reset_unavailable",
				detail: "No email.",
			}),
		});
		await renderWithProviders(<ForgotPasswordPage />);
		await user.type(email(), "ada@example.com");
		await user.click(send());
		const notice = await screen.findByRole("alert");
		expect(notice).toHaveTextContent("not set up on this instance");
		expect(screen.queryByLabelText("Email")).toBeNull();
	});
});
