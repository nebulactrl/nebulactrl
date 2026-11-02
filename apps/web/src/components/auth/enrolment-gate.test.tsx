import { afterEach, describe, expect, it } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { EnrolmentGate } from "./enrolment-gate";
import { installPasskeys } from "./passkey-env.testkit";

let env: ReturnType<typeof installPasskeys> | undefined;
afterEach(() => env?.restore());

const SECURITY = {
	enrollmentNeeded: true,
	hasPassword: true,
	requiredByTeams: [{ id: "t1", name: "Platform" }],
	identities: [],
	passkeys: [],
	isInstanceAdmin: false,
	recoveryCodesLeft: 0,
	sessionMfa: false,
	totpEnabled: false,
	totpPending: false,
};

const ME = make("MeBody", {
	email: "robin@example.com",
	name: "Robin",
	id: "u1",
});

function stub(security: object, extra: Parameters<typeof stubApi>[0] = {}) {
	return stubApi({
		"GET /me/security": { body: security },
		"GET /me": { body: ME },
		"POST /me/2fa/setup": {
			body: {
				secret: "JBSWY3DPEHPK3PXP",
				uri: "otpauth://totp/x?secret=JBSWY3DPEHPK3PXP",
			},
		},
		...extra,
	});
}

describe("EnrolmentGate", () => {
	it("lets the page through when no team requires two-factor", async () => {
		stub({ ...SECURITY, enrollmentNeeded: false });
		await renderWithProviders(
			<EnrolmentGate>
				<p>the page</p>
			</EnrolmentGate>,
		);
		expect(await screen.findByText("the page")).toBeInTheDocument();
		expect(screen.queryByText(/Set up two-factor/)).toBeNull();
	});

	it("lets the page through while the status is loading or unavailable", async () => {
		stubApi({
			"GET /me/security": {
				status: 500,
				body: { code: "internal", status: 500 },
			},
		});
		await renderWithProviders(
			<EnrolmentGate>
				<p>the page</p>
			</EnrolmentGate>,
		);
		expect(screen.getByText("the page")).toBeInTheDocument();
	});

	it("replaces the page with the setup flow and names the teams that require it", async () => {
		stub({
			...SECURITY,
			requiredByTeams: [
				{ id: "t1", name: "Platform" },
				{ id: "t2", name: "Data" },
			],
		});
		await renderWithProviders(
			<EnrolmentGate>
				<p>the page</p>
			</EnrolmentGate>,
		);
		expect(
			await screen.findByRole("heading", {
				name: "Set up two-factor authentication to continue",
			}),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				/Platform, Data requires every member to sign in with a second step/,
			),
		).toBeInTheDocument();
		expect(screen.queryByText("the page")).toBeNull();
		expect(await screen.findByTestId("totp-secret")).toBeInTheDocument();
	});

	it("uses a generic sentence when no team is named", async () => {
		stub({ ...SECURITY, requiredByTeams: null });
		await renderWithProviders(
			<EnrolmentGate>
				<p>the page</p>
			</EnrolmentGate>,
		);
		expect(
			await screen.findByText(
				/This team requires every member to sign in with a second step/,
			),
		).toBeInTheDocument();
	});

	it("shows the recovery codes after enabling, with Continue locked until they are stored", async () => {
		stub(SECURITY, {
			"POST /me/2fa/enable": {
				body: { recoveryCodes: ["aaaa-bbbb-cccc-dddd"] },
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<EnrolmentGate>
				<p>the page</p>
			</EnrolmentGate>,
		);
		await screen.findByTestId("totp-secret");
		await waitFor(() =>
			expect(screen.getByLabelText(/Enter the 6-digit code/)).toBeEnabled(),
		);
		await user.type(screen.getByLabelText(/Enter the 6-digit code/), "123456");
		await user.click(
			screen.getByRole("button", { name: "Verify and turn on" }),
		);
		expect(
			await screen.findByRole("button", { name: "Continue" }),
		).toBeDisabled();
	});

	it("reloads everything that was refused once the codes are stored", async () => {
		const api = stub(SECURITY, {
			"POST /me/2fa/enable": {
				body: { recoveryCodes: ["aaaa-bbbb-cccc-dddd"] },
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<EnrolmentGate>
				<p>the page</p>
			</EnrolmentGate>,
		);
		await screen.findByTestId("totp-secret");
		await user.type(screen.getByLabelText(/Enter the 6-digit code/), "123456");
		await user.click(
			screen.getByRole("button", { name: "Verify and turn on" }),
		);
		await user.click(
			await screen.findByRole("checkbox", { name: /I stored these codes/ }),
		);
		const before = api.calls.filter((c) => c.path === "/me/security").length;
		await user.click(screen.getByRole("button", { name: "Continue" }));
		await waitFor(() =>
			expect(
				api.calls.filter((c) => c.path === "/me/security").length,
			).toBeGreaterThan(before),
		);
	});

	it("offers no passkey card where the browser cannot use passkeys", async () => {
		stub(SECURITY);
		await renderWithProviders(
			<EnrolmentGate>
				<p>the page</p>
			</EnrolmentGate>,
		);
		await screen.findByTestId("totp-secret");
		expect(screen.queryByRole("button", { name: "Add a passkey" })).toBeNull();
	});

	it("offers a passkey as the second step where the browser can use one", async () => {
		env = installPasskeys();
		stub(SECURITY);
		const user = userEvent.setup();
		await renderWithProviders(
			<EnrolmentGate>
				<p>the page</p>
			</EnrolmentGate>,
		);
		await user.click(
			await screen.findByRole("button", { name: "Add a passkey" }),
		);
		expect(
			await screen.findByRole("dialog", { name: "Add a passkey" }),
		).toBeInTheDocument();
	});

	it("hides the passkey card when the server cannot do passkeys", async () => {
		env = installPasskeys();
		stub({ ...SECURITY, passkeysUnavailable: "No public URL set." });
		await renderWithProviders(
			<EnrolmentGate>
				<p>the page</p>
			</EnrolmentGate>,
		);
		await screen.findByTestId("totp-secret");
		expect(screen.queryByRole("button", { name: "Add a passkey" })).toBeNull();
	});
});
