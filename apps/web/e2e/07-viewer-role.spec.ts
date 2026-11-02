import { expect, test } from "@playwright/test";
import {
	deleteProject,
	getJson,
	type Item,
	list,
	projectServices,
	uniqueName,
} from "./support/api";
import { WHOAMI } from "./support/env";
import { createImageService, createProject } from "./support/ui";

// Flow 7: a viewer. The owner invites someone as a viewer; the invitee creates
// the account from the invite link, sees the owner's project, and every change
// they try is refused by the API with 403.
//
// The dashboard does not hide what a viewer cannot do: Create, Arrange, the
// Deploy key and New project are all shown and only fail when pressed (the
// finding is recorded as an annotation of the test). So the flow asserts the
// refusal and how the UI handles it: an error where the person is looking and
// nothing changed, not that the buttons are missing.

const FORBIDDEN = /permission/;

test("a viewer reads the project, every change is refused, the UI says so", async ({
	page,
	browser,
}, testInfo) => {
	const project = uniqueName("e2e-viewer");
	const service = "web";
	const email = `${uniqueName("viewer")}@example.com`;
	const baseURL = testInfo.project.use.baseURL ?? "";
	let member: { teamId: string; userId: string } | undefined;
	try {
		await createProject(page, project);
		await createImageService(page, { name: service, image: WHOAMI, port: 80 });
		const owned = await list(page.request, "/projects");
		const found = owned.find((p) => p.name === project);
		expect(found, "the project exists").toBeTruthy();
		const projectId = found?.id ?? "";
		const [environment] = await list(
			page.request,
			`/projects/${projectId}/environments`,
		);
		const environmentId = environment?.id ?? "";
		const [web] = await projectServices(page.request, projectId);
		expect(web?.name).toBe(service);

		// The owner invites a viewer.
		const teams = await getJson<{ items: { id: string; current: boolean }[] }>(
			page.request,
			"/teams",
		);
		const team = teams.items.find((t) => t.current);
		expect(team, "the owner has a current team").toBeTruthy();
		const invited = await page.request.post(
			`/api/v1/teams/${team?.id}/invites`,
			{ data: { role: "viewer", email } },
		);
		expect(invited.status()).toBe(201);
		const invite: { link: string } = await invited.json();

		// The invitee opens the link signed out and creates the account.
		const context = await browser.newContext({
			baseURL,
			ignoreHTTPSErrors: true,
			storageState: { cookies: [], origins: [] },
		});
		try {
			const viewer = await context.newPage();
			await viewer.goto(new URL(invite.link, baseURL).toString());
			await expect(
				viewer.getByRole("heading", { name: /^Join / }),
			).toBeVisible();
			await expect(viewer.getByText("Viewer", { exact: true })).toBeVisible();
			await viewer.getByLabel("Your name").fill("E2E Viewer");
			await viewer.getByLabel("Password").fill("viewer-password-123");
			await viewer
				.getByRole("button", { name: "Create account and join" })
				.click();
			await expect(viewer.locator('[data-page="projects"]')).toBeVisible();
			const me = await getJson<{ id: string; principal: { role: string } }>(
				viewer.request,
				"/me",
			);
			expect(me.principal.role).toBe("viewer");
			member = { teamId: team?.id ?? "", userId: me.id };

			// Reading works, in the API and in the page: the project is listed, its
			// canvas shows the owner's staged service.
			expect(
				(await list(viewer.request, "/projects")).map((p) => p.name),
			).toContain(project);
			expect(
				(await list(viewer.request, `/environments/${environmentId}/services`)).map(
					(s) => s.name,
				),
			).toEqual([service]);
			await viewer.getByText(project).first().click();
			await expect(viewer).toHaveURL(/\/p\/[^/]+\/production/);
			await expect(
				viewer.locator("[data-service-id]").filter({ hasText: service }),
			).toBeVisible();

			// The API refuses every change with 403.
			const refused: [string, string, object | undefined][] = [
				["POST", "/projects", { name: uniqueName("intruder") }],
				[
					"POST",
					`/environments/${environmentId}/services`,
					{ name: "intruder", sourceType: "image", config: { image: WHOAMI } },
				],
				["POST", `/environments/${environmentId}/deploy-staged`, {}],
				["DELETE", `/services/${web?.id}?confirm=${service}`, undefined],
				["DELETE", `/services/${web?.id}/variables/ANYTHING`, undefined],
				[
					"POST",
					`/teams/${team?.id}/invites`,
					{ role: "owner", email: `${uniqueName("intruder")}@example.com` },
				],
			];
			for (const [method, path, data] of refused) {
				const res = await viewer.request.fetch(`/api/v1${path}`, {
					method,
					...(data === undefined ? {} : { data }),
				});
				expect(res.status(), `${method} ${path}`).toBe(403);
				expect(await res.text(), `${method} ${path}`).toMatch(FORBIDDEN);
			}

			// What the page offers a viewer: the controls of a writer are there.
			const shown = {
				create: await viewer
					.getByRole("button", { name: "Create", exact: true })
					.first()
					.isVisible(),
				deploy: await viewer
					.getByRole("button", { name: /^Deploy · \d+$/ })
					.isVisible(),
			};
			if (shown.create || shown.deploy) {
				testInfo.annotations.push({
					type: "finding",
					description:
						"the canvas shows Create and the Deploy key to a viewer; the API refuses what they do (403)",
				});
			}

			// Creating a service: the dialog stays open with the refusal in it and no
			// card appears.
			await viewer
				.getByRole("button", { name: "Create", exact: true })
				.first()
				.click();
			await viewer
				.getByRole("listbox", { name: "Create a service" })
				.getByRole("option", { name: /Docker image/ })
				.click();
			const dialog = viewer.getByRole("dialog");
			await dialog.getByLabel("Image").fill(WHOAMI);
			await dialog.getByLabel("Name").fill("intruder");
			await dialog.getByRole("button", { name: "Create service" }).click();
			await expect(dialog.getByRole("alert")).toContainText(FORBIDDEN);
			await expect(dialog).toBeVisible();
			await dialog.getByRole("button", { name: "Cancel" }).click();
			await expect(dialog).toBeHidden();
			await expect(
				viewer.locator("[data-service-id]").filter({ hasText: "intruder" }),
			).toHaveCount(0);

			// Deploying: the guard lifts, the press is refused and a toast says why.
			await viewer
				.getByRole("button", { name: "Lift production guard" })
				.click();
			await viewer.getByRole("button", { name: /^Deploy · \d+$/ }).click();
			await expect(viewer.getByText("Could not deploy the changes")).toBeVisible();
			await expect(viewer.getByText(FORBIDDEN).first()).toBeVisible();

			// A new project: the dialog stays open with the refusal.
			await viewer.goto("/");
			await viewer.getByRole("button", { name: "New project" }).first().click();
			const projectDialog = viewer.getByRole("dialog", { name: "New project" });
			await projectDialog.getByLabel("Name").fill(uniqueName("intruder"));
			await projectDialog.getByRole("button", { name: "Create project" }).click();
			await expect(projectDialog.getByRole("alert")).toContainText(FORBIDDEN);

			// Nothing changed: the owner still sees one project of theirs and its
			// one staged service.
			const projects: Item[] = await list(page.request, "/projects");
			expect(projects.map((p) => p.name).filter((n) => n.includes("intruder"))).toEqual([]);
			expect(
				(await projectServices(page.request, projectId)).map((s) => s.name),
			).toEqual([service]);
		} finally {
			await context.close();
		}
	} finally {
		await deleteProject(page.request, project);
		if (member) {
			await page.request.delete(
				`/api/v1/teams/${member.teamId}/members/${member.userId}`,
			);
		}
	}
});
