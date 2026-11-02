import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { allowSlowRoutes, ME, renderApp, SHELL_ROUTES } from "./route-harness";

allowSlowRoutes();

/** A fresh installation: setup is open until the POST succeeds. */
function freshInstall({
	tokenRequired = false,
	setup = () => Response.json({}),
}: {
	tokenRequired?: boolean;
	setup?: (body: unknown) => Response;
} = {}) {
	let done = false;
	return stubApi({
		...SHELL_ROUTES,
		"GET /setup": () =>
			Response.json({
				needsSetup: !done,
				tokenRequired: tokenRequired && !done,
			}),
		"GET /me": () =>
			done
				? Response.json(ME)
				: Response.json({ code: "unauthorized" }, { status: 401 }),
		"POST /setup": async (request) => {
			const answer = setup(await request.json());
			if (answer.ok) done = true;
			return answer;
		},
		"POST /auth/login": () => Response.json({}),
	});
}

const field = (label: string) => screen.findByLabelText(label);

async function fillIn(name: string, email: string, password: string) {
	const user = userEvent.setup();
	await user.type(await field("Your name"), name);
	await user.type(screen.getByLabelText("Email"), email);
	await user.type(screen.getByLabelText("Password"), password);
}

describe("the setup page", () => {
	test("creates the administrator, signs in and lands on the projects page", async () => {
		const api = freshInstall();
		const { router } = await renderApp("/setup");
		await fillIn("  Ada Admin ", " ada@example.com ", "a-long-password");
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Create administrator" }));
		await waitFor(() => expect(router.state.location.pathname).toBe("/"));

		const post = api.calls.find(
			(c) => c.method === "POST" && c.path === "/setup",
		);
		expect(post?.body).toEqual({
			name: "Ada Admin",
			email: "ada@example.com",
			password: "a-long-password",
		});
		const login = api.calls.find((c) => c.path === "/auth/login");
		expect(login?.body).toEqual({
			email: "ada@example.com",
			password: "a-long-password",
		});
	});

	test("asks for the setup token when the installer required one, and sends it trimmed", async () => {
		const api = freshInstall({ tokenRequired: true });
		const { router } = await renderApp("/setup");
		const user = userEvent.setup();
		await user.type(await field("Setup token"), "  tok-123 ");
		await user.type(screen.getByLabelText("Your name"), "Ada");
		await user.type(screen.getByLabelText("Email"), "ada@example.com");
		await user.type(screen.getByLabelText("Password"), "a-long-password");
		await user.click(
			screen.getByRole("button", { name: "Create administrator" }),
		);
		await waitFor(() => expect(router.state.location.pathname).toBe("/"));
		const post = api.calls.find(
			(c) => c.method === "POST" && c.path === "/setup",
		);
		expect(post?.body).toMatchObject({ setupToken: "tok-123" });
	});

	test("does not ask for a token the installation does not need", async () => {
		freshInstall({ tokenRequired: false });
		await renderApp("/setup");
		await field("Your name");
		expect(screen.queryByLabelText("Setup token")).not.toBeInTheDocument();
	});

	test("takes the token from the setup link and clears it from the address", async () => {
		freshInstall({ tokenRequired: true });
		const { router } = await renderApp("/setup?token=from-the-link");
		expect(await field("Setup token")).toHaveValue("from-the-link");
		await waitFor(() => expect(router.state.location.search).toEqual({}));
		expect(router.state.location.pathname).toBe("/setup");
	});

	test("a password under ten characters is not sent", async () => {
		const api = freshInstall();
		await renderApp("/setup");
		await fillIn("Ada", "ada@example.com", "short");
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Create administrator" }));
		expect(
			api.calls.filter((c) => c.path === "/setup" && c.method === "POST"),
		).toEqual([]);
	});

	test("with a token required, an empty token is not sent", async () => {
		const api = freshInstall({ tokenRequired: true });
		await renderApp("/setup");
		await fillIn("Ada", "ada@example.com", "a-long-password");
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Create administrator" }));
		expect(
			api.calls.filter((c) => c.path === "/setup" && c.method === "POST"),
		).toEqual([]);
	});

	test.each([
		[
			409,
			"already_setup",
			"This installation already has an administrator. Sign in instead.",
		],
		[
			403,
			"setup_token_invalid",
			"The setup token is missing or wrong. Use the setup link the installer printed.",
		],
		[500, "internal", "The database is down."],
	])("a %i answer is explained on the page", async (status, code, text) => {
		freshInstall({
			setup: () =>
				Response.json(
					{ code, title: code, detail: "The database is down." },
					{ status },
				),
		});
		const { router } = await renderApp("/setup");
		await fillIn("Ada", "ada@example.com", "a-long-password");
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Create administrator" }));
		expect(await screen.findByText(text)).toBeInTheDocument();
		expect(router.state.location.pathname).toBe("/setup");
	});
});
