import { expect, test } from "@playwright/test";
import { deleteProject, uniqueName } from "./support/api";
import { NGINX } from "./support/env";
import { getService } from "./support/ingress";
import {
	addDomain,
	closeInspector,
	closeShell,
	createImageService,
	createProject,
	deployStaged,
	expectLive,
	openService,
	openShell,
	typeLine,
} from "./support/ui";

// Flow 6: the live logs and the web terminal of a running service. nginx logs
// each request it serves to stdout and has a shell.

test("live logs follow requests and a shell runs commands in the container", async ({
	page,
}) => {
	test.setTimeout(300_000);
	const project = uniqueName("e2e-shell");
	const service = "web";
	const domain = `${project}.localhost`;
	try {
		await createProject(page, project);
		await createImageService(page, { name: service, image: NGINX, port: 80 });
		await openService(page, service);
		await addDomain(page, domain);
		await closeInspector(page);
		await deployStaged(page);
		await expectLive(page, service);
		await expect
			.poll(async () => (await getService(domain)).status, { timeout: 60_000 })
			.toBe(200);

		// Live logs: open the Logs tab, then make the service log a request that
		// has a unique path; the line appears without reloading.
		await openService(page, service);
		await page.getByRole("tab", { name: "Logs" }).click();
		const marker = `/e2e-log-${Math.random().toString(36).slice(2, 8)}`;
		const logs = page.getByRole("tabpanel", { name: "Logs" });
		await expect(logs).toBeVisible();
		await expect
			.poll(
				async () => {
					await getService(domain, marker);
					return logs.innerText();
				},
				{ timeout: 60_000, intervals: [1000] },
			)
			.toContain(`GET ${marker}`);

		// Terminal: "Open shell…" in the inspector's menu, confirm production,
		// run a command whose output differs from what was typed.
		const terminal = await openShell(page, service);
		await typeLine(page, "echo e2e-$((6*7)) $(hostname)");
		await expect(terminal).toContainText(/e2e-42 \S+/, { timeout: 30_000 });
		await typeLine(page, "cat /etc/os-release | head -1");
		await expect(terminal).toContainText("Alpine Linux");

		// Closing the panel asks before it ends the session.
		await closeShell(page, service);
	} finally {
		await deleteProject(page.request, project);
	}
});
