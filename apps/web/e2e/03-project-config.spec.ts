import { expect, test } from "@playwright/test";
import { deleteProject, findProject, getJson, uniqueName } from "./support/api";
import { WHOAMI } from "./support/env";
import {
	createImageService,
	createProject,
	deployStaged,
	expectLive,
} from "./support/ui";

// Flow 3: nebula.toml. Deploy a service, export the project's file from the
// environment menu, add a variable to it the way a commit would, plan it (the
// plan lists the change and refuses a broken file), apply it as staged changes,
// and see the staged review say "set by nebula.toml" for the line the file owns.
// The service is deployed first: for a service that never ran, the review
// shows one line, "First deployment", and no field lines.

const FLAG = "E2E_CONFIG_FLAG";

test("export, plan shows the diff, apply stages it as set by nebula.toml", async ({
	page,
}) => {
	const project = uniqueName("e2e-cfg");
	const service = "web";
	try {
		await createProject(page, project);
		await createImageService(page, { name: service, image: WHOAMI, port: 80 });
		await deployStaged(page);
		await expectLive(page, service);

		// Export from the environment menu: the file names the project and the service.
		await page.getByRole("button", { name: /^production/ }).click();
		await page.getByRole("menuitem", { name: /Export nebula\.toml/ }).click();
		const exportDialog = page.getByRole("dialog", { name: "Export nebula.toml" });
		const exported = exportDialog.getByRole("textbox", { name: "nebula.toml" });
		await expect(exported).toHaveValue(/schema = 1/);
		await expect(exported).toHaveValue(new RegExp(`project = "${project}"`));
		await expect(exported).toHaveValue(new RegExp(`services\\.${service}`));
		const exportedFile = await exported.inputValue();
		await exportDialog.getByRole("button", { name: "Close" }).first().click();
		await expect(exportDialog).toBeHidden();

		const found = await findProject(page.request, project);
		expect(found, "the project exists").toBeTruthy();
		const base = `/api/v1/projects/${found?.id}/config`;
		const file = `${exportedFile.trimEnd()}\n\n[services.${service}.variables]\n${FLAG} = "on"\n`;

		// Validate and plan: a valid file lists what it would change; nothing is
		// written yet.
		const validated = await page.request.post(`${base}/validate`, {
			data: { file },
		});
		const validity: { valid: boolean } = await validated.json();
		expect(validity.valid).toBe(true);
		const planned = await page.request.post(`${base}/plan`, {
			data: { environment: "production", source: { file } },
		});
		expect(planned.ok(), "plan").toBe(true);
		const plan: {
			planId: string;
			valid: boolean;
			summary: { update: number; create: number; staged: number };
			services: {
				slug: string;
				serviceId: string;
				action: string;
				changes: { field: string; op: string; to: string; applies: string }[];
			}[];
		} = await planned.json();
		expect(plan.valid).toBe(true);
		expect(plan.summary).toMatchObject({ create: 0, update: 1, staged: 1 });
		expect(plan.services).toHaveLength(1);
		expect(plan.services[0]).toMatchObject({ slug: service, action: "update" });
		expect(plan.services[0]?.changes).toEqual([
			expect.objectContaining({
				field: `variables.${FLAG}`,
				op: "add",
				to: "on",
				applies: "staged",
			}),
		]);
		// Planning wrote nothing: the service still has no variable.
		const planned0 = await getJson<{ items: { key: string }[] }>(
			page.request,
			`/services/${plan.services[0]?.serviceId ?? ""}/variables`,
		);
		expect(planned0.items.map((v) => v.key)).not.toContain(FLAG);

		// A broken file is refused with a reason and never applied.
		const broken = await page.request.post(`${base}/validate`, {
			data: { file: `${file}\n[services.${service}.variables\n` },
		});
		const refusal: { valid: boolean; diagnostics: unknown[] } =
			await broken.json();
		expect(refusal.valid).toBe(false);
		expect(refusal.diagnostics.length).toBeGreaterThan(0);

		// Apply stages the change (deploy: false): nothing is deployed.
		const applied = await page.request.post(`${base}/apply`, {
			headers: { "Idempotency-Key": `e2e-${project}` },
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
		await page.getByRole("button", { name: "Deploy options" }).click();
		await page.getByRole("menuitem", { name: "Review changes" }).click();
		const review = page.getByRole("dialog", { name: "Review changes" });
		await expect(review).toContainText(`variables.${FLAG}`);
		await expect(review).toContainText("set by nebula.toml");
		await review.getByRole("button", { name: "Close" }).first().click();

		// Planning the same file again finds nothing left to do.
		const replanned = await page.request.post(`${base}/plan`, {
			data: { environment: "production", source: { file } },
		});
		const again: { summary: { update: number; unchanged: number } } =
			await replanned.json();
		expect(again.summary.update).toBe(0);
	} finally {
		await deleteProject(page.request, project);
	}
});
