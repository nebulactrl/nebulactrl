import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { getMySecurityQueryKey } from "#/api/gen/@tanstack/react-query.gen";
import type { PasskeyBody, SecurityBody } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders, testQueryClient } from "#/test/render";
import { TwoFactorCard } from "./two-factor-card";

const passkey: PasskeyBody = {
	id: "pk1",
	name: "MacBook",
	createdAt: "2026-01-01T00:00:00Z",
	synced: true,
	transports: null,
};

const security = (over: Partial<SecurityBody> = {}): SecurityBody => ({
	enrollmentNeeded: false,
	hasPassword: true,
	identities: null,
	isInstanceAdmin: false,
	passkeys: null,
	recoveryCodesLeft: 0,
	requiredByTeams: null,
	sessionMfa: false,
	totpEnabled: false,
	totpPending: false,
	...over,
});

const ACCOUNT = "ada@example.com";

describe("TwoFactorCard status", () => {
	test("with no factor it is Off, explains why to add one and offers Turn on only", async () => {
		await renderWithProviders(
			<TwoFactorCard security={security()} account={ACCOUNT} primary />,
		);
		expect(screen.getByText("Off")).toBeInTheDocument();
		expect(
			screen.getByText(/Add a second step to signing in/),
		).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Turn on" })).toBeEnabled();
		expect(
			screen.queryByRole("button", { name: "New recovery codes" }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: /Turn off/ }),
		).not.toBeInTheDocument();
		// Nothing about recovery codes without a factor.
		expect(screen.queryByText(/recovery code/i)).not.toBeInTheDocument();
	});

	test("with an authenticator app it is On and offers new codes and Turn off", async () => {
		await renderWithProviders(
			<TwoFactorCard
				security={security({ totpEnabled: true, recoveryCodesLeft: 8 })}
				account={ACCOUNT}
				primary={false}
			/>,
		);
		expect(screen.getByText("On")).toBeInTheDocument();
		expect(
			screen.getByText(/6-digit code from your authenticator app/),
		).toBeInTheDocument();
		expect(screen.getByText("8 recovery codes left.")).toBeInTheDocument();
		expect(screen.queryByRole("alert")).not.toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "New recovery codes" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Turn off authenticator app" }),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Turn on" }),
		).not.toBeInTheDocument();
	});

	test("a passkey alone counts as a factor and the app can still be added", async () => {
		await renderWithProviders(
			<TwoFactorCard
				security={security({ passkeys: [passkey], recoveryCodesLeft: 10 })}
				account={ACCOUNT}
				primary={false}
			/>,
		);
		expect(screen.getByText("On")).toBeInTheDocument();
		expect(
			screen.getByText(/asks for one of your passkeys after your password/),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Add authenticator app" }),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: /Turn off/ }),
		).not.toBeInTheDocument();
	});

	test("warns when no recovery code is left", async () => {
		await renderWithProviders(
			<TwoFactorCard
				security={security({ totpEnabled: true, recoveryCodesLeft: 0 })}
				account={ACCOUNT}
				primary={false}
			/>,
		);
		expect(screen.getByText(/no recovery codes left/)).toBeInTheDocument();
		expect(
			screen.queryByText("0 recovery codes left."),
		).not.toBeInTheDocument();
	});

	test("warns with the exact count when only a few codes are left", async () => {
		const one = await renderWithProviders(
			<TwoFactorCard
				security={security({ totpEnabled: true, recoveryCodesLeft: 1 })}
				account={ACCOUNT}
				primary={false}
			/>,
		);
		expect(
			screen.getByText("Only 1 recovery code is left. Create new ones soon."),
		).toBeInTheDocument();
		one.unmount();
		await renderWithProviders(
			<TwoFactorCard
				security={security({ totpEnabled: true, recoveryCodesLeft: 3 })}
				account={ACCOUNT}
				primary={false}
			/>,
		);
		expect(
			screen.getByText("Only 3 recovery codes are left. Create new ones soon."),
		).toBeInTheDocument();
	});

	test("names the teams that require two-step verification", async () => {
		await renderWithProviders(
			<TwoFactorCard
				security={security({
					requiredByTeams: [
						{ id: "t1", name: "Platform" },
						{ id: "t2", name: "Data" },
					],
				})}
				account={ACCOUNT}
				primary
			/>,
		);
		expect(screen.getByText("Platform, Data")).toBeInTheDocument();
		expect(screen.getByText(/Required by/)).toBeInTheDocument();
	});

	test("says so when the session was verified by the identity provider and no factor is set", async () => {
		await renderWithProviders(
			<TwoFactorCard
				security={security({ sessionMfa: true })}
				account={ACCOUNT}
				primary
			/>,
		);
		expect(
			screen.getByText("This session was verified by your identity provider."),
		).toBeInTheDocument();
	});

	test("does not claim IdP verification once a factor exists", async () => {
		await renderWithProviders(
			<TwoFactorCard
				security={security({
					sessionMfa: true,
					totpEnabled: true,
					recoveryCodesLeft: 10,
				})}
				account={ACCOUNT}
				primary={false}
			/>,
		);
		expect(
			screen.queryByText(/verified by your identity provider/),
		).not.toBeInTheDocument();
	});
});

describe("TwoFactorCard actions", () => {
	test("Turn on runs the enrolment: setup, verify the code, keep the recovery codes", async () => {
		const api = stubApi({
			"POST /me/2fa/setup": {
				body: {
					secret: "JBSWY3DPEHPK3PXP",
					uri: "otpauth://totp/NebulaCtrl:ada?secret=JBSWY3DPEHPK3PXP",
				},
			},
			"POST /me/2fa/enable": { body: { recoveryCodes: ["r1r1-aaaa"] } },
		});
		const queryClient = testQueryClient();
		queryClient.setQueryData(getMySecurityQueryKey(), { totpEnabled: false });
		const user = userEvent.setup();
		await renderWithProviders(
			<TwoFactorCard security={security()} account={ACCOUNT} primary />,
			{ queryClient },
		);
		await user.click(screen.getByRole("button", { name: "Turn on" }));

		expect(await screen.findByTestId("totp-secret")).toHaveTextContent(
			"JBSW Y3DP EHPK 3PXP",
		);
		const verify = screen.getByRole("button", { name: "Verify and turn on" });
		expect(verify).toBeDisabled();
		await user.type(screen.getByLabelText(/Enter the 6-digit code/), "123456");
		await user.click(verify);

		expect(await screen.findByText("r1r1-aaaa")).toBeInTheDocument();
		expect(api.calls.map((c) => `${c.method} ${c.path}`)).toEqual([
			"POST /me/2fa/setup",
			"POST /me/2fa/enable",
		]);
		expect(api.calls[1]?.body).toEqual({ code: "123456" });

		await user.click(
			screen.getByRole("checkbox", {
				name: "I stored these codes somewhere safe",
			}),
		);
		await user.click(screen.getByRole("button", { name: "Done" }));
		await waitFor(() =>
			expect(
				screen.queryByText("Turn on two-factor authentication"),
			).not.toBeInTheDocument(),
		);
		expect(
			queryClient.getQueryState(getMySecurityQueryKey())?.isInvalidated,
		).toBe(true);
	});

	test("Cancel in the enrolment dialog closes it", async () => {
		stubApi({
			"POST /me/2fa/setup": {
				body: { secret: "ABCDEFGH", uri: "otpauth://x" },
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<TwoFactorCard security={security()} account={ACCOUNT} primary />,
		);
		await user.click(screen.getByRole("button", { name: "Turn on" }));
		await screen.findByTestId("totp-secret");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		await waitFor(() =>
			expect(
				screen.queryByText("Turn on two-factor authentication"),
			).not.toBeInTheDocument(),
		);
	});

	test("Add authenticator app opens the same enrolment", async () => {
		const api = stubApi({
			"POST /me/2fa/setup": {
				body: { secret: "ABCDEFGH", uri: "otpauth://x" },
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<TwoFactorCard
				security={security({ passkeys: [passkey], recoveryCodesLeft: 10 })}
				account={ACCOUNT}
				primary={false}
			/>,
		);
		await user.click(
			screen.getByRole("button", { name: "Add authenticator app" }),
		);
		expect(await screen.findByTestId("totp-secret")).toHaveTextContent(
			"ABCD EFGH",
		);
		expect(api.calls.map((c) => c.path)).toEqual(["/me/2fa/setup"]);
	});

	test("Turn off needs the code and password, sends both, and names the requiring teams", async () => {
		const api = stubApi({ "POST /me/2fa/disable": { status: 204 } });
		const user = userEvent.setup();
		await renderWithProviders(
			<TwoFactorCard
				security={security({
					totpEnabled: true,
					recoveryCodesLeft: 10,
					requiredByTeams: [{ id: "t1", name: "Platform" }],
				})}
				account={ACCOUNT}
				primary={false}
			/>,
		);
		await user.click(
			screen.getByRole("button", { name: "Turn off authenticator app" }),
		);
		const dialog = await screen.findByRole("dialog");
		expect(within(dialog).getByText(/Platform\s+requires it/)).toBeVisible();
		await user.type(
			within(dialog).getByLabelText("Authenticator or recovery code"),
			"111222",
		);
		await user.type(within(dialog).getByLabelText("Password"), "hunter2");
		await user.click(within(dialog).getByRole("button", { name: "Turn off" }));
		expect(
			await screen.findByText("Two-step verification is off"),
		).toBeInTheDocument();
		expect(api.calls[0]?.body).toEqual({ code: "111222", password: "hunter2" });
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);
	});

	test("New recovery codes asks for an authenticator code when the app is on", async () => {
		const api = stubApi({
			"POST /me/2fa/recovery-codes": { body: { recoveryCodes: ["n1n1-bbbb"] } },
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<TwoFactorCard
				security={security({ totpEnabled: true, recoveryCodesLeft: 2 })}
				account={ACCOUNT}
				primary={false}
			/>,
		);
		await user.click(
			screen.getByRole("button", { name: "New recovery codes" }),
		);
		const dialog = await screen.findByRole("dialog");
		expect(within(dialog).queryByLabelText("Password")).not.toBeInTheDocument();
		await user.type(
			within(dialog).getByLabelText("Authenticator or recovery code"),
			"999 888",
		);
		await user.click(
			within(dialog).getByRole("button", { name: "Create new codes" }),
		);
		expect(await screen.findByText("n1n1-bbbb")).toBeInTheDocument();
		expect(api.calls[0]?.body).toEqual({ code: "999888" });
	});

	test("New recovery codes asks for the password on a passkey-only account", async () => {
		const api = stubApi({
			"POST /me/2fa/recovery-codes": { body: { recoveryCodes: ["p1p1-cccc"] } },
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<TwoFactorCard
				security={security({ passkeys: [passkey], recoveryCodesLeft: 0 })}
				account={ACCOUNT}
				primary={false}
			/>,
		);
		await user.click(
			screen.getByRole("button", { name: "New recovery codes" }),
		);
		const dialog = await screen.findByRole("dialog");
		expect(
			within(dialog).queryByLabelText("Authenticator or recovery code"),
		).not.toBeInTheDocument();
		await user.type(within(dialog).getByLabelText("Password"), "hunter2");
		await user.click(
			within(dialog).getByRole("button", { name: "Create new codes" }),
		);
		expect(await screen.findByText("p1p1-cccc")).toBeInTheDocument();
		expect(api.calls[0]?.body).toEqual({ password: "hunter2" });
	});
});
