import { expect, type Page, test } from "@playwright/test";
import { deleteProject, getJson, list, uniqueName } from "./support/api";
import {
	closeShell,
	createProject,
	deployStaged,
	expectLive,
	openService,
	openShell,
	typeLine,
} from "./support/ui";

// Flow 8: a managed PostgreSQL database, a backup, a restore over it. The
// database is created from the Create menu and deployed (the first run pulls the
// image into the stack's Docker daemon, so the deploy gets a long deadline); a
// directory on the server is added as the backup destination. A table with one
// row is written in the web terminal and backed up, a second row is written, and
// the backup is restored over the database with the service's name typed to
// confirm. Then the first row is back, the second is gone, and the restore left a
// safety backup behind.

const DB = "pg";
const TABLE = "e2e_marker";
const DESTINATION = "local-e2e";

interface Backup {
	id: string;
	status: string;
	trigger: string;
	destinationKind?: string;
	restorable: boolean;
}
interface Restore {
	mode: string;
	status: string;
	error?: string;
	preBackupId?: string;
}

const PSQL = 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"';

/** Runs one statement with psql in the terminal. */
async function exec(page: Page, statement: string): Promise<void> {
	await typeLine(page, `${PSQL} -c "${statement}"`);
}

/** Prints `label=<the query's single value>`; the typed command line never contains that text. */
async function show(page: Page, label: string, query: string): Promise<void> {
	await typeLine(page, `echo "${label}=$(${PSQL} -tA -c "${query}")"`);
}

test("create a database, back it up to a local directory, restore it over itself", async ({
	page,
}) => {
	test.setTimeout(900_000);
	const project = uniqueName("e2e-db");
	let destinationId: string | undefined;
	try {
		// A destination an earlier, failed run left on a kept stack would turn
		// "Add destination…" into a list: start without it.
		for (const old of await list(page.request, "/backup-destinations")) {
			if (old.name === DESTINATION) {
				await page.request.delete(`/api/v1/backup-destinations/${old.id}`);
			}
		}
		await createProject(page, project);
		await page
			.locator("main")
			.getByRole("button", { name: "Create", exact: true })
			.first()
			.click();
		const menu = page.getByRole("listbox", { name: "Create a service" });
		await menu.getByRole("option", { name: /^Database/ }).click();
		await menu.getByRole("option", { name: /^PostgreSQL/ }).click();
		const create = page.getByRole("dialog", { name: "New database" });
		await create.getByLabel("Name", { exact: true }).fill(DB);
		await create.getByRole("button", { name: "Create database" }).click();
		await expect(create).toBeHidden();
		await deployStaged(page);
		await expectLive(page, DB, 480_000);
		await openService(page, DB);
		const serviceId =
			(await page
				.locator("[data-service-id]")
				.first()
				.getAttribute("data-service-id")) ?? "";

		// The backup destination: a directory on the server (not off-site).
		await page.getByRole("tab", { name: /^Backups/ }).click();
		await page.getByRole("button", { name: "Add destination…" }).click();
		const destination = page.getByRole("dialog", {
			name: "Add backup destination",
		});
		await destination.getByLabel("Name").fill(DESTINATION);
		await destination
			.getByRole("button", { name: "Directory on the server" })
			.click();
		await destination.getByRole("button", { name: "Add destination" }).click();
		await expect(destination).toBeHidden();
		const destinations = await list(page.request, "/backup-destinations");
		const added = destinations.find((d) => d.name === DESTINATION);
		expect(added, "the destination is saved").toBeTruthy();
		destinationId = added?.id;
		expect(added).toMatchObject({ kind: "local", offSite: false });

		// State before the backup: one row.
		const shell = await openShell(page, DB);
		await exec(page, `create table ${TABLE}(v text)`);
		await exec(page, `insert into ${TABLE} values ('kept')`);
		await show(page, "before", `select count(*) from ${TABLE}`);
		await expect(shell).toContainText("before=1");
		await closeShell(page, DB);

		// Back up now: the row of the Backups table says Succeeded for the directory.
		await page.getByRole("button", { name: "Back up now" }).first().click();
		await page
			.getByRole("dialog", { name: "Back up now" })
			.getByRole("button", { name: "Back up now" })
			.click();
		const backups = () =>
			getJson<{ items: Backup[] }>(
				page.request,
				`/services/${serviceId}/backups`,
			).then((r) => r.items);
		await expect
			.poll(async () => (await backups()).map((b) => b.status), {
				message: "the backup succeeds",
				timeout: 120_000,
			})
			.toContain("succeeded");
		const [backup] = await backups();
		expect(backup).toMatchObject({
			trigger: "manual",
			destinationKind: "local",
			restorable: true,
		});
		const row = page.getByRole("row").filter({ hasText: DESTINATION });
		await expect(row).toContainText("Succeeded");

		// A second row after the backup: this is what a restore has to take away.
		const writer = await openShell(page, DB);
		await exec(page, `insert into ${TABLE} values ('lost')`);
		await show(page, "after", `select count(*) from ${TABLE}`);
		await expect(writer).toContainText("after=2");
		await closeShell(page, DB);

		// Restore over the database: the confirmation is the service's name.
		await row.getByRole("button", { name: "Restore", exact: true }).click();
		const restore = page.getByRole("dialog", { name: "Restore backup" });
		await restore.getByRole("button", { name: "Over this database" }).click();
		const overwrite = restore.getByRole("button", { name: "Overwrite database" });
		await expect(overwrite).toBeDisabled();
		await restore.getByLabel(/^Type .* to confirm/).fill(DB);
		await overwrite.click();
		await expect(restore).toBeHidden();
		await expect
			.poll(
				async () =>
					(
						await getJson<{ items: Restore[] }>(
							page.request,
							`/services/${serviceId}/backup-restores`,
						)
					).items.map((r) => `${r.mode}:${r.status}:${r.error ?? ""}`),
				{ message: "the restore succeeds", timeout: 240_000 },
			)
			.toEqual(["in_place:succeeded:"]);
		const [restored] = (
			await getJson<{ items: Restore[] }>(
				page.request,
				`/services/${serviceId}/backup-restores`,
			)
		).items;
		expect(restored?.preBackupId, "a safety backup was taken first").toBeTruthy();
		expect((await backups()).map((b) => b.trigger).sort()).toEqual([
			"manual",
			"pre_restore",
		]);

		// The database is back to the backup: the first row, not the second.
		const reader = await openShell(page, DB);
		await show(page, "rows", `select string_agg(v, ',') from ${TABLE}`);
		await expect(reader).toContainText("rows=kept", { timeout: 60_000 });
		await expect(reader).not.toContainText("lost");
		await closeShell(page, DB);
	} finally {
		await deleteProject(page.request, project);
		if (destinationId) {
			await page.request.delete(`/api/v1/backup-destinations/${destinationId}`);
		}
	}
});
