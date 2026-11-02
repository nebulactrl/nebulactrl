import { afterEach, describe, expect, it, jest } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import {
	dismissedPrompt,
	FAKE_ASSERTION,
	installPasskeys,
	REQUEST_OPTIONS,
} from "./passkey-env.testkit";
import { SecondFactorStep } from "./second-factor-step";

let env: ReturnType<typeof installPasskeys> | undefined;
afterEach(() => env?.restore());

function props(
	overrides: Partial<Parameters<typeof SecondFactorStep>[0]> = {},
) {
	return {
		onVerified: jest.fn(),
		onBack: jest.fn(),
		onExpired: jest.fn(),
		...overrides,
	};
}

const TOTP_ONLY = {
	body: make("SecondFactorMethodsOutputBody", { passkey: false, totp: true }),
};

describe("SecondFactorStep: authenticator code", () => {
	it("shows a spinner and the heading while the methods load", async () => {
		stubApi({ "GET /auth/2fa": () => new Promise<Response>(() => {}) });
		await renderWithProviders(<SecondFactorStep {...props()} />);
		expect(
			screen.getByRole("heading", { name: "Two-step verification" }),
		).toBeInTheDocument();
		expect(
			screen.getByText("Checking how you can finish signing in."),
		).toBeInTheDocument();
	});

	it("asks for the 6-digit code and keeps Verify locked until it looks right", async () => {
		stubApi({ "GET /auth/2fa": TOTP_ONLY });
		const user = userEvent.setup();
		await renderWithProviders(<SecondFactorStep {...props()} />);
		const input = await screen.findByLabelText("Authenticator code");
		const verify = screen.getByRole("button", { name: "Verify" });
		expect(verify).toBeDisabled();
		await user.type(input, "12345");
		expect(verify).toBeDisabled();
		await user.type(input, "6");
		expect(verify).toBeEnabled();
	});

	it("posts the normalised code and reports success", async () => {
		const api = stubApi({
			"GET /auth/2fa": TOTP_ONLY,
			"POST /auth/2fa": { body: make("MeBody") },
		});
		const p = props();
		const user = userEvent.setup();
		await renderWithProviders(<SecondFactorStep {...p} />);
		await user.type(
			await screen.findByLabelText("Authenticator code"),
			"123 456",
		);
		await user.click(screen.getByRole("button", { name: "Verify" }));
		await waitFor(() => expect(p.onVerified).toHaveBeenCalledTimes(1));
		expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
			code: "123456",
		});
		expect(api.unmatched).toEqual([]);
	});

	it("submits with Enter", async () => {
		const api = stubApi({
			"GET /auth/2fa": TOTP_ONLY,
			"POST /auth/2fa": { body: make("MeBody") },
		});
		const p = props();
		const user = userEvent.setup();
		await renderWithProviders(<SecondFactorStep {...p} />);
		await user.type(
			await screen.findByLabelText("Authenticator code"),
			"123456{Enter}",
		);
		await waitFor(() => expect(p.onVerified).toHaveBeenCalled());
		expect(api.calls.filter((c) => c.method === "POST")).toHaveLength(1);
	});

	it("does not post an implausible code on Enter", async () => {
		const api = stubApi({ "GET /auth/2fa": TOTP_ONLY });
		const user = userEvent.setup();
		await renderWithProviders(<SecondFactorStep {...props()} />);
		await user.type(
			await screen.findByLabelText("Authenticator code"),
			"12{Enter}",
		);
		expect(api.calls.filter((c) => c.method === "POST")).toHaveLength(0);
	});

	it("says the code is not valid on a 401 and lets the person retry", async () => {
		stubApi({
			"GET /auth/2fa": TOTP_ONLY,
			"POST /auth/2fa": problem(401, { code: "invalid_code" }),
		});
		const p = props();
		const user = userEvent.setup();
		await renderWithProviders(<SecondFactorStep {...p} />);
		await user.type(
			await screen.findByLabelText("Authenticator code"),
			"123456",
		);
		await user.click(screen.getByRole("button", { name: "Verify" }));
		expect(await screen.findByRole("alert")).toHaveTextContent(
			"That code is not valid. Check the code and try again.",
		);
		expect(p.onVerified).not.toHaveBeenCalled();
		expect(screen.getByLabelText("Authenticator code")).toBeEnabled();
	});

	it("tells the person to wait on a 429", async () => {
		stubApi({
			"GET /auth/2fa": TOTP_ONLY,
			"POST /auth/2fa": problem(429, { code: "rate_limited" }),
		});
		const user = userEvent.setup();
		await renderWithProviders(<SecondFactorStep {...props()} />);
		await user.type(
			await screen.findByLabelText("Authenticator code"),
			"123456",
		);
		await user.click(screen.getByRole("button", { name: "Verify" }));
		expect(await screen.findByRole("alert")).toHaveTextContent(
			"Too many attempts. Wait a minute, then try again.",
		);
	});

	it("locks the form and offers only Back once the sign-in is locked out", async () => {
		stubApi({
			"GET /auth/2fa": TOTP_ONLY,
			"POST /auth/2fa": problem(403, { code: "two_factor_locked" }),
		});
		const p = props();
		const user = userEvent.setup();
		await renderWithProviders(<SecondFactorStep {...p} />);
		await user.type(
			await screen.findByLabelText("Authenticator code"),
			"123456",
		);
		await user.click(screen.getByRole("button", { name: "Verify" }));
		const alert = await screen.findByRole("alert");
		expect(alert).toHaveTextContent("Too many wrong codes");
		expect(screen.getByLabelText("Authenticator code")).toBeDisabled();
		expect(screen.getByRole("button", { name: "Verify" })).toBeDisabled();
		// Only the notice's own Back is left.
		expect(screen.getAllByRole("button", { name: "Back" })).toHaveLength(1);
		await user.click(screen.getByRole("button", { name: "Back" }));
		expect(p.onBack).toHaveBeenCalledTimes(1);
	});

	it("shows the caller's notice above the form until a failure replaces it", async () => {
		stubApi({
			"GET /auth/2fa": TOTP_ONLY,
			"POST /auth/2fa": problem(401, { code: "x" }),
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<SecondFactorStep {...props({ notice: "Your session ended." })} />,
		);
		expect(await screen.findByText("Your session ended.")).toBeInTheDocument();
		await user.type(screen.getByLabelText("Authenticator code"), "123456");
		await user.click(screen.getByRole("button", { name: "Verify" }));
		await screen.findByText(/That code is not valid/);
		expect(screen.queryByText("Your session ended.")).toBeNull();
	});

	it("goes back from the Back button", async () => {
		stubApi({ "GET /auth/2fa": TOTP_ONLY });
		const p = props();
		await renderWithProviders(<SecondFactorStep {...p} />);
		await userEvent
			.setup()
			.click(await screen.findByRole("button", { name: "Back" }));
		expect(p.onBack).toHaveBeenCalledTimes(1);
	});
});

describe("SecondFactorStep: recovery codes", () => {
	it("switches to a recovery code and back, clearing what was typed", async () => {
		stubApi({ "GET /auth/2fa": TOTP_ONLY });
		const user = userEvent.setup();
		await renderWithProviders(<SecondFactorStep {...props()} />);
		await user.type(await screen.findByLabelText("Authenticator code"), "123");
		await user.click(
			screen.getByRole("button", { name: "Use a recovery code" }),
		);
		const recovery = screen.getByLabelText("Recovery code");
		expect(recovery).toHaveValue("");
		expect(
			screen.getByText("Enter one of your recovery codes. Each works once."),
		).toBeInTheDocument();
		expect(
			screen.getByText("Spaces, dashes and capitals do not matter."),
		).toBeInTheDocument();
		await user.click(
			screen.getByRole("button", { name: "Use an authenticator code" }),
		);
		expect(screen.getByLabelText("Authenticator code")).toBeInTheDocument();
	});

	it("posts a recovery code as typed, minus spaces (the server ignores dashes and case)", async () => {
		const api = stubApi({
			"GET /auth/2fa": TOTP_ONLY,
			"POST /auth/2fa": { body: make("MeBody") },
		});
		const p = props();
		const user = userEvent.setup();
		await renderWithProviders(<SecondFactorStep {...p} />);
		await screen.findByLabelText("Authenticator code");
		await user.click(
			screen.getByRole("button", { name: "Use a recovery code" }),
		);
		await user.type(
			screen.getByLabelText("Recovery code"),
			"ABCD-1234 abcd-1234",
		);
		await user.click(screen.getByRole("button", { name: "Verify" }));
		await waitFor(() => expect(p.onVerified).toHaveBeenCalled());
		expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
			code: "ABCD-1234abcd-1234",
		});
	});

	it("offers recovery codes only when the account has no authenticator app", async () => {
		stubApi({ "GET /auth/2fa": { body: { passkey: true, totp: false } } });
		await renderWithProviders(<SecondFactorStep {...props()} />);
		// Without WebAuthn support the passkey is unusable: the code form shows.
		expect(await screen.findByLabelText("Recovery code")).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: /authenticator code/i }),
		).toBeNull();
		expect(
			screen.queryByRole("button", { name: "Use a recovery code" }),
		).toBeNull();
		expect(
			screen.getByText(/Your passkeys cannot be used on this page/),
		).toBeInTheDocument();
	});
});

describe("SecondFactorStep: no sign-in pending", () => {
	it("hands back to the password step with the reason on a 401", async () => {
		stubApi({
			"GET /auth/2fa": {
				status: 401,
				body: { code: "no_pending_login", status: 401 },
			},
		});
		const p = props();
		await renderWithProviders(<SecondFactorStep {...p} />);
		await waitFor(() =>
			expect(p.onExpired).toHaveBeenCalledWith(
				"That sign-in expired. Go back and enter your password again.",
			),
		);
	});
});

describe("SecondFactorStep: passkey", () => {
	const BOTH = { body: { passkey: true, totp: true } };

	it("leads with the passkey when the account has one and the browser can use it", async () => {
		env = installPasskeys();
		stubApi({ "GET /auth/2fa": BOTH });
		await renderWithProviders(<SecondFactorStep {...props()} />);
		expect(
			await screen.findByRole("button", { name: "Use a passkey" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Use a code instead" }),
		).toBeInTheDocument();
		expect(screen.queryByLabelText("Authenticator code")).toBeNull();
	});

	it("verifies with the passkey through options, authenticator and the 2fa passkey call", async () => {
		env = installPasskeys();
		const api = stubApi({
			"GET /auth/2fa": BOTH,
			"POST /auth/2fa/passkey/options": { body: REQUEST_OPTIONS },
			"POST /auth/2fa/passkey": { body: make("MeBody") },
		});
		const p = props();
		await renderWithProviders(<SecondFactorStep {...p} />);
		await userEvent
			.setup()
			.click(await screen.findByRole("button", { name: "Use a passkey" }));
		await waitFor(() => expect(p.onVerified).toHaveBeenCalledTimes(1));
		expect(api.calls.at(-1)).toMatchObject({
			method: "POST",
			path: "/auth/2fa/passkey",
			body: FAKE_ASSERTION,
		});
		expect(api.unmatched).toEqual([]);
	});

	it("stays quiet when the prompt is closed", async () => {
		env = installPasskeys(dismissedPrompt);
		stubApi({
			"GET /auth/2fa": BOTH,
			"POST /auth/2fa/passkey/options": { body: REQUEST_OPTIONS },
		});
		const p = props();
		await renderWithProviders(<SecondFactorStep {...p} />);
		await userEvent
			.setup()
			.click(await screen.findByRole("button", { name: "Use a passkey" }));
		await waitFor(() => expect(env?.get).toHaveBeenCalled());
		await waitFor(() =>
			expect(
				screen.getByRole("button", { name: "Use a passkey" }),
			).toBeEnabled(),
		);
		expect(screen.queryByRole("alert")).toBeNull();
		expect(p.onVerified).not.toHaveBeenCalled();
	});

	it("explains a refused passkey", async () => {
		env = installPasskeys();
		stubApi({
			"GET /auth/2fa": BOTH,
			"POST /auth/2fa/passkey/options": { body: REQUEST_OPTIONS },
			"POST /auth/2fa/passkey": problem(401, { code: "passkey_failed" }),
		});
		await renderWithProviders(<SecondFactorStep {...props()} />);
		await userEvent
			.setup()
			.click(await screen.findByRole("button", { name: "Use a passkey" }));
		expect(await screen.findByRole("alert")).toHaveTextContent(
			"That passkey was not accepted",
		);
	});

	it("disables the passkey and offers only Back once the sign-in expired", async () => {
		env = installPasskeys();
		stubApi({
			"GET /auth/2fa": BOTH,
			"POST /auth/2fa/passkey/options": {
				status: 401,
				body: { code: "no_pending_login", status: 401 },
			},
		});
		const p = props();
		const user = userEvent.setup();
		await renderWithProviders(<SecondFactorStep {...p} />);
		await user.click(
			await screen.findByRole("button", { name: "Use a passkey" }),
		);
		expect(await screen.findByRole("alert")).toHaveTextContent(
			"That sign-in expired",
		);
		expect(
			screen.getByRole("button", { name: "Use a passkey" }),
		).toBeDisabled();
		expect(
			screen.queryByRole("button", { name: "Use a code instead" }),
		).toBeNull();
		await user.click(screen.getByRole("button", { name: "Back" }));
		expect(p.onBack).toHaveBeenCalledTimes(1);
	});

	it("moves to the code form and back to the passkey", async () => {
		env = installPasskeys();
		stubApi({ "GET /auth/2fa": BOTH });
		const user = userEvent.setup();
		await renderWithProviders(<SecondFactorStep {...props()} />);
		await user.click(
			await screen.findByRole("button", { name: "Use a code instead" }),
		);
		expect(screen.getByLabelText("Authenticator code")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Use a passkey" }));
		expect(
			screen.getByRole("button", { name: "Use a code instead" }),
		).toBeInTheDocument();
	});

	it("calls the code path a recovery code for an account with passkeys only", async () => {
		env = installPasskeys();
		stubApi({ "GET /auth/2fa": { body: { passkey: true, totp: false } } });
		const user = userEvent.setup();
		await renderWithProviders(<SecondFactorStep {...props()} />);
		await user.click(
			await screen.findByRole("button", { name: "Use a recovery code" }),
		);
		expect(screen.getByLabelText("Recovery code")).toBeInTheDocument();
	});

	it("shows the caller's notice above the passkey prompt", async () => {
		env = installPasskeys();
		stubApi({ "GET /auth/2fa": BOTH });
		await renderWithProviders(
			<SecondFactorStep {...props({ notice: "Verify again." })} />,
		);
		expect(await screen.findByText("Verify again.")).toBeInTheDocument();
	});
});
