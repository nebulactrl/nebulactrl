import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import type { InstanceUpgradeView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { PreUpgradeBackupNote } from "./pre-upgrade-note";

type PreUpgrade = NonNullable<InstanceUpgradeView["preUpgradeBackup"]>;

const upgrade = (preUpgradeBackup?: PreUpgrade): InstanceUpgradeView => ({
	agentAutoUpgrade: true,
	behind: 0,
	controlPlaneVersion: "1.4.0",
	servers: [],
	upgradable: 0,
	...(preUpgradeBackup ? { preUpgradeBackup } : {}),
});

const at = "2026-10-27T03:30:00Z";

describe("PreUpgradeBackupNote", () => {
	test("says the backup before the upgrade succeeded, with the versions and when", async () => {
		const api = stubApi({
			"GET /instance/upgrade": {
				body: upgrade({
					status: "succeeded",
					at,
					fromVersion: "1.3.0",
					toVersion: "1.4.0",
				}),
			},
		});
		const { container } = await renderWithProviders(<PreUpgradeBackupNote />);
		expect(
			await screen.findByText("Backed up before the upgrade (1.3.0 → 1.4.0)"),
		).toBeInTheDocument();
		expect(
			container.querySelector(`time[title="2026-10-27 03:30:00 UTC"]`),
		).not.toBeNull();
		expect(api.unmatched).toEqual([]);
	});

	test("a failed backup shows the failure and the reason from the control plane", async () => {
		stubApi({
			"GET /instance/upgrade": {
				body: upgrade({
					status: "failed",
					at,
					toVersion: "1.4.0",
					message: "pg_dump: connection refused",
				}),
			},
		});
		await renderWithProviders(<PreUpgradeBackupNote />);
		expect(
			await screen.findByText("Backup before the upgrade failed (to 1.4.0)"),
		).toBeInTheDocument();
		expect(screen.getByText("pg_dump: connection refused")).toBeInTheDocument();
	});

	test("a forced upgrade says it went ahead without a backup", async () => {
		stubApi({
			"GET /instance/upgrade": { body: upgrade({ status: "forced", at }) },
		});
		await renderWithProviders(<PreUpgradeBackupNote />);
		expect(
			await screen.findByText("Upgraded without a backup, forced"),
		).toBeInTheDocument();
	});

	test("a skipped backup says there was no destination", async () => {
		stubApi({
			"GET /instance/upgrade": { body: upgrade({ status: "skipped", at }) },
		});
		await renderWithProviders(<PreUpgradeBackupNote />);
		expect(
			await screen.findByText("No backup before the upgrade: no destination"),
		).toBeInTheDocument();
	});

	test("shows no message line when the control plane sent none", async () => {
		stubApi({
			"GET /instance/upgrade": { body: upgrade({ status: "succeeded", at }) },
		});
		const { container } = await renderWithProviders(<PreUpgradeBackupNote />);
		await screen.findByText("Backed up before the upgrade");
		// Only the status line: no second line under it.
		expect(container.querySelector("p")?.children).toHaveLength(1);
	});

	test("renders nothing until the control plane has upgraded once", async () => {
		const api = stubApi({ "GET /instance/upgrade": { body: upgrade() } });
		const { container } = await renderWithProviders(<PreUpgradeBackupNote />);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(container.querySelector("p")).toBeNull();
		expect(screen.queryByText(/before the upgrade/i)).not.toBeInTheDocument();
	});
});
