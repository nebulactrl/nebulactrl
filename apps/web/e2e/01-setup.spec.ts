import { execFileSync } from "node:child_process";
import { expect, type Page, test } from "@playwright/test";
import { list } from "./support/api";
import { AUTH_FILE, e2e } from "./support/env";

// Flow 1: first-run setup, sign-in, and the enrolled server online with facts.
//
// On a fresh stack (tests/e2e/run.sh) nothing exists yet: the first test
// creates the administrator on the setup page, signs out and in again with the
// form, and keeps the session for the other flows (AUTH_FILE). The second adds
// the server through "Add server": the install command carries the enrollment
// token, which the stack's launcher starts a real agent with, and the page
// shows the agent's facts once it connects. Against a stack that already has
// the account and the server (`make dev`), both steps just verify them.

test.describe.configure({ mode: "serial" });

async function signInWithForm(page: Page) {
	await page.goto("/login");
	await page.getByLabel("Email").fill(e2e.email);
	await page.getByLabel("Password").fill(e2e.password);
	await page.getByRole("button", { name: "Sign in", exact: true }).click();
	await expect(page.locator('[data-page="projects"]')).toBeVisible();
}

test("first-run setup creates the administrator, sign-out and sign-in work", async ({
	page,
}) => {
	const setup = await page.request.get("/api/v1/setup");
	const { needsSetup }: { needsSetup: boolean } = await setup.json();

	if (needsSetup) {
		await page.goto("/setup");
		await expect(
			page.getByRole("heading", { name: "Set up this installation" }),
		).toBeVisible();
		if (e2e.setupToken !== "") {
			await page.getByLabel("Setup token").fill(e2e.setupToken);
		}
		await page.getByLabel("Your name").fill("E2E Admin");
		await page.getByLabel("Email").fill(e2e.email);
		await page.getByLabel("Password").fill(e2e.password);
		await page.getByRole("button", { name: "Create administrator" }).click();
		await expect(page.locator('[data-page="projects"]')).toBeVisible();
		// A second setup is refused once the administrator exists.
		const again = await page.request.post("/api/v1/setup", {
			data: {
				name: "Intruder",
				email: "intruder@example.com",
				password: "another-long-password",
				setupToken: e2e.setupToken,
			},
		});
		expect(again.status()).toBe(409);
	} else {
		await signInWithForm(page);
	}

	// Sign out from the account menu: the dashboard is behind the login again.
	await page.getByRole("button", { name: /^Account menu for / }).click();
	await page.getByRole("menuitem", { name: "Sign out" }).click();
	await expect(page).toHaveURL(/\/login/);
	await page.goto("/");
	await expect(page).toHaveURL(/\/login/);

	// A wrong password is refused, the right one lets the same account in.
	await page.getByLabel("Email").fill(e2e.email);
	await page.getByLabel("Password").fill(`${e2e.password}-wrong`);
	await page.getByRole("button", { name: "Sign in", exact: true }).click();
	await expect(page.getByRole("alert")).toBeVisible();
	await expect(page).toHaveURL(/\/login/);
	await signInWithForm(page);

	await page.context().storageState({ path: AUTH_FILE });
});

test("the enrolled server shows online with its host facts", async ({
	page,
}) => {
	test.setTimeout(300_000);
	await signInWithForm(page);
	const existing = (await list(page.request, "/servers")).find(
		(s) => s.name === e2e.server,
	);

	if (!existing) {
		if (e2e.stack === "") {
			throw new Error(
				`no server named ${e2e.server}: start its agent (make dev-agent) or set E2E_SERVER`,
			);
		}
		await page.goto("/servers");
		await page.getByRole("button", { name: "Add server" }).first().click();
		const dialog = page.getByRole("dialog", { name: "Add a server" });
		await dialog.getByLabel("Name").fill(e2e.server);
		await dialog
			.getByRole("button", { name: "Create install command" })
			.click();
		const command = await dialog
			.getByRole("textbox", { name: "Install command" })
			.inputValue();
		const token = /nct_enroll_[A-Za-z0-9_-]+/.exec(command)?.[0];
		expect(token, "the install command carries an enrollment token").toBeTruthy();
		await expect(dialog.getByText("Waiting for the agent")).toBeVisible();

		// The launcher starts the real agent (a container on the stack's Docker
		// daemon) with that token, the way the install command would on a host.
		execFileSync(e2e.stack, ["agent", token ?? ""], { stdio: "inherit" });

		await expect(dialog.getByText("Agent connected")).toBeVisible({
			timeout: 120_000,
		});
		await expect(dialog.getByText(`${e2e.server} is online`)).toBeVisible();
		const facts = dialog.getByRole("status").filter({ hasText: "Docker" });
		await expect(facts).toContainText(/OS\s*\S+/);
		await expect(facts).toContainText(/CPUs\s*\d+/);
		await expect(facts).toContainText(/Docker\s*\d+\.\d+/);
		await dialog.getByRole("button", { name: "Done" }).click();
	}

	// The servers page lists it online, and its page shows the host's facts.
	await page.goto("/servers");
	const card = page.getByRole("link", { name: new RegExp(`^${e2e.server} `) });
	await expect(card).toContainText("Online");
	await expect(card).toContainText(/docker \d+\.\d+/);
	await card.click();
	await expect(page).toHaveURL(/\/servers\/[0-9a-f-]+/);
	await expect(page.getByRole("heading", { name: e2e.server })).toBeVisible();
	await expect(page.getByText("Online").first()).toBeVisible();
});
