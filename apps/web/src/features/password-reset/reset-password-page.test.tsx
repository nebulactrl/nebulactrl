import { describe, expect, it } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { ResetPasswordPage } from "./reset-password-page";

const password = () => screen.getByLabelText("New password");
const repeat = () => screen.getByLabelText("Repeat the password");
const submit = () => screen.getByRole("button", { name: "Change password" });

const GOOD = "correct horse battery";

describe("ResetPasswordPage", () => {
	it("asks for a new link when the URL carries no token", async () => {
		const api = stubApi({});
		await renderWithProviders(<ResetPasswordPage token={undefined} />);
		expect(
			screen.getByRole("heading", { name: "Reset link missing" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Ask for a new link" }),
		).toHaveAttribute("href", "/forgot-password");
		expect(api.calls).toEqual([]);
	});

	it("checks the link first and shows the form once the server accepts it", async () => {
		const api = stubApi({
			"POST /auth/password-reset/check": { status: 204 },
		});
		await renderWithProviders(<ResetPasswordPage token="tok-1" />);
		expect(
			await screen.findByRole("heading", { name: "Choose a new password" }),
		).toBeInTheDocument();
		expect(api.calls[0]).toMatchObject({
			path: "/auth/password-reset/check",
			body: { token: "tok-1" },
		});
	});

	it("shows a dead link instead of a form when the server calls the token invalid", async () => {
		stubApi({
			"POST /auth/password-reset/check": problem(400, {
				code: "invalid_reset_token",
				detail: "bad token",
			}),
		});
		await renderWithProviders(<ResetPasswordPage token="old" />);
		expect(
			await screen.findByRole("heading", {
				name: "This reset link does not work",
			}),
		).toBeInTheDocument();
		expect(screen.getByRole("alert")).toHaveTextContent(
			"invalid, has expired or was already used",
		);
		expect(screen.queryByLabelText("New password")).toBeNull();
		expect(
			screen.getByRole("button", { name: "Ask for a new link" }),
		).toHaveAttribute("href", "/forgot-password");
	});

	it("keeps the form up when the check fails for another reason", async () => {
		stubApi({
			"POST /auth/password-reset/check": problem(429, {
				code: "rate_limited",
				detail: "slow",
			}),
		});
		await renderWithProviders(<ResetPasswordPage token="tok" />);
		expect(await screen.findByLabelText("New password")).toBeInTheDocument();
	});

	it("blocks Change password until the password is long enough and repeated", async () => {
		const user = userEvent.setup();
		const api = stubApi({ "POST /auth/password-reset/check": { status: 204 } });
		await renderWithProviders(<ResetPasswordPage token="tok" />);
		await screen.findByLabelText("New password");
		expect(screen.getByText("At least 10 characters.")).toBeInTheDocument();
		expect(submit()).toBeDisabled();

		await user.type(password(), "short");
		await user.type(repeat(), "short");
		expect(submit()).toBeDisabled();

		await user.clear(password());
		await user.clear(repeat());
		await user.type(password(), GOOD);
		await user.type(repeat(), "correct horse");
		expect(
			screen.getByText("The two passwords do not match."),
		).toBeInTheDocument();
		expect(submit()).toBeDisabled();

		await user.clear(repeat());
		await user.type(repeat(), GOOD);
		expect(screen.queryByText("The two passwords do not match.")).toBeNull();
		expect(submit()).toBeEnabled();
		expect(api.calls.filter((c) => c.method === "POST")).toHaveLength(1); // only the check
	});

	it("changes the password, keeping API tokens, and lists the ones that still work", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"POST /auth/password-reset/check": { status: 204 },
			"POST /auth/password-reset/confirm": {
				body: make("PasswordResetConfirmOutputBody", {
					revokedTokens: 0,
					keptTokens: [
						{
							id: "k1",
							name: "ci-deploy",
							prefix: "nbc_ab12",
							teamId: "t1",
							teamName: "Acme",
							createdAt: "2026-01-01T00:00:00Z",
						},
					],
				}),
			},
		});
		const { queryClient } = await renderWithProviders(
			<ResetPasswordPage token="tok-9" />,
		);
		await screen.findByLabelText("New password");
		queryClient.setQueryData(["me"], { name: "Ada" });
		await user.type(password(), GOOD);
		await user.type(repeat(), GOOD);
		await user.click(submit());

		expect(
			await screen.findByRole("heading", { name: "Password changed" }),
		).toBeInTheDocument();
		const confirm = api.calls.find(
			(c) => c.path === "/auth/password-reset/confirm",
		);
		expect(confirm?.body).toEqual({
			token: "tok-9",
			password: GOOD,
			revokeApiTokens: false,
		});
		expect(screen.getByText(/signed out on every device/)).toBeInTheDocument();
		expect(screen.getByText(/1 API token still works/)).toBeInTheDocument();
		const item = screen.getByRole("listitem");
		expect(item).toHaveTextContent("ci-deploy");
		expect(item).toHaveTextContent("nbc_ab12");
		expect(item).toHaveTextContent("Acme");
		expect(item).toHaveTextContent("never used");
		expect(screen.getByRole("button", { name: "Sign in" })).toHaveAttribute(
			"href",
			"/login",
		);
		// Every session ended: nothing cached about the old user survives.
		expect(queryClient.getQueryData(["me"])).toBeUndefined();
	});

	it("sends revokeApiTokens when ticked and reports how many were revoked", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"POST /auth/password-reset/check": { status: 204 },
			"POST /auth/password-reset/confirm": {
				body: make("PasswordResetConfirmOutputBody", {
					revokedTokens: 3,
					keptTokens: null,
				}),
			},
		});
		await renderWithProviders(<ResetPasswordPage token="tok" />);
		await screen.findByLabelText("New password");
		await user.type(password(), GOOD);
		await user.type(repeat(), GOOD);
		await user.click(screen.getByRole("checkbox"));
		await user.click(submit());
		expect(
			await screen.findByText("3 API tokens revoked."),
		).toBeInTheDocument();
		const confirm = api.calls.find(
			(c) => c.path === "/auth/password-reset/confirm",
		);
		expect(confirm?.body).toMatchObject({ revokeApiTokens: true });
		expect(screen.queryByRole("list")).toBeNull();
	});

	it("offers a new link when the token died between the check and the submit", async () => {
		const user = userEvent.setup();
		stubApi({
			"POST /auth/password-reset/check": { status: 204 },
			"POST /auth/password-reset/confirm": problem(400, {
				code: "invalid_reset_token",
				detail: "gone",
			}),
		});
		await renderWithProviders(<ResetPasswordPage token="tok" />);
		await screen.findByLabelText("New password");
		await user.type(password(), GOOD);
		await user.type(repeat(), GOOD);
		await user.click(submit());
		const alert = await screen.findByRole("alert");
		expect(alert).toHaveTextContent("invalid, has expired or was already used");
		expect(
			within(alert).getByRole("link", { name: "New link" }),
		).toHaveAttribute("href", "/forgot-password");
		// The form stays so nothing typed is lost.
		expect(password()).toHaveValue(GOOD);
	});

	it("explains a weak password and a rate limit in words", async () => {
		const user = userEvent.setup();
		let n = 0;
		stubApi({
			"POST /auth/password-reset/check": { status: 204 },
			"POST /auth/password-reset/confirm": () => {
				n++;
				return n === 1
					? Response.json(
							{ code: "weak_password", detail: "x" },
							{
								status: 422,
								headers: { "Content-Type": "application/problem+json" },
							},
						)
					: Response.json(
							{ code: "rate_limited", detail: "x" },
							{
								status: 429,
								headers: { "Content-Type": "application/problem+json" },
							},
						);
			},
		});
		await renderWithProviders(<ResetPasswordPage token="tok" />);
		await screen.findByLabelText("New password");
		await user.type(password(), GOOD);
		await user.type(repeat(), GOOD);
		await user.click(submit());
		expect(
			await screen.findByText("Choose a password of at least 10 characters."),
		).toBeInTheDocument();
		expect(screen.queryByRole("link", { name: "New link" })).toBeNull();
		await user.click(submit());
		expect(
			await screen.findByText(
				"Too many attempts. Wait a minute, then try again.",
			),
		).toBeInTheDocument();
	});
});
