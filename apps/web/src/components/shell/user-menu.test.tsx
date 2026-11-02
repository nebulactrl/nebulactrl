import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ApiRoutes } from "#/test/api";
import { make, problem } from "#/test/factories";
import { ADMIN, ME, renderAt, shellApi, where } from "./shell-test-kit";
import { UserMenu } from "./user-menu";

const UPDATE = make("UpdateView", {
	available: true,
	canUpdate: true,
	checkEnabled: true,
	current: "1.0.0",
	latest: "1.3.0",
	isAdmin: true,
});
const NO_UPDATE = { ...UPDATE, available: false };

function api(extra: ApiRoutes = {}) {
	return shellApi({ "GET /system/update": { body: NO_UPDATE }, ...extra });
}

function menu(props: Partial<Parameters<typeof UserMenu>[0]> = {}) {
	return (
		<UserMenu
			user={ME as never}
			version="v1.2.3"
			versionPending={false}
			collapsed={false}
			{...props}
		/>
	);
}

async function open(
	props: Partial<Parameters<typeof UserMenu>[0]> = {},
	path = "/servers",
) {
	const user = userEvent.setup();
	const view = await renderAt(menu(props), { path });
	await user.click(
		await screen.findByRole("button", { name: /Account menu for/ }),
	);
	await screen.findByRole("menu");
	return { user, ...view };
}

describe("UserMenu trigger", () => {
	test("is named for the person and shows their email and name", async () => {
		api();
		await renderAt(menu());
		const trigger = screen.getByRole("button", {
			name: "Account menu for Ada Lovelace",
		});
		expect(trigger).toHaveTextContent("Ada Lovelace");
		expect(trigger).toHaveTextContent("ada@example.com");
	});

	test("a person whose name is their email shows their role instead of repeating it", async () => {
		api();
		await renderAt(menu({ user: { ...ME, name: "ada@example.com" } as never }));
		const trigger = screen.getByRole("button", {
			name: "Account menu for ada@example.com",
		});
		expect(trigger).toHaveTextContent("Owner");
	});

	test("no name at all falls back to the email", async () => {
		api();
		await renderAt(menu({ user: { ...ME, name: "" } as never }));
		expect(
			screen.getByRole("button", { name: "Account menu for ada@example.com" }),
		).toBeInTheDocument();
	});

	test("someone without a team role is told so", async () => {
		api();
		const user = { ...ME, principal: { ...ME.principal, role: undefined } };
		await renderAt(menu({ user: { ...user, name: user.email } as never }));
		expect(
			screen.getByRole("button", { name: /Account menu for/ }),
		).toHaveTextContent("No team");
	});

	test("collapsed, only the avatar remains", async () => {
		api();
		await renderAt(menu({ collapsed: true }));
		const trigger = screen.getByRole("button", {
			name: "Account menu for Ada Lovelace",
		});
		expect(trigger).not.toHaveTextContent("Ada Lovelace");
		expect(trigger).not.toHaveTextContent("ada@example.com");
	});
});

describe("UserMenu menu", () => {
	test("shows the person and the build", async () => {
		api();
		await open();
		expect(screen.getByRole("menu")).toHaveTextContent("ada@example.com");
		expect(screen.getByText("v1.2.3")).toBeInTheDocument();
		expect(
			screen.getByText("NebulaCtrl", { exact: false }),
		).toBeInTheDocument();
	});

	test("words a development build", async () => {
		api();
		await open({ version: "dev" });
		expect(screen.getByText("development build")).toBeInTheDocument();
	});

	test("with no version and none pending there is no build line", async () => {
		api();
		await open({ version: undefined });
		expect(screen.getByRole("menu")).not.toHaveTextContent("NebulaCtrl");
	});

	test("holds the version's place while it loads", async () => {
		api();
		await open({ version: undefined, versionPending: true });
		expect(screen.getByRole("menu")).toHaveTextContent("NebulaCtrl");
		expect(screen.queryByText("v1.2.3")).toBeNull();
	});

	test("Account settings opens the profile", async () => {
		api();
		const { user, router } = await open();
		await user.click(
			screen.getByRole("menuitem", { name: "Account settings" }),
		);
		await waitFor(() => expect(where(router)).toBe("/account/profile"));
	});

	test("Docs opens the docs", async () => {
		api();
		const { user, router } = await open();
		await user.click(screen.getByRole("menuitem", { name: "Docs" }));
		await waitFor(() => expect(where(router)).toBe("/docs"));
	});

	test("All projects goes home", async () => {
		api();
		const { user, router } = await open({}, "/servers");
		await user.click(screen.getByRole("menuitem", { name: "All projects" }));
		await waitFor(() => expect(where(router)).toBe("/"));
	});

	test("Escape closes the menu", async () => {
		api();
		const { user } = await open();
		await user.keyboard("{Escape}");
		await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
	});
});

describe("UserMenu sign out", () => {
	test("posts to /auth/logout, drops the cache and lands on /login", async () => {
		const stub = api({ "POST /auth/logout": { status: 204 } });
		const { user, router, queryClient } = await open();
		queryClient.setQueryData(["secret"], { team: "acme" });
		await user.click(screen.getByRole("menuitem", { name: "Sign out" }));
		await waitFor(() => expect(where(router)).toBe("/login"));
		expect(
			stub.calls.filter((c) => c.method === "POST").map((c) => c.path),
		).toEqual(["/auth/logout"]);
		expect(queryClient.getQueryData(["secret"])).toBeUndefined();
	});

	test("a failed sign-out keeps the session and says so", async () => {
		api({
			"POST /auth/logout": problem(500, { title: "Server exploded" }),
		});
		const { user, router, queryClient } = await open();
		queryClient.setQueryData(["keep"], 1);
		await user.click(screen.getByRole("menuitem", { name: "Sign out" }));
		expect(await screen.findByText("Sign-out failed")).toBeInTheDocument();
		expect(
			screen.getByText(/You are still signed in; try again\./),
		).toBeInTheDocument();
		expect(where(router)).toBe("/servers");
		expect(queryClient.getQueryData(["keep"])).toBe(1);
	});
});

describe("UserMenu update notice", () => {
	test("the instance admin is told a newer release is out, in the label and as a menu entry", async () => {
		api({ "GET /system/update": { body: UPDATE } });
		const user = userEvent.setup();
		const { router } = await renderAt(menu({ user: ADMIN as never }));
		const trigger = await screen.findByRole("button", {
			name: "Account menu for Ada Lovelace, v1.3.0 available",
		});
		expect(trigger).toHaveTextContent("v1.3.0 available");
		await user.click(trigger);
		await user.click(
			await screen.findByRole("menuitem", { name: "v1.3.0 available" }),
		);
		await waitFor(() => expect(where(router)).toBe("/admin/updates"));
	});

	test("anyone else is not told, and the endpoint is not even asked for their notice chip", async () => {
		api({ "GET /system/update": { body: UPDATE } });
		await renderAt(menu());
		await waitFor(() =>
			expect(
				screen.getByRole("button", { name: /Account menu for/ }),
			).toBeInTheDocument(),
		);
		expect(
			screen.getByRole("button", { name: "Account menu for Ada Lovelace" }),
		).not.toHaveTextContent("available");
	});

	test("an admin with nothing newer sees no chip", async () => {
		api();
		await renderAt(menu({ user: ADMIN as never }));
		await waitFor(() =>
			expect(
				screen.getByRole("button", { name: /Account menu for/ }),
			).toBeInTheDocument(),
		);
		expect(
			screen.getByRole("button", { name: "Account menu for Ada Lovelace" }),
		).toBeInTheDocument();
	});
});
