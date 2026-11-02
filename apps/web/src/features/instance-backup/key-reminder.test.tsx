import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { backupView, problem } from "./instance-backup.fixture";
import { SecretKeyReminder } from "./key-reminder";

const unacknowledged = () =>
	backupView({ keyAcknowledgedAt: undefined, keyFingerprint: "9f8e7d6c5b4a" });

describe("SecretKeyReminder", () => {
	test("warns that backups exclude the key and shows its fingerprint until acknowledged", async () => {
		const api = stubApi({ "GET /instance/backup": { body: unacknowledged() } });
		await renderWithProviders(<SecretKeyReminder />);
		expect(
			await screen.findByText("Store your instance secret key separately"),
		).toBeInTheDocument();
		expect(screen.getByText("9f8e7d6c5b4a")).toBeInTheDocument();
		expect(screen.getByText("NEBULACTRL_SECRET_KEY")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "I stored the key" }),
		).toBeEnabled();
		expect(api.unmatched).toEqual([]);
	});

	test("renders nothing once an admin acknowledged the key", async () => {
		const api = stubApi({
			"GET /instance/backup": {
				body: backupView({ keyAcknowledgedAt: "2026-10-01T00:00:00Z" }),
			},
		});
		await renderWithProviders(<SecretKeyReminder />);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(screen.queryByTestId("secret-key-reminder")).not.toBeInTheDocument();
		expect(screen.queryByText(/secret key separately/)).not.toBeInTheDocument();
	});

	test("renders nothing when the settings did not load", async () => {
		const api = stubApi({
			"GET /instance/backup": problem(403, "Instance admins only."),
		});
		await renderWithProviders(<SecretKeyReminder />);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(screen.queryByTestId("secret-key-reminder")).not.toBeInTheDocument();
	});

	test("acknowledging posts the confirmation, then the card disappears with a toast", async () => {
		let acknowledged = false;
		const api = stubApi({
			"GET /instance/backup": () =>
				Response.json(
					acknowledged
						? backupView({ keyAcknowledgedAt: "2026-11-01T00:00:00Z" })
						: unacknowledged(),
				),
			"POST /instance/backup/key-acknowledgement": () => {
				acknowledged = true;
				return new Response(null, { status: 204 });
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<SecretKeyReminder />);
		await user.click(
			await screen.findByRole("button", { name: "I stored the key" }),
		);
		expect(await screen.findByText("Noted")).toBeInTheDocument();
		expect(screen.getByText("The reminder is gone.")).toBeInTheDocument();
		await waitFor(() =>
			expect(
				screen.queryByTestId("secret-key-reminder"),
			).not.toBeInTheDocument(),
		);
		const posts = api.calls.filter((c) => c.method === "POST");
		expect(posts.map((c) => c.path)).toEqual([
			"/instance/backup/key-acknowledgement",
		]);
		expect(api.unmatched).toEqual([]);
	});

	test("a failed confirmation keeps the card and reports the error", async () => {
		stubApi({
			"GET /instance/backup": { body: unacknowledged() },
			"POST /instance/backup/key-acknowledgement": problem(
				500,
				"database is read-only",
			),
		});
		const user = userEvent.setup();
		await renderWithProviders(<SecretKeyReminder />);
		await user.click(
			await screen.findByRole("button", { name: "I stored the key" }),
		);
		expect(
			await screen.findByText("Could not save the confirmation"),
		).toBeInTheDocument();
		expect(screen.getByText("database is read-only")).toBeInTheDocument();
		expect(screen.getByTestId("secret-key-reminder")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "I stored the key" }),
		).toBeEnabled();
	});
});
