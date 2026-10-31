import {
	type APIRequestContext,
	expect,
	type Page,
	test,
} from "@playwright/test";

// Smoke flow against a running dev stack, as in smoke.spec.ts (same
// E2E_EMAIL / E2E_PASSWORD / E2E_SERVER): export a project's nebula.toml,
// edit it, plan, apply (stage), and see the staged review say "set by
// nebula.toml" for the line the file owns.

const EMAIL = process.env.E2E_EMAIL ?? "admin@example.com";
const PASSWORD = process.env.E2E_PASSWORD ?? "correct-horse-battery";
const SERVER = process.env.E2E_SERVER ?? "agent1";

const rand = Math.random().toString(36).slice(2, 8);
const PROJECT_NAME = `e2e-cfg-${rand}`;
const SERVICE_NAME = `e2e-cfg-${rand}`;
const FLAG = "E2E_CONFIG_FLAG";

interface Item {
	id: string;
	name: string;
	slug: string;
	status?: string;
}

async function list(request: APIRequestContext, path: string): Promise<Item[]> {
	const res = await request.get(`/api/v1${path}`);
	expect(res.ok(), `GET ${path}`).toBe(true);
	const body: { items: Item[] } = await res.json();
	return body.items;
}

async function signIn(page: Page) {
	const setup = await page.request.get("/api/v1/setup");
	const { needsSetup }: { needsSetup: boolean } = await setup.json();
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
			await request.delete(
				`/api/v1/services/${svc.id}?confirm=${svc.slug}&deleteVolumes=true`,
			);
		}
	}
	await request.delete(`/api/v1/projects/${project.id}`);
}

test("export, edit, plan, stage: the staged review says set by nebula.toml", async ({
	page,
}) => {
	try {
		await signIn(page);
		await expect
			.poll(
				async () =>
					(await list(page.request, "/servers")).find((s) => s.name === SERVER)
						?.status,
				{ message: `server ${SERVER} online`, timeout: 30_000 },
			)
			.toBe("online");

		await page.getByRole("button", { name: "New project" }).first().click();
		await page.getByLabel("Name").fill(PROJECT_NAME);
		await page.getByRole("button", { name: "Create project" }).click();
		await expect(page).toHaveURL(/\/p\/[^/]+\/production/);
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
		await dialog.getByRole("radio", { name: new RegExp(SERVER) }).click();
		await dialog.getByRole("button", { name: "Create service" }).click();
		await expect(dialog).toBeHidden();

		const project = (await list(page.request, "/projects")).find(
			(p) => p.name === PROJECT_NAME,
		);
		expect(project, "project exists").toBeTruthy();
		const projectId = project?.id ?? "";
		const base = `/api/v1/projects/${projectId}/config`;

		// Export what exists, then add one variable the file owns.
		const exported = await page.request.get(
			`${base}/export?environment=production`,
			{ headers: { Accept: "application/json" } },
		);
		expect(exported.ok(), "export").toBe(true);
		const { file: exportedFile }: { file: string } = await exported.json();
		expect(exportedFile).toContain("schema = 1");
		const slug = (
			await list(
				page.request,
				`/environments/${
					(await list(page.request, `/projects/${projectId}/environments`))[0]
						?.id
				}/services`,
			)
		)[0]?.slug;
		const file = `${exportedFile.trimEnd()}\n\n[services.${slug}.variables]\n${FLAG} = "on"\n`;

		const validated = await page.request.post(`${base}/validate`, {
			data: { file },
		});
		const validity: { valid: boolean } = await validated.json();
		expect(validity.valid).toBe(true);

		const planned = await page.request.post(`${base}/plan`, {
			data: { environment: "production", source: { file } },
		});
		expect(planned.ok(), "plan").toBe(true);
		const plan: { planId: string; valid: boolean } = await planned.json();
		expect(plan.valid).toBe(true);

		const applied = await page.request.post(`${base}/apply`, {
			headers: { "Idempotency-Key": `e2e-${rand}` },
			data: {
				environment: "production",
				source: { file },
				planId: plan.planId,
				deploy: false,
			},
		});
		expect(applied.ok(), "apply").toBe(true);

		// The staged review marks the line the file owns.
		await page.reload();
		await page
			.getByRole("button", { name: /Deploy/ })
			.first()
			.click();
		const review = page.getByRole("dialog");
		await expect(review).toContainText(`variables.${FLAG}`);
		await expect(review).toContainText("set by nebula.toml");
	} finally {
		await cleanup(page);
	}
});
