import { expect, test } from "@playwright/test";
import { deleteProject, uniqueName } from "./support/api";
import { WHOAMI } from "./support/env";
import { getService } from "./support/ingress";
import {
	addDomain,
	addVariable,
	closeInspector,
	createImageService,
	createProject,
	deployStaged,
	expectLive,
	openService,
	setVariable,
} from "./support/ui";

// Flow 4: a deploy that fails keeps the previous release serving, and a
// rollback from the Deployments tab brings an older release back.
//
// v1 and v2 are two releases of one image that answer with different names
// (WHOAMI_NAME); the third deploy points at an image tag that does not exist.

test("a failing deploy keeps the previous release serving, then roll back from the UI", async ({
	page,
}) => {
	test.setTimeout(420_000);
	const project = uniqueName("e2e-rollback");
	const service = "whoami";
	const domain = `${project}.localhost`;
	const answers = async () => (await getService(domain)).body;
	try {
		await createProject(page, project);
		await createImageService(page, { name: service, image: WHOAMI, port: 80 });
		await openService(page, service);
		await addDomain(page, domain);
		await addVariable(page, "WHOAMI_NAME", "release-one");
		await closeInspector(page);
		await deployStaged(page);
		await expectLive(page, service);
		await expect.poll(answers, { timeout: 60_000 }).toContain("Name: release-one");

		// Release two.
		await openService(page, service);
		await setVariable(page, "WHOAMI_NAME", "release-two");
		await closeInspector(page);
		await deployStaged(page);
		await expect.poll(answers, { timeout: 120_000 }).toContain("Name: release-two");
		await expectLive(page, service);

		// Release three cannot start: its image tag does not exist. The deployment
		// fails at the pull and nothing was taken down.
		await openService(page, service);
		await page.getByRole("tab", { name: /^Settings/ }).click();
		await page.getByRole("tab", { name: "General" }).click();
		await page.getByRole("textbox", { name: "Image", exact: true }).fill(
			`${WHOAMI.split(":")[0]}:e2e-no-such-tag`,
		);
		await page.getByRole("button", { name: "Save changes" }).click();
		await expect(page.getByText("All changes saved")).toBeVisible();
		await closeInspector(page);
		await deployStaged(page);

		await openService(page, service);
		await page.getByRole("tab", { name: "Deployments" }).click();
		await expect(
			page.getByText("Deployment failed: the previous release is still serving"),
		).toBeVisible({ timeout: 180_000 });
		// One row per deployment; the opened row has its own list of steps, which
		// getByRole("listitem") would count too.
		const list = page.getByRole("list", { name: "Deployments, newest first" });
		const rows = list.locator(":scope > li");
		await expect(rows).toHaveCount(3);
		await expect(rows.first()).toContainText("Failed");
		await expect(rows.first()).not.toContainText("Live");
		// Release two still answers.
		expect((await getService(domain)).status).toBe(200);
		expect(await answers()).toContain("Name: release-two");

		// Roll back to release one: the older succeeded deployment offers it.
		const releaseOne = rows.last();
		await expect(releaseOne).toContainText("Succeeded");
		await releaseOne
			.getByRole("button", { name: "Rollback to this release" })
			.click();
		const confirm = page.getByRole("alertdialog");
		await expect(confirm).toContainText(`Roll back ${service} in production`);
		await confirm.getByRole("button", { name: "Lift production guard" }).click();
		await confirm.getByRole("button", { name: "Roll back" }).click();

		await expect.poll(answers, { timeout: 120_000 }).toContain("Name: release-one");
		await expect(rows).toHaveCount(4);
		await expect(rows.first()).toContainText("Rollback");
		await expect(rows.first()).toContainText("Live");
	} finally {
		await deleteProject(page.request, project);
	}
});
