import { describe, expect, test } from "bun:test";
import { useRouterState } from "@tanstack/react-router";
import { fireEvent, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithProviders } from "#/test/render";
import { pagesFor } from "./nav-model";
import { SettingsShell } from "./settings-shell";

function Where() {
	const path = useRouterState({
		select: ({ location }) =>
			location.pathname + (location.hash ? `#${location.hash}` : ""),
	});
	return <div data-testid="location">{path}</div>;
}

const owner = { instanceAdmin: false };

async function openTeam(
	props: Partial<Parameters<typeof SettingsShell>[0]> = {},
	role: "owner" | "developer" = "owner",
) {
	const user = userEvent.setup();
	const result = await renderWithProviders(
		<>
			<Where />
			<SettingsShell
				area="team"
				title="Team settings"
				scopeLine="Team · Acme · applies to everyone in it"
				pages={pagesFor("team", owner, role)}
				active="members"
				{...props}
			>
				<p>page body</p>
			</SettingsShell>
		</>,
		{ path: "/settings/members" },
	);
	return { user, ...result };
}

const sectionNav = () =>
	screen.getByRole("navigation", { name: "Team settings sections" });

describe("SettingsShell", () => {
	test("frames the area: h1, scope line, document title and the open page", async () => {
		await openTeam();
		expect(
			screen.getByRole("heading", { level: 1, name: "Team settings" }),
		).toBeInTheDocument();
		expect(
			screen.getByText("Team · Acme · applies to everyone in it"),
		).toBeInTheDocument();
		expect(document.title).toBe("Members · Team settings · NebulaCtrl");
		const section = screen.getByRole("region", { name: "Members" });
		expect(within(section).getByText("page body")).toBeInTheDocument();
		expect(section).toHaveAttribute("id", "team-members");
	});

	test("lists the pages under their group headers and marks the open one", async () => {
		await openTeam();
		const nav = within(sectionNav());
		for (const group of [
			"People",
			"Build and ship",
			"Servers",
			"Data and alerts",
		]) {
			expect(nav.getByRole("heading", { name: group })).toBeInTheDocument();
		}
		expect(nav.getByRole("link", { name: "Members" })).toHaveAttribute(
			"aria-current",
			"page",
		);
		expect(nav.getByRole("link", { name: "Members" })).toHaveAttribute(
			"href",
			"/settings/members",
		);
		expect(nav.getByRole("link", { name: "General" })).not.toHaveAttribute(
			"aria-current",
		);
		expect(nav.getByRole("link", { name: "Audit log" })).toHaveAttribute(
			"href",
			"/settings/audit-log",
		);
	});

	test("only lists the pages the person may use", async () => {
		await openTeam(
			{ pages: pagesFor("team", owner, "developer") },
			"developer",
		);
		const nav = within(sectionNav());
		expect(nav.getByRole("link", { name: "Members" })).toBeInTheDocument();
		expect(nav.queryByRole("link", { name: "Audit log" })).toBeNull();
		expect(nav.queryByRole("link", { name: "Integrations" })).toBeNull();
	});

	test("a plain click on a page navigates in the app", async () => {
		const { user } = await openTeam();
		await user.click(
			within(sectionNav()).getByRole("link", { name: "Audit log" }),
		);
		expect(screen.getByTestId("location")).toHaveTextContent(
			"/settings/audit-log",
		);
	});

	test("a modified click keeps its browser meaning", async () => {
		await openTeam();
		const link = within(sectionNav()).getByRole("link", { name: "Audit log" });
		const proceeded = fireEvent.click(link, { ctrlKey: true });
		// Not default-prevented: the browser opens it in a new tab.
		expect(proceeded).toBe(true);
		expect(screen.getByTestId("location")).toHaveTextContent(
			"/settings/members",
		);
		expect(fireEvent.click(link, { metaKey: true })).toBe(true);
		expect(fireEvent.click(link, { shiftKey: true })).toBe(true);
		expect(fireEvent.click(link, { altKey: true })).toBe(true);
		expect(fireEvent.click(link, { button: 1 })).toBe(true);
	});

	test("the filter narrows the list and announces how many pages match", async () => {
		const { user } = await openTeam();
		await user.type(
			screen.getByRole("textbox", { name: "Filter team settings" }),
			"back",
		);
		const nav = within(sectionNav());
		expect(
			nav.getByRole("link", { name: "Backup storage" }),
		).toBeInTheDocument();
		expect(nav.queryByRole("link", { name: "Members" })).toBeNull();
		expect(screen.getByRole("status")).toHaveTextContent(
			"1 page matches. Press Enter to open the first.",
		);
	});

	test("a filter matching several pages says so in the plural", async () => {
		const { user } = await openTeam();
		await user.type(
			screen.getByRole("textbox", { name: "Filter team settings" }),
			"log",
		);
		expect(screen.getByRole("status")).toHaveTextContent(/\d+ pages match\./);
	});

	test("Enter opens the first match and clears the filter", async () => {
		const { user } = await openTeam();
		const filter = screen.getByRole("textbox", {
			name: "Filter team settings",
		});
		await user.type(filter, "back{Enter}");
		expect(screen.getByTestId("location")).toHaveTextContent(
			"/settings/backup-storage",
		);
		expect(filter).toHaveValue("");
	});

	test("Enter with no match does nothing", async () => {
		const { user } = await openTeam();
		const filter = screen.getByRole("textbox", {
			name: "Filter team settings",
		});
		await user.type(filter, "zzzzqq{Enter}");
		expect(screen.getByTestId("location")).toHaveTextContent(
			"/settings/members",
		);
		expect(filter).toHaveValue("zzzzqq");
	});

	test("nothing matching says so and offers an Account page when one fits", async () => {
		const { user } = await openTeam();
		await user.type(
			screen.getByRole("textbox", { name: "Filter team settings" }),
			"passkey",
		);
		expect(
			screen.getByText("No settings match “passkey”."),
		).toBeInTheDocument();
		const link = screen.getByRole("link", {
			name: "Account settings → Security",
		});
		expect(link).toHaveAttribute("href", "/account/security");
		await user.click(link);
		expect(screen.getByTestId("location")).toHaveTextContent(
			"/account/security",
		);
		expect(
			screen.getByRole("textbox", { name: "Filter team settings" }),
		).toHaveValue("");
	});

	test("nothing matching and nothing elsewhere offers no link", async () => {
		const { user } = await openTeam();
		await user.type(
			screen.getByRole("textbox", { name: "Filter team settings" }),
			"zzzzqq",
		);
		expect(screen.getByText("No settings match “zzzzqq”.")).toBeInTheDocument();
		expect(screen.queryByText(/Looking for/)).toBeNull();
	});

	test("the Account area never points elsewhere", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<SettingsShell
				area="account"
				title="Account settings"
				scopeLine="Account"
				pages={pagesFor("account", owner, "owner")}
				active="profile"
			>
				<p>page body</p>
			</SettingsShell>,
		);
		await user.type(
			screen.getByRole("textbox", { name: "Filter account settings" }),
			"zzzzqq",
		);
		expect(screen.getByText("No settings match “zzzzqq”.")).toBeInTheDocument();
		expect(screen.queryByText(/Looking for/)).toBeNull();
	});

	test("Escape clears a filter; the clear button does too", async () => {
		const { user } = await openTeam();
		const filter = screen.getByRole("textbox", {
			name: "Filter team settings",
		});
		expect(screen.queryByRole("button", { name: "Clear filter" })).toBeNull();
		await user.type(filter, "back");
		await user.keyboard("{Escape}");
		expect(filter).toHaveValue("");
		expect(
			within(sectionNav()).getByRole("link", { name: "Members" }),
		).toBeInTheDocument();

		await user.type(filter, "back");
		await user.click(screen.getByRole("button", { name: "Clear filter" }));
		expect(filter).toHaveValue("");
		expect(screen.queryByRole("button", { name: "Clear filter" })).toBeNull();
	});

	test("an active page the person may not use still draws its section", async () => {
		await openTeam(
			{ pages: pagesFor("team", owner, "developer"), active: "audit-log" },
			"developer",
		);
		expect(
			screen.getByRole("region", { name: "Audit log" }),
		).toBeInTheDocument();
		expect(document.title).toBe("Audit log · Team settings · NebulaCtrl");
	});

	test("an unknown active page renders the children without a section", async () => {
		await openTeam({ active: "nope" });
		expect(screen.getByText("page body")).toBeInTheDocument();
		expect(screen.queryByRole("region")).toBeNull();
	});

	test("embedded draws the list and page without the frame or title", async () => {
		const before = document.title;
		await openTeam({
			area: "server",
			title: "Server settings",
			pages: pagesFor("server", owner, "owner"),
			active: "scheduling",
			serverId: "srv1",
			embedded: true,
		});
		expect(screen.queryByRole("heading", { level: 1 })).toBeNull();
		expect(document.title).toBe(before);
		expect(
			screen.getByRole("navigation", { name: "Server settings sections" }),
		).toBeInTheDocument();
		expect(screen.getByRole("link", { name: "Monitoring" })).toHaveAttribute(
			"href",
			"/servers/srv1/settings/monitoring",
		);
	});

	test("project pages link to hashes on the project page", async () => {
		const { user } = await openTeam({
			area: "project",
			title: "Project settings",
			pages: pagesFor("project", owner, "owner"),
			active: "general",
			projectId: "p9",
		});
		const link = screen.getByRole("link", { name: "Environments" });
		expect(link).toHaveAttribute("href", "/p/p9/settings#environments");
		await user.click(link);
		expect(screen.getByTestId("location")).toHaveTextContent(
			"/p/p9/settings#environments",
		);
	});

	test("the small-screen picker names the open page", async () => {
		await openTeam();
		expect(
			screen.getByRole("combobox", { name: "Team settings section" }),
		).toHaveTextContent("Members");
	});

	test("the small-screen picker opens another page", async () => {
		const { user } = await openTeam();
		await user.click(
			screen.getByRole("combobox", { name: "Team settings section" }),
		);
		await user.click(
			await screen.findByRole("option", { name: "Notifications" }),
		);
		expect(screen.getByTestId("location")).toHaveTextContent(
			"/settings/notifications",
		);
	});
});
