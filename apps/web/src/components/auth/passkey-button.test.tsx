import { afterEach, describe, expect, it, jest } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { PasskeyLoginButton } from "./passkey-button";
import {
	dismissedPrompt,
	FAKE_ASSERTION,
	FakePublicKeyCredential,
	installPasskeys,
	REQUEST_OPTIONS,
} from "./passkey-env.testkit";

let env: ReturnType<typeof installPasskeys> | undefined;
afterEach(() => env?.restore());

const signIn = () =>
	screen.getByRole("button", { name: "Sign in with a passkey" });

describe("PasskeyLoginButton", () => {
	it("renders nothing where WebAuthn is unavailable", async () => {
		const { container } = await renderWithProviders(
			<PasskeyLoginButton onSignedIn={() => {}} />,
		);
		expect(container).not.toHaveTextContent("passkey");
		expect(screen.queryByRole("button", { name: /passkey/ })).toBeNull();
	});

	it("runs the ceremony: options, authenticator, login, then onSignedIn", async () => {
		env = installPasskeys();
		const api = stubApi({
			"POST /auth/passkey/options": { body: REQUEST_OPTIONS },
			"POST /auth/passkey/login": { body: make("MeBody") },
		});
		const onSignedIn = jest.fn();
		await renderWithProviders(<PasskeyLoginButton onSignedIn={onSignedIn} />);
		await userEvent.setup().click(signIn());
		await waitFor(() => expect(onSignedIn).toHaveBeenCalledTimes(1));
		expect(api.calls.map((c) => `${c.method} ${c.path}`)).toEqual([
			"POST /auth/passkey/options",
			"POST /auth/passkey/login",
		]);
		expect(api.calls[1]?.body).toEqual(FAKE_ASSERTION);
		expect(api.unmatched).toEqual([]);
		expect(screen.queryByRole("alert")).toBeNull();
	});

	it("disables the button while the ceremony is pending", async () => {
		let release: (value: unknown) => void = () => {};
		env = installPasskeys(() => new Promise((resolve) => (release = resolve)));
		stubApi({
			"POST /auth/passkey/options": { body: REQUEST_OPTIONS },
			"POST /auth/passkey/login": { body: make("MeBody") },
		});
		await renderWithProviders(<PasskeyLoginButton onSignedIn={() => {}} />);
		await userEvent.setup().click(signIn());
		await waitFor(() => expect(env?.get).toHaveBeenCalled());
		expect(signIn()).toBeDisabled();
		release(new FakePublicKeyCredential());
		await waitFor(() => expect(signIn()).toBeEnabled(), { timeout: 10_000 });
	}, 20_000);

	it("says nothing when the person closes the prompt themselves", async () => {
		env = installPasskeys(dismissedPrompt);
		const api = stubApi({
			"POST /auth/passkey/options": { body: REQUEST_OPTIONS },
		});
		const onSignedIn = jest.fn();
		await renderWithProviders(<PasskeyLoginButton onSignedIn={onSignedIn} />);
		await userEvent.setup().click(signIn());
		await waitFor(() => expect(api.calls).toHaveLength(1));
		await waitFor(() => expect(signIn()).toBeEnabled());
		expect(screen.queryByRole("alert")).toBeNull();
		expect(onSignedIn).not.toHaveBeenCalled();
	});

	it("explains a passkey the server refused and lets the person retry", async () => {
		env = installPasskeys();
		stubApi({
			"POST /auth/passkey/options": { body: REQUEST_OPTIONS },
			"POST /auth/passkey/login": problem(401, {
				code: "passkey_failed",
				title: "no",
			}),
		});
		const onSignedIn = jest.fn();
		await renderWithProviders(<PasskeyLoginButton onSignedIn={onSignedIn} />);
		await userEvent.setup().click(signIn());
		expect(await screen.findByRole("alert")).toHaveTextContent(
			"That passkey was not accepted. Try another one, or use your password.",
		);
		expect(onSignedIn).not.toHaveBeenCalled();
		expect(signIn()).toBeEnabled();
	});

	it("shows the server's message when passkeys are unavailable", async () => {
		env = installPasskeys();
		stubApi({
			"POST /auth/passkey/options": {
				status: 503,
				body: {
					code: "passkeys_unavailable",
					detail: "Set the public URL first.",
					status: 503,
				},
			},
		});
		await renderWithProviders(<PasskeyLoginButton onSignedIn={() => {}} />);
		await userEvent.setup().click(signIn());
		expect(await screen.findByRole("alert")).toHaveTextContent(
			"Set the public URL first.",
		);
	});
});
