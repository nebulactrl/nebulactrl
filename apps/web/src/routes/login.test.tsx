import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { problem as apiProblem, make } from "#/test/factories";
import { allowSlowRoutes, ME, renderApp, SHELL_ROUTES } from "./route-harness";

const problem = (status: number, code: string, detail?: string) =>
	apiProblem(status, { code, title: code, ...(detail ? { detail } : {}) });

/**
 * A control plane with nobody signed in until the login call succeeds. `login`
 * decides the answer to POST /auth/login.
 */
function signedOutApi(
	login: (body: unknown) => Response | { status: number; body: unknown },
	extra: Parameters<typeof stubApi>[0] = {},
) {
	let signedIn = false;
	return stubApi({
		...SHELL_ROUTES,
		"GET /me": () =>
			signedIn
				? Response.json(ME)
				: Response.json(
						{ code: "unauthorized", title: "Unauthorized" },
						{ status: 401 },
					),
		"POST /auth/login": async (request) => {
			const answer = login(await request.json());
			if (answer instanceof Response) {
				if (answer.ok) signedIn = true;
				return answer;
			}
			return Response.json(answer.body, { status: answer.status });
		},
		"GET /auth/providers": { body: { providers: [] } },
		"GET /auth/password-reset": { body: { available: true } },
		...extra,
	});
}

allowSlowRoutes();

async function fillAndSubmit(email: string, password: string) {
	const user = userEvent.setup();
	await user.type(await screen.findByLabelText("Email"), email);
	await user.type(screen.getByLabelText("Password"), password);
	await user.click(screen.getByRole("button", { name: "Sign in" }));
}

describe("the sign-in page", () => {
	test("signs in with the trimmed email and lands on the page that asked for a session", async () => {
		const api = signedOutApi(() => Response.json({ ok: true }), {
			"GET /environments/:id/services": { body: { items: [] } },
		});
		const { router } = await renderApp("/login?returnTo=%2Fservers");
		await fillAndSubmit("  ada@example.com ", "correct horse");
		await waitFor(() =>
			expect(router.state.location.pathname).toBe("/servers"),
		);
		const login = api.calls.find((c) => c.path === "/auth/login");
		expect(login?.method).toBe("POST");
		expect(login?.body).toEqual({
			email: "ada@example.com",
			password: "correct horse",
		});
	});

	test("without a returnTo it lands on the projects page", async () => {
		signedOutApi(() => Response.json({ ok: true }));
		const { router } = await renderApp("/login");
		await fillAndSubmit("ada@example.com", "pw");
		await waitFor(() => expect(router.state.location.pathname).toBe("/"));
	});

	test("a returnTo that points off this site is ignored", async () => {
		signedOutApi(() => Response.json({ ok: true }));
		const { router } = await renderApp(
			"/login?returnTo=%2F%2Fevil.example.com",
		);
		await fillAndSubmit("ada@example.com", "pw");
		await waitFor(() => expect(router.state.location.pathname).toBe("/"));
	});

	test("wrong credentials say so and stay on the page", async () => {
		signedOutApi(() => problem(401, "invalid_credentials", "bad"));
		const { router } = await renderApp("/login");
		await fillAndSubmit("ada@example.com", "nope");
		expect(
			await screen.findByText("That email and password do not match."),
		).toBeInTheDocument();
		expect(router.state.location.pathname).toBe("/login");
	});

	test("too many attempts tells the person to wait", async () => {
		signedOutApi(() => problem(429, "rate_limited"));
		await renderApp("/login");
		await fillAndSubmit("ada@example.com", "pw");
		expect(
			await screen.findByText(
				"Too many sign-in attempts. Wait a minute, then try again.",
			),
		).toBeInTheDocument();
	});

	test("any other failure shows the server's own message", async () => {
		signedOutApi(() => problem(500, "internal", "The database is down."));
		await renderApp("/login");
		await fillAndSubmit("ada@example.com", "pw");
		expect(
			await screen.findByText("The database is down."),
		).toBeInTheDocument();
	});

	test("an account with two-step verification is asked for its code, and Back returns to the password", async () => {
		signedOutApi(() => problem(401, "two_factor_required"), {
			"GET /auth/2fa": {
				body: make("SecondFactorMethodsOutputBody", {
					passkey: false,
					totp: true,
				}),
			},
		});
		await renderApp("/login");
		await fillAndSubmit("ada@example.com", "pw");
		expect(
			await screen.findByLabelText(/authenticator code/i),
		).toBeInTheDocument();
		expect(
			screen.getByRole("heading", { name: "Two-step verification" }),
		).toBeInTheDocument();
		expect(
			screen.queryByText("That email and password do not match."),
		).not.toBeInTheDocument();
		await userEvent
			.setup()
			.click(await screen.findByRole("button", { name: /back/i }));
		expect(
			await screen.findByRole("heading", { name: "Sign in" }),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Password")).toHaveValue("");
	});

	test("coming back from a provider with a code still due opens the second step directly", async () => {
		signedOutApi(() => Response.json({}), {
			"GET /auth/2fa": {
				body: make("SecondFactorMethodsOutputBody", {
					passkey: false,
					totp: true,
				}),
			},
		});
		await renderApp("/login?step=2fa");
		expect(
			await screen.findByLabelText(/authenticator code/i),
		).toBeInTheDocument();
		expect(screen.queryByLabelText("Password")).not.toBeInTheDocument();
	});

	test("a second step with nothing pending returns to the password with the reason", async () => {
		signedOutApi(() => Response.json({}), {
			"GET /auth/2fa": problem(401, "no_pending_login"),
		});
		await renderApp("/login?step=2fa");
		expect(
			await screen.findByRole("heading", { name: "Sign in" }),
		).toBeInTheDocument();
		expect(await screen.findByText(/sign-in/i)).toBeInTheDocument();
	});

	test("an identity-provider failure is explained from the address", async () => {
		signedOutApi(() => Response.json({}));
		await renderApp("/login?error=oidc_denied");
		expect(
			await screen.findByText(
				"The identity provider did not grant access, so nobody was signed in.",
			),
		).toBeInTheDocument();
	});

	test("an error code that is an Object property name does not crash the page", async () => {
		signedOutApi(() => Response.json({}));
		await renderApp("/login?error=__proto__");
		expect(await screen.findByLabelText("Email")).toBeInTheDocument();
	});

	test("opening /setup after setup explains it on the sign-in page", async () => {
		signedOutApi(() => Response.json({}));
		await renderApp("/setup");
		expect(
			await screen.findByText(
				"This installation is already set up. Sign in instead.",
			),
		).toBeInTheDocument();
	});

	test("a server error replaces the redirect error and the notice", async () => {
		signedOutApi(() => problem(401, "invalid_credentials"));
		await renderApp("/login?error=oidc_denied");
		await fillAndSubmit("ada@example.com", "nope");
		expect(
			await screen.findByText("That email and password do not match."),
		).toBeInTheDocument();
		expect(screen.queryByText(/did not grant access/)).not.toBeInTheDocument();
	});

	test("offers a reset link when reset by email is available", async () => {
		signedOutApi(() => Response.json({}));
		await renderApp("/login");
		const link = await screen.findByRole("link", {
			name: "Forgot your password?",
		});
		expect(link).toHaveAttribute("href", "/forgot-password");
	});

	test("without reset by email it tells the person to ask an admin", async () => {
		signedOutApi(() => Response.json({}), {
			"GET /auth/password-reset": { body: { available: false } },
		});
		await renderApp("/login");
		expect(
			await screen.findByText(/Ask an admin to send you a reset link\./),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("link", { name: "Forgot your password?" }),
		).not.toBeInTheDocument();
	});

	test("lists each single-sign-on provider as a link to its start address", async () => {
		signedOutApi(() => Response.json({}), {
			"GET /auth/providers": {
				body: {
					providers: [
						{ id: "okta", name: "Okta", kind: "oidc" },
						{ id: "gh", name: "GitHub", kind: "github" },
					],
				},
			},
		});
		await renderApp("/login");
		expect(
			await screen.findByRole("button", { name: /Continue with Okta/ }),
		).toHaveAttribute("href", "/api/v1/auth/oidc/okta/start");
		expect(
			screen.getByRole("button", { name: /Continue with GitHub/ }),
		).toHaveAttribute("href", "/api/v1/auth/oidc/gh/start");
	});

	test("a second submit while signing in does not send a second request", async () => {
		const gate = Promise.withResolvers<Response>();
		const api = stubApi({
			...SHELL_ROUTES,
			"GET /me": problem(401, "unauthorized"),
			"GET /auth/providers": { body: { providers: [] } },
			"GET /auth/password-reset": { body: { available: true } },
			"POST /auth/login": () => gate.promise,
		});
		await renderApp("/login");
		await fillAndSubmit("ada@example.com", "pw");
		const logins = () => api.calls.filter((c) => c.path === "/auth/login");
		await waitFor(() => expect(logins()).toHaveLength(1));
		await userEvent.setup().type(screen.getByLabelText("Password"), "{Enter}");
		gate.resolve(
			Response.json({ code: "invalid_credentials" }, { status: 401 }),
		);
		expect(
			await screen.findByText("That email and password do not match."),
		).toBeInTheDocument();
		expect(logins()).toHaveLength(1);
	});
});
