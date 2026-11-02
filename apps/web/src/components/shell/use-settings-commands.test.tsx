import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import { type PaletteCommand, useCommandRegistry } from "./palette-registry";
import { ADMIN, ME, renderAt, shellApi, where } from "./shell-test-kit";
import { useSettingsCommands } from "./use-settings-commands";

function Registrar() {
	useSettingsCommands();
	return null;
}

let latest: readonly PaletteCommand[] = [];
function Registry() {
	latest = useCommandRegistry();
	return <output aria-label="count">{latest.length}</output>;
}

function Host() {
	return (
		<>
			<Registrar />
			<Registry />
		</>
	);
}

const settings = () => latest.filter((c) => c.id.startsWith("settings:"));

describe("useSettingsCommands", () => {
	test("registers nothing until the signed-in person is known", async () => {
		shellApi({ "GET /me": () => new Promise<Response>(() => {}) });
		await renderAt(<Host />);
		expect(settings()).toEqual([]);
	});

	test("an owner gets account and team pages, not admin or project pages", async () => {
		shellApi();
		await renderAt(<Host />, { path: "/servers" });
		await waitFor(() => expect(settings().length).toBeGreaterThan(0));
		const areas = new Set(settings().map((c) => c.id.split(":")[1]));
		expect([...areas].sort()).toEqual(["account", "team"]);
	});

	test("every page is search-only, grouped as Settings and findable by its keywords", async () => {
		shellApi();
		await renderAt(<Host />, { path: "/servers" });
		await waitFor(() => expect(settings().length).toBeGreaterThan(0));
		for (const command of settings()) {
			expect(command.searchOnly).toBe(true);
			expect(command.group).toBe("Settings");
			expect(command.keywords).toContain("settings");
		}
		const profile = settings().find((c) => c.id === "settings:account:profile");
		expect(profile?.label).toBe("Account settings: Profile");
		expect(profile?.keywords).toContain("password");
	});

	test("the instance admin also gets the Admin pages", async () => {
		shellApi({ "GET /me": { body: ADMIN } });
		await renderAt(<Host />, { path: "/servers" });
		await waitFor(() =>
			expect(settings().some((c) => c.id.startsWith("settings:admin:"))).toBe(
				true,
			),
		);
	});

	test("a viewer is not offered pages only managers may use", async () => {
		const owner = shellApi();
		const { unmount } = await renderAt(<Host />, { path: "/servers" });
		await waitFor(() => expect(settings().length).toBeGreaterThan(0));
		const ownerCount = settings().length;
		unmount();
		expect(owner.unmatched).toEqual([]);

		shellApi({
			"GET /me": {
				body: { ...ME, principal: { ...ME.principal, role: "viewer" } },
			},
		});
		await renderAt(<Host />, { path: "/servers" });
		await waitFor(() => expect(settings().length).toBeGreaterThan(0));
		expect(settings().length).toBeLessThan(ownerCount);
	});

	test("inside a project the project's pages appear, with its id in the link", async () => {
		shellApi();
		await renderAt(<Host />, { path: "/p/p1/staging" });
		await waitFor(() =>
			expect(settings().some((c) => c.id.startsWith("settings:project:"))).toBe(
				true,
			),
		);
	});

	test("running a command navigates to that page", async () => {
		shellApi();
		const { router } = await renderAt(<Host />, { path: "/servers" });
		await waitFor(() => expect(settings().length).toBeGreaterThan(0));
		settings()
			.find((c) => c.id === "settings:account:profile")
			?.run();
		await waitFor(() => expect(where(router)).toBe("/account/profile"));
		expect(screen.getByLabelText("count")).toBeInTheDocument();
	});
});
