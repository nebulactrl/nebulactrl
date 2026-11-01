import {
	type APIRequestContext,
	expect,
	type Page,
	test,
} from "@playwright/test";

// Smoke flow against a running dev stack (`make dev` + the agent1 dev profile).
//
//   E2E_EMAIL / E2E_PASSWORD  admin credentials (setup creates them on a fresh DB)
//   E2E_SERVER                name of the online server to place the service on
//                             (default "agent1"; the agent enrolled with
//                             E2E_ENROLL_TOKEN, which is single-use and only needed
//                             when bringing that agent up)

const EMAIL = process.env.E2E_EMAIL ?? "admin@example.com";
const PASSWORD = process.env.E2E_PASSWORD ?? "correct-horse-battery";
const SERVER = process.env.E2E_SERVER ?? "agent1";
const SECRET_VALUE = "sekret-canary-e2e-12345";

const rand = Math.random().toString(36).slice(2, 8);
const PROJECT_NAME = `e2e-${rand}`;
const SERVICE_NAME = `e2e-${rand}`;

interface Item {
	id: string;
	name: string;
	slug: string;
	status?: string;
}

async function list(request: APIRequestContext, path: string): Promise<Item[]> {
	const res = await request.get(`/api/v1${path}`);
	expect(res.ok(), `GET ${path}`).toBe(true);
	return ((await res.json()) as { items: Item[] }).items;
}

async function signIn(page: Page) {
	const setup = await page.request.get("/api/v1/setup");
	const { needsSetup } = (await setup.json()) as { needsSetup: boolean };
	if (needsSetup) {
		await page.goto("/setup");
		await page.getByLabel("Your name").fill("E2E Admin");
		await page.getByLabel("Email").fill(EMAIL);
		await page.getByLabel("Password").fill(PASSWORD);
		await page.getByRole("button", { name: /create|continue|set up/i }).click();
	} else {
		await page.goto("/login");
		await page.getByLabel("Email").fill(EMAIL);
		await page.getByLabel("Password").fill(PASSWORD);
		await page.getByRole("button", { name: "Sign in", exact: true }).click();
	}
	await expect(page.locator('[data-page="projects"]')).toBeVisible();
}

async function cleanup(page: Page) {
	const request = page.request;
	const project = (await list(request, "/projects")).find(
		(p) => p.name === PROJECT_NAME,
	);
	if (!project) return;
	for (const env of await list(
		request,
		`/projects/${project.id}/environments`,
	)) {
		for (const svc of await list(request, `/environments/${env.id}/services`)) {
			const res = await request.delete(
				`/api/v1/services/${svc.id}?confirm=${svc.slug}&deleteVolumes=true`,
			);
			expect(res.ok(), `delete service ${svc.slug}`).toBe(true);
		}
	}
	const res = await request.delete(`/api/v1/projects/${project.id}`);
	expect(res.ok(), `delete project ${project.name}`).toBe(true);
}

test("project to deployed service to staged secret", async ({ page }) => {
	try {
		await signIn(page);

		// The target server must be online before the service can run on it.
		await expect
			.poll(
				async () =>
					(await list(page.request, "/servers")).find((s) => s.name === SERVER)
						?.status,
				{ message: `server ${SERVER} online`, timeout: 30_000 },
			)
			.toBe("online");

		// Create a project; the dialog navigates to its production canvas.
		await page.getByRole("button", { name: "New project" }).first().click();
		await page.getByLabel("Name").fill(PROJECT_NAME);
		await page.getByRole("button", { name: "Create project" }).click();
		await expect(page).toHaveURL(/\/p\/[^/]+\/production/);
		await expect(page.getByText("Create your first service")).toBeVisible();

		// Create a Docker image service on the server: the empty state's Create
		// button opens a filterable listbox of sources.
		await page
			.locator('[data-slot="canvas-empty"]')
			.getByRole("button", { name: "Create", exact: true })
			.click();
		await page
			.getByRole("listbox", { name: "Create a service" })
			.getByRole("option", { name: /Docker image/ })
			.click();
		const dialog = page.getByRole("dialog");
		await dialog.getByLabel("Image").fill("traefik/whoami");
		await dialog.getByLabel("Name").fill(SERVICE_NAME);
		await dialog.getByLabel("Port").fill("80");
		const serverOption = dialog.getByRole("radio", {
			name: new RegExp(SERVER),
		});
		await serverOption.click();
		await expect(serverOption).toBeChecked();
		await dialog.getByRole("button", { name: "Create service" }).click();
		await expect(dialog).toBeHidden();

		// Production stages the new service: the toast opens the review sheet,
		// where the guarded key deploys it (lift the guard, then press the key).
		await page.getByRole("button", { name: "Review & deploy" }).click();
		const review = page.getByRole("dialog");
		await expect(review).toContainText(SERVICE_NAME);
		await review.getByRole("button", { name: "Lift production guard" }).click();
		await review.getByRole("button", { name: "Deploy · 1" }).click();

		// The node reports Live (status pill) with the LED caption "Live · …".
		const node = page.locator("[data-service-id]", { hasText: SERVICE_NAME });
		await expect(node).toBeVisible();
		await expect(node).toContainText("Live", { timeout: 120_000 });
		await expect(node.getByText(/^Live · /)).toBeVisible();

		// Inspector, Variables tab: add a secret and check it stays masked.
		await node.click();
		await page.getByRole("tab", { name: "Variables" }).click();
		await page.getByRole("button", { name: "New Variable" }).click();
		const varDialog = page.getByRole("dialog", { name: "New variable" });
		await varDialog.getByLabel("Name", { exact: true }).fill("API_KEY");
		await varDialog.getByLabel("Value").fill(SECRET_VALUE);
		await varDialog.getByRole("checkbox", { name: "Secret" }).check();
		await varDialog.getByRole("button", { name: "Add variable" }).click();
		await expect(varDialog).toBeHidden();

		const row = page.getByRole("list", { name: "Service variables" });
		await expect(row).toContainText("API_KEY");
		await expect(row).toContainText(/•{4,}/);
		await expect(row).toContainText("Secret value, hidden");
		await expect(page.locator("body")).not.toContainText(SECRET_VALUE);
		expect(await page.content()).not.toContain(SECRET_VALUE);

		// The edit is staged, not deployed: the top bar's Deploy key counts it.
		await expect(page.getByText("Deploy · 1", { exact: true })).toBeAttached();
	} finally {
		await cleanup(page);
	}
});
