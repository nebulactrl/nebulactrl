import { describe, expect, test } from "bun:test";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { SecurityBody, TeamSecurityBody } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, type Overrides, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { TeamRequirementCard } from "./team-requirement-card";

function mySecurity(over: Overrides<SecurityBody> = {}) {
	return make("SecurityBody", {
		totpEnabled: true,
		totpPending: false,
		passkeys: [],
		sessionMfa: false,
		hasPassword: true,
		identities: [],
		isInstanceAdmin: false,
		enrollmentNeeded: false,
		recoveryCodesLeft: 8,
		requiredByTeams: [],
		...over,
	});
}

function security(over: Overrides<TeamSecurityBody> = {}) {
	return make("TeamSecurityBody", {
		require2fa: false,
		enrolled: 2,
		members: 3,
		...over,
	});
}

const switchEl = () =>
	screen.getByRole("switch", {
		name: "Every member turns it on before acting in this team.",
	});

describe("TeamRequirementCard", () => {
	test("turning it on PUTs require2fa true, confirms and reloads the state", async () => {
		let current = security();
		const api = stubApi({
			"GET /teams/:teamId/security": () => Response.json(current),
			"GET /me/security": { body: mySecurity() },
			"PUT /teams/:teamId/security": (req) =>
				req.json().then((b: { require2fa: boolean }) => {
					current = security({ require2fa: b.require2fa });
					return Response.json(current);
				}),
		});
		const user = userEvent.setup();
		await renderWithProviders(<TeamRequirementCard teamId="t-1" />);

		await screen.findByText(/2 of 3 members have it on\./);
		expect(switchEl()).not.toBeChecked();
		await user.click(switchEl());

		expect(
			await screen.findByText("Two-step verification is now required"),
		).toBeInTheDocument();
		const put = api.calls.find((c) => c.method === "PUT");
		expect(put).toMatchObject({
			path: "/teams/t-1/security",
			body: { require2fa: true },
		});
		// The refetched state shows the switch on and who is waiting.
		expect(
			await screen.findByText(/1 is waiting to set it up\./),
		).toBeInTheDocument();
		expect(switchEl()).toBeChecked();
		expect(api.unmatched).toEqual([]);
	});

	test("turning it off says it is no longer required", async () => {
		let current = security({ require2fa: true, enrolled: 3 });
		const api = stubApi({
			"GET /teams/:teamId/security": () => Response.json(current),
			"GET /me/security": { body: mySecurity() },
			"PUT /teams/:teamId/security": () => {
				current = security({ require2fa: false, enrolled: 3 });
				return Response.json(current);
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<TeamRequirementCard teamId="t-1" />);

		await screen.findByText(/3 of 3 members have it on\./);
		expect(switchEl()).toBeChecked();
		await user.click(switchEl());

		expect(
			await screen.findByText("Two-step verification is no longer required"),
		).toBeInTheDocument();
		expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({
			require2fa: false,
		});
	});

	test("counts a single waiting member and a single member in the singular", async () => {
		stubApi({
			"GET /teams/:teamId/security": {
				body: security({ require2fa: true, enrolled: 0, members: 1 }),
			},
			"GET /me/security": { body: mySecurity() },
		});
		await renderWithProviders(<TeamRequirementCard teamId="t-1" />);
		expect(
			await screen.findByText(
				/0 of 1 member has it on; 1 is waiting to set it up\./,
			),
		).toBeInTheDocument();
	});

	test("several waiting members read in the plural", async () => {
		stubApi({
			"GET /teams/:teamId/security": {
				body: security({ require2fa: true, enrolled: 1, members: 4 }),
			},
			"GET /me/security": { body: mySecurity() },
		});
		await renderWithProviders(<TeamRequirementCard teamId="t-1" />);
		expect(
			await screen.findByText(/3 are waiting to set it up\./),
		).toBeInTheDocument();
	});

	test("without two-step on your own account the switch is locked and the way there is shown", async () => {
		const api = stubApi({
			"GET /teams/:teamId/security": { body: security() },
			"GET /me/security": {
				body: mySecurity({
					totpEnabled: false,
					passkeys: [],
					sessionMfa: false,
				}),
			},
		});
		await renderWithProviders(<TeamRequirementCard teamId="t-1" />);

		await screen.findByText(
			/Turn on two-step verification for your own account first/,
		);
		expect(switchEl()).toHaveAttribute("aria-disabled", "true");
		const link = screen.getByRole("link", {
			name: "Account settings → Security",
		});
		expect(link).toHaveAttribute("href", "/account/security");
		await userEvent.setup().click(switchEl());
		expect(api.calls.filter((c) => c.method === "PUT")).toEqual([]);
	});

	test.each([
		[
			"a passkey",
			{ totpEnabled: false, passkeys: [make("PasskeyBody", { id: "k" })] },
		],
		["a verified session", { totpEnabled: false, sessionMfa: true }],
	])("%s counts as your own two-step", async (_label, mine) => {
		stubApi({
			"GET /teams/:teamId/security": { body: security() },
			"GET /me/security": { body: mySecurity(mine) },
		});
		await renderWithProviders(<TeamRequirementCard teamId="t-1" />);
		await screen.findByText(/2 of 3 members have it on\./);
		expect(switchEl()).not.toHaveAttribute("aria-disabled", "true");
		expect(
			screen.queryByText(/Turn on two-step verification for your own account/),
		).not.toBeInTheDocument();
	});

	test("a team that already requires it can be switched off without own two-step", async () => {
		stubApi({
			"GET /teams/:teamId/security": { body: security({ require2fa: true }) },
			"GET /me/security": { body: mySecurity({ totpEnabled: false }) },
		});
		await renderWithProviders(<TeamRequirementCard teamId="t-1" />);
		await screen.findByText(/2 of 3 members have it on/);
		expect(switchEl()).not.toHaveAttribute("aria-disabled", "true");
		expect(screen.queryByText(/for your own account first/)).toBeNull();
	});

	test("shows the server's refusal (enrol_first) under the switch", async () => {
		stubApi({
			"GET /teams/:teamId/security": { body: security() },
			"GET /me/security": { body: mySecurity() },
			"PUT /teams/:teamId/security": problem(409, {
				code: "enrol_first",
				title: "Conflict",
				detail: "turn on two-step verification first",
			}),
		});
		const user = userEvent.setup();
		await renderWithProviders(<TeamRequirementCard teamId="t-1" />);
		await screen.findByText(/2 of 3 members have it on/);
		await user.click(switchEl());

		expect(
			await screen.findByText("turn on two-step verification first"),
		).toBeInTheDocument();
		expect(switchEl()).not.toBeChecked();
	});

	test("says the setting could not be loaded", async () => {
		stubApi({
			"GET /teams/:teamId/security": {
				status: 500,
				body: { status: 500, code: "internal", title: "Boom" },
			},
			"GET /me/security": { body: mySecurity() },
		});
		await renderWithProviders(<TeamRequirementCard teamId="t-1" />);
		expect(
			await screen.findByText("Could not load this team's setting."),
		).toBeInTheDocument();
		expect(screen.queryByRole("switch")).not.toBeInTheDocument();
	});
});
