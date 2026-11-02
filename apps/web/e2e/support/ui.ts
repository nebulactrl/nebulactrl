import { expect, type Locator, type Page } from "@playwright/test";
import { e2e } from "./env";

// Steps several flows share, written against what a person sees: names of
// buttons, tabs and dialogs.

/** Opens the projects page, creates a project and lands on its production canvas. */
export async function createProject(page: Page, name: string): Promise<void> {
	await page.goto("/");
	await page.getByRole("button", { name: "New project" }).first().click();
	await page.getByLabel("Name").fill(name);
	await page.getByRole("button", { name: "Create project" }).click();
	await expect(page).toHaveURL(/\/p\/[^/]+\/production/);
}

export interface ImageService {
	name: string;
	image: string;
	port?: number;
}

/**
 * Creates a Docker image service from the canvas' Create menu. The service is
 * staged in production; deploying it is `deployStaged`.
 */
export async function createImageService(
	page: Page,
	service: ImageService,
): Promise<void> {
	await page
		.locator("main")
		.getByRole("button", { name: "Create", exact: true })
		.first()
		.click();
	await page
		.getByRole("listbox", { name: "Create a service" })
		.getByRole("option", { name: /Docker image/ })
		.click();
	const dialog = page.getByRole("dialog");
	await dialog.getByLabel("Image").fill(service.image);
	await dialog.getByLabel("Name").fill(service.name);
	if (service.port !== undefined) {
		await dialog.getByLabel("Port").fill(String(service.port));
	}
	// With several servers the dialog asks where it runs; with one it is chosen.
	const target = dialog.getByRole("radio", { name: new RegExp(e2e.server) });
	if ((await target.count()) > 0) {
		await target.click();
		await expect(target).toBeChecked();
	}
	await dialog.getByRole("button", { name: "Create service" }).click();
	await expect(dialog).toBeHidden();
	await expect(serviceNode(page, service.name)).toBeVisible();
}

/** The canvas card of a service. */
export function serviceNode(page: Page, name: string): Locator {
	return page
		.locator("[data-service-id]")
		.filter({ has: page.getByText(name, { exact: true }) });
}

/** Opens the inspector of a service by pressing its card. */
export async function openService(page: Page, name: string): Promise<void> {
	await serviceNode(page, name).click();
	await expect(page.getByRole("dialog", { name })).toBeVisible();
}

/**
 * Presses the production Deploy key for everything staged: lifts the guard,
 * then presses Deploy. The toast "Deploying N services" confirms it was sent.
 */
export async function deployStaged(page: Page): Promise<void> {
	await page.getByRole("button", { name: "Lift production guard" }).click();
	await page.getByRole("button", { name: /^Deploy · \d+$/ }).click();
	await expect(page.getByText(/Deploying \d+ service/)).toBeVisible();
}

/** The card says Live (and the LED caption "Live · …" follows); a first image pull may need a longer `timeout`. */
export async function expectLive(
	page: Page,
	name: string,
	timeout = 180_000,
): Promise<void> {
	const node = serviceNode(page, name);
	await expect(node).toContainText("Live", { timeout });
	await expect(node.getByText(/^Live · /)).toBeVisible();
}

/** Adds a domain in the inspector's Settings → Networking (saved as staged at once). */
export async function addDomain(page: Page, domain: string): Promise<void> {
	await page.getByRole("tab", { name: /^Settings/ }).click();
	await page.getByRole("tab", { name: /^Networking/ }).click();
	await page.getByRole("textbox", { name: "New domain" }).fill(domain);
	await page.getByRole("button", { name: "Add domain" }).click();
	await expect(page.getByRole("list", { name: "Domains" })).toContainText(
		domain,
	);
}

/** Closes the service inspector. */
export async function closeInspector(page: Page): Promise<void> {
	await page.getByRole("button", { name: "Close inspector" }).click();
	await expect(
		page.getByRole("button", { name: "Close inspector" }),
	).toBeHidden();
}

/** Inspector, Variables tab: adds a plain variable (staged). */
export async function addVariable(
	page: Page,
	key: string,
	value: string,
): Promise<void> {
	await page.getByRole("tab", { name: /^Variables/ }).click();
	await page.getByRole("button", { name: "New Variable" }).click();
	const dialog = page.getByRole("dialog", { name: "New variable" });
	await dialog.getByLabel("Name", { exact: true }).fill(key);
	await dialog.getByLabel("Value").fill(value);
	await dialog.getByRole("button", { name: "Add variable" }).click();
	await expect(dialog).toBeHidden();
	await expect(
		page.getByRole("list", { name: "Service variables" }),
	).toContainText(key);
}

/** Inspector, Variables tab: changes the value of an existing variable (staged). */
export async function setVariable(
	page: Page,
	key: string,
	value: string,
): Promise<void> {
	await page.getByRole("tab", { name: /^Variables/ }).click();
	await page.getByRole("button", { name: `Actions for ${key}` }).click();
	await page.getByRole("menuitem", { name: "Edit" }).click();
	const dialog = page.getByRole("dialog", { name: `Edit ${key}`, exact: true });
	await dialog.getByLabel("Value").fill(value);
	await dialog.getByRole("button", { name: "Save variable" }).click();
	await expect(dialog).toBeHidden();
}

/** Presses and holds a hold-to-confirm button the way a person does. */
export async function hold(page: Page, name: string | RegExp): Promise<void> {
	const button = page.getByRole("button", { name });
	await button.hover();
	await page.mouse.down();
	await page.waitForTimeout(1600);
	await page.mouse.up();
}

/**
 * Opens a shell in a running service from its inspector's menu ("Open shell…"),
 * confirms the production guard by holding the button, and returns the live,
 * focused terminal. The inspector of the service must be open.
 */
export async function openShell(page: Page, service: string): Promise<Locator> {
	await page
		.getByRole("dialog", { name: service, exact: true })
		.getByRole("button", { name: `More actions for ${service}` })
		.click();
	await page.getByRole("menuitem", { name: "Open shell…" }).click();
	const sheet = page.getByRole("dialog", { name: `Shell · ${service}` });
	await expect(sheet).toBeVisible();
	await sheet.getByRole("button", { name: "Open in production…" }).click();
	await hold(page, /Hold to open terminal/);
	const terminal = sheet.getByRole("application", { name: "Terminal" });
	await expect(terminal).toBeVisible();
	await expect(terminal).toHaveAttribute("data-phase", "live", {
		timeout: 60_000,
	});
	await terminal.click();
	return terminal;
}

/** Types a line into the focused terminal and presses Enter. */
export async function typeLine(page: Page, line: string): Promise<void> {
	await page.keyboard.type(line);
	await page.keyboard.press("Enter");
}

/** Closes the shell panel; closing asks before it ends the session. */
export async function closeShell(page: Page, service: string): Promise<void> {
	const sheet = page.getByRole("dialog", { name: `Shell · ${service}` });
	await page.keyboard.press("Escape");
	await sheet.getByRole("button", { name: "Close" }).first().click();
	await page
		.getByRole("alertdialog")
		.getByRole("button", { name: "End session" })
		.click();
	await expect(sheet).toBeHidden();
}
