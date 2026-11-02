import { expect, test } from "@playwright/test";
import { deleteProject, uniqueName } from "./support/api";
import { WHOAMI } from "./support/env";
import { getService, serviceUrl } from "./support/ingress";
import {
	addDomain,
	createImageService,
	createProject,
	deployStaged,
	expectLive,
	openService,
} from "./support/ui";

// Flow 2: create a project, deploy an image service, follow the deployment's
// log, reach the service through the agent's proxy, stage a secret.

const SECRET_VALUE = "sekret-canary-e2e-12345";

test("project to deployed image service, its deployment log, reached through the proxy", async ({
	page,
}) => {
	const project = uniqueName("e2e-deploy");
	const service = "whoami";
	const domain = `${project}.localhost`;
	try {
		await createProject(page, project);
		await expect(page.getByText("Create your first service")).toBeVisible();
		await createImageService(page, { name: service, image: WHOAMI, port: 80 });
		await openService(page, service);
		await addDomain(page, domain);

		// Nothing runs yet: the card is staged and the proxy has no route.
		expect((await getService(domain)).status).not.toBe(200);
		await page.getByRole("button", { name: "Close inspector" }).click();

		await deployStaged(page);

		// The deployment's page follows the log while it runs: the pull, the
		// container, the health check and the proxy route, step by step.
		await openService(page, service);
		await page.getByRole("tab", { name: "Deployments" }).click();
		const deployment = page.getByRole("list", {
			name: "Deployments, newest first",
		});
		await expect(deployment.getByText(WHOAMI).first()).toBeVisible();
		const log = page.getByRole("log", { name: "Deployment log" });
		await expect(log).toContainText(`Pulling ${WHOAMI}`);
		await expect(log).toContainText("[create] Creating container");
		await expect(log).toContainText("[health] Healthy");
		await expect(log).toContainText("[route] Updating proxy routes");
		await expect(deployment).toContainText("Succeeded");
		const steps = page.getByRole("list", { name: "Deployment steps" });
		for (const step of ["pull", "create", "start", "health", "route"]) {
			await expect(steps).toContainText(step);
		}

		// Live on the canvas, and the service answers on its domain.
		await page.getByRole("button", { name: "Close inspector" }).click();
		await expectLive(page, service);
		await expect
			.poll(async () => (await getService(domain)).status, {
				message: `${domain} answers through the proxy`,
				timeout: 60_000,
			})
			.toBe(200);
		const answer = await getService(domain);
		expect(answer.body).toContain("Hostname:");
		expect(answer.body).toContain(`Host: ${domain}`);

		// The same in a browser tab, as a visitor sees it.
		const visitor = await page.context().newPage();
		await visitor.goto(serviceUrl(domain));
		await expect(visitor.locator("body")).toContainText("Hostname:");
		await visitor.close();

		// A secret added afterwards is staged, not deployed, and never shown.
		await openService(page, service);
		await page.getByRole("tab", { name: "Variables" }).click();
		await page.getByRole("button", { name: "New Variable" }).click();
		const variable = page.getByRole("dialog", { name: "New variable" });
		await variable.getByLabel("Name", { exact: true }).fill("API_KEY");
		await variable.getByLabel("Value").fill(SECRET_VALUE);
		await variable.getByRole("checkbox", { name: "Secret" }).check();
		await variable.getByRole("button", { name: "Add variable" }).click();
		await expect(variable).toBeHidden();
		const row = page.getByRole("list", { name: "Service variables" });
		await expect(row).toContainText("API_KEY");
		await expect(row).toContainText("Secret value, hidden");
		await expect(page.locator("body")).not.toContainText(SECRET_VALUE);
		expect(await page.content()).not.toContain(SECRET_VALUE);
		await expect(page.getByText("Deploy · 1", { exact: true })).toBeAttached();
	} finally {
		await deleteProject(page.request, project);
	}
});
