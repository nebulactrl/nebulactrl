import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import {
	act,
	fireEvent,
	renderHook,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { make } from "#/test/factories";
import { ADMIN, ME, renderAt, shellApi, stubMedia } from "./shell-test-kit";
import { Sidebar } from "./sidebar";
import { useSidebar, useSidebarDrawer } from "./use-sidebar";

const NARROW = "(max-width: 759px)";
const KEY = "nebulactrl:sidebar";

let media: ReturnType<typeof stubMedia>;

/** The sidebar's store is module state shared by every test: put it back to wide, expanded and shut. */
function resetStore() {
	const { result, unmount } = renderHook(() => ({
		sidebar: useSidebar(),
		drawer: useSidebarDrawer(),
	}));
	if (result.current.sidebar.collapsed)
		act(() => result.current.sidebar.toggle(true));
	act(() => result.current.drawer.closeDrawer());
	unmount();
}

beforeEach(() => {
	window.localStorage.clear();
	media = stubMedia();
	resetStore();
});
afterEach(() => {
	media.restore();
	resetStore();
	window.localStorage.clear();
});

/** The open tooltip's popup; base-ui gives it no role while its anchor has no layout. */
function tooltip() {
	return document.querySelector<HTMLElement>('[data-slot="tooltip-content"]');
}

function link(name: string | RegExp) {
	return screen.getByRole("link", { name });
}

function hrefOf(name: string | RegExp) {
	return link(name).getAttribute("href");
}

describe("Sidebar navigation inside an environment", () => {
	test("project pages link to the environment on screen", async () => {
		shellApi();
		await renderAt(<Sidebar user={ME as never} />, { path: "/p/p1/staging" });
		const project = within(screen.getByRole("navigation", { name: "Project" }));
		expect(project.getByRole("link", { name: "Canvas" })).toHaveAttribute(
			"href",
			"/p/p1/staging",
		);
		expect(project.getByRole("link", { name: /Deployments/ })).toHaveAttribute(
			"href",
			"/p/p1/staging/deployments",
		);
		expect(project.getByRole("link", { name: "Variables" })).toHaveAttribute(
			"href",
			"/p/p1/staging/variables",
		);
		expect(project.getByRole("link", { name: "Previews" })).toHaveAttribute(
			"href",
			"/p/p1/staging/previews",
		);
		expect(
			project.getByRole("link", { name: "Observability" }),
		).toHaveAttribute("href", "/p/p1/staging/observability");
		expect(
			project.getByRole("link", { name: "Project settings" }),
		).toHaveAttribute("href", "/p/p1/settings");
	});

	test("team pages link to the team's screens", async () => {
		shellApi();
		await renderAt(<Sidebar user={ME as never} />, { path: "/p/p1/staging" });
		const team = within(screen.getByRole("navigation", { name: "Team" }));
		expect(team.getByRole("link", { name: "Projects" })).toHaveAttribute(
			"href",
			"/",
		);
		expect(team.getByRole("link", { name: "Servers" })).toHaveAttribute(
			"href",
			"/servers",
		);
		expect(team.getByRole("link", { name: "Status pages" })).toHaveAttribute(
			"href",
			"/status-pages",
		);
		expect(team.getByRole("link", { name: "Team settings" })).toHaveAttribute(
			"href",
			"/settings",
		);
	});

	test("the brand link goes to all projects", async () => {
		shellApi();
		await renderAt(<Sidebar user={ME as never} />, { path: "/servers" });
		expect(hrefOf("NebulaCtrl — all projects")).toBe("/");
	});

	test("the page being read is the current row, and only that one", async () => {
		shellApi();
		await renderAt(<Sidebar user={ME as never} />, {
			path: "/p/p1/staging/deployments",
		});
		await waitFor(() =>
			expect(link(/Deployments/)).toHaveAttribute("data-current"),
		);
		const current = screen
			.getAllByRole("link")
			.filter((l) => l.hasAttribute("data-current"));
		expect(current).toHaveLength(1);
		expect(link("Canvas")).not.toHaveAttribute("data-current");
	});

	test("the canvas is current only on the exact canvas path", async () => {
		shellApi();
		await renderAt(<Sidebar user={ME as never} />, { path: "/p/p1/staging" });
		await waitFor(() => expect(link("Canvas")).toHaveAttribute("data-current"));
		expect(link(/Deployments/)).not.toHaveAttribute("data-current");
	});

	test("a team page is current on its own path", async () => {
		shellApi();
		await renderAt(<Sidebar user={ME as never} />, { path: "/servers" });
		await waitFor(() =>
			expect(link("Servers")).toHaveAttribute("data-current"),
		);
		expect(link("Projects")).not.toHaveAttribute("data-current");
	});

	test("Projects is current only on the home page", async () => {
		shellApi();
		await renderAt(<Sidebar user={ME as never} />, { path: "/" });
		await waitFor(() =>
			expect(link("Projects")).toHaveAttribute("data-current"),
		);
		expect(link("Servers")).not.toHaveAttribute("data-current");
	});

	test("pressing a row highlights it at once, before the page has changed", async () => {
		shellApi();
		const { router } = await renderAt(<Sidebar user={ME as never} />, {
			path: "/servers",
		});
		await waitFor(() =>
			expect(link("Servers")).toHaveAttribute("data-current"),
		);
		fireEvent.pointerDown(link("Status pages"), { button: 0 });
		expect(link("Status pages")).toHaveAttribute("data-current");
		expect(link("Servers")).not.toHaveAttribute("data-current");
		expect(router.state.location.pathname).toBe("/servers");
	});

	test("clicking a row navigates to it and it stays current", async () => {
		shellApi();
		const user = userEvent.setup();
		const { router } = await renderAt(<Sidebar user={ME as never} />, {
			path: "/servers",
		});
		await user.click(link("Status pages"));
		await waitFor(() =>
			expect(router.state.location.pathname).toBe("/status-pages"),
		);
		await waitFor(() =>
			expect(link("Status pages")).toHaveAttribute("data-current"),
		);
		expect(link("Servers")).not.toHaveAttribute("data-current");
	});

	test("a modified click does not move the highlight", async () => {
		shellApi();
		await renderAt(<Sidebar user={ME as never} />, { path: "/servers" });
		await waitFor(() =>
			expect(link("Servers")).toHaveAttribute("data-current"),
		);
		fireEvent.pointerDown(link("Status pages"), { button: 0, ctrlKey: true });
		fireEvent.pointerDown(link("Status pages"), { button: 2 });
		expect(link("Status pages")).not.toHaveAttribute("data-current");
		expect(link("Servers")).toHaveAttribute("data-current");
	});
});

describe("Sidebar outside a project", () => {
	test("project rows are disabled and say to open a project first", async () => {
		shellApi();
		await renderAt(<Sidebar user={ME as never} />, { path: "/servers" });
		await waitFor(() =>
			expect(screen.getAllByTitle("Open a project first").length).toBe(6),
		);
		const canvas = screen.getByRole("button", { name: "Canvas" });
		expect(canvas).toHaveAttribute("aria-disabled", "true");
		expect(screen.queryByRole("link", { name: "Canvas" })).toBeNull();
	});

	test("a team with no projects is told to create one", async () => {
		shellApi({ "GET /projects": { body: { items: [] } } });
		await renderAt(<Sidebar user={ME as never} />, { path: "/servers" });
		await waitFor(() =>
			expect(screen.getAllByTitle("Create a project first").length).toBe(6),
		);
	});

	test("the last environment the team had open is where project rows lead", async () => {
		window.localStorage.setItem(
			"nebulactrl:last-environment:t1",
			JSON.stringify({ projectId: "p2", envSlug: "production" }),
		);
		shellApi();
		await renderAt(<Sidebar user={ME as never} />, { path: "/servers" });
		await waitFor(() =>
			expect(link("Canvas")).toHaveAttribute("href", "/p/p2/production"),
		);
		expect(hrefOf("Project settings")).toBe("/p/p2/settings");
		expect(hrefOf(/Deployments/)).toBe("/p/p2/production/deployments");
	});

	test("visiting an environment is remembered for the next page outside a project", async () => {
		shellApi();
		await renderAt(<Sidebar user={ME as never} />, { path: "/p/p1/staging" });
		await waitFor(() =>
			expect(
				window.localStorage.getItem("nebulactrl:last-environment:t1"),
			).toBe(JSON.stringify({ projectId: "p1", envSlug: "staging" })),
		);
	});
});

describe("Sidebar counts", () => {
	test("shows how many servers the team has, and nothing for none", async () => {
		shellApi({
			"GET /servers": {
				body: make("PageServer", {
					items: [
						make("Server", { id: "s1", name: "a" }),
						make("Server", { id: "s2", name: "b" }),
					],
				}),
			},
		});
		await renderAt(<Sidebar user={ME as never} />, { path: "/servers" });
		await waitFor(() => expect(link(/Servers/)).toHaveTextContent("2"));
	});

	test("a team with no servers shows no zero", async () => {
		shellApi();
		await renderAt(<Sidebar user={ME as never} />, { path: "/servers" });
		await waitFor(() => expect(link("Servers")).toBeInTheDocument());
		expect(link("Servers")).toHaveTextContent(/^Servers$/);
	});

	test("shows the environment's deployment count, with a plus when there are more pages", async () => {
		shellApi({
			"GET /environments/:id/deployments": {
				body: make("PageEnvDeploymentView", {
					items: [
						make("EnvDeploymentView", { id: "d1" }),
						make("EnvDeploymentView", { id: "d2" }),
						make("EnvDeploymentView", { id: "d3" }),
					],
					nextCursor: "next",
				}),
			},
		});
		await renderAt(<Sidebar user={ME as never} />, { path: "/p/p1/staging" });
		await waitFor(() => expect(link(/Deployments/)).toHaveTextContent("3+"));
	});

	test("an exact count has no plus", async () => {
		shellApi({
			"GET /environments/:id/deployments": {
				body: make("PageEnvDeploymentView", {
					items: [make("EnvDeploymentView", { id: "d1" })],
				}),
			},
		});
		await renderAt(<Sidebar user={ME as never} />, { path: "/p/p1/staging" });
		await waitFor(() =>
			expect(link(/Deployments/)).toHaveTextContent("Deployments1"),
		);
	});
});

describe("Sidebar admin row", () => {
	test("is only for the instance admin", async () => {
		shellApi();
		await renderAt(<Sidebar user={ME as never} />, { path: "/servers" });
		expect(screen.queryByRole("navigation", { name: "Instance" })).toBeNull();
		expect(screen.queryByRole("link", { name: /Admin/ })).toBeNull();
	});

	test("leads to the admin area, and is current anywhere under it", async () => {
		shellApi({
			"GET /me": { body: ADMIN },
			"GET /system/update": {
				body: make("UpdateView", {
					available: false,
					isAdmin: true,
					current: "1.0.0",
				}),
			},
			"GET /instance/backup": {
				body: make("InstanceBackupView", {
					keyAcknowledgedAt: "2026-01-01T00:00:00Z",
				}),
			},
		});
		await renderAt(<Sidebar user={ADMIN as never} />, { path: "/admin/mcp" });
		const admin = link("Admin");
		expect(admin).toHaveAttribute("href", "/admin");
		expect(admin).toHaveAttribute("aria-current", "page");
	});

	test("is not current elsewhere", async () => {
		shellApi({
			"GET /me": { body: ADMIN },
			"GET /system/update": {
				body: make("UpdateView", {
					available: false,
					isAdmin: true,
					current: "1.0.0",
				}),
			},
			"GET /instance/backup": {
				body: make("InstanceBackupView", {
					keyAcknowledgedAt: "2026-01-01T00:00:00Z",
				}),
			},
		});
		await renderAt(<Sidebar user={ADMIN as never} />, { path: "/servers" });
		expect(link("Admin")).not.toHaveAttribute("aria-current");
	});

	test("an available update marks it and leads to the updates page", async () => {
		shellApi({
			"GET /me": { body: ADMIN },
			"GET /system/update": {
				body: make("UpdateView", {
					available: true,
					isAdmin: true,
					current: "1.0.0",
					latest: "1.3.0",
				}),
			},
			"GET /instance/backup": {
				body: make("InstanceBackupView", {
					keyAcknowledgedAt: "2026-01-01T00:00:00Z",
				}),
			},
		});
		await renderAt(<Sidebar user={ADMIN as never} />, { path: "/servers" });
		const admin = await screen.findByRole("link", {
			name: "Admin, Update available",
		});
		expect(admin).toHaveAttribute("href", "/admin/updates");
	});

	test("an unconfirmed secret key marks it and leads to the backup page", async () => {
		shellApi({
			"GET /me": { body: ADMIN },
			"GET /system/update": {
				body: make("UpdateView", {
					available: false,
					isAdmin: true,
					current: "1.0.0",
				}),
			},
			"GET /instance/backup": {
				body: make("InstanceBackupView", { enabled: false }),
			},
		});
		await renderAt(<Sidebar user={ADMIN as never} />, { path: "/servers" });
		const admin = await screen.findByRole("link", {
			name: "Admin, Secret key not confirmed stored",
		});
		expect(admin).toHaveAttribute("href", "/admin/backup");
	});
});

describe("Sidebar collapse", () => {
	function toggle() {
		return screen.getByRole("button", { name: /(Collapse|Expand) sidebar/ });
	}

	test("starts expanded with labels, the collapse key and its shortcut", async () => {
		shellApi();
		await renderAt(<Sidebar user={ME as never} />, { path: "/servers" });
		expect(
			screen.getByRole("complementary", { name: "Primary" }),
		).not.toHaveAttribute("data-collapsed");
		expect(toggle()).toHaveAccessibleName("Collapse sidebar");
		expect(toggle()).toHaveAttribute("aria-keyshortcuts", "Control+B Meta+B");
		expect(screen.getByText("Collapse")).toBeInTheDocument();
		expect(
			screen.getByRole("heading", { name: "Project" }),
		).toBeInTheDocument();
		expect(screen.getByRole("heading", { name: "Team" })).toBeInTheDocument();
	});

	test("collapsing leaves an icon rail that still names every row, and remembers it", async () => {
		shellApi();
		const user = userEvent.setup();
		await renderAt(<Sidebar user={ME as never} />, { path: "/servers" });
		await user.click(toggle());
		const aside = screen.getByRole("complementary", { name: "Primary" });
		expect(aside).toHaveAttribute("data-collapsed", "true");
		expect(toggle()).toHaveAccessibleName("Expand sidebar");
		expect(screen.queryByRole("heading", { name: "Team" })).toBeNull();
		// The label is still the link's accessible name.
		expect(hrefOf("Servers")).toBe("/servers");
		expect(screen.queryByText("Collapse")).toBeNull();
		expect(window.localStorage.getItem(KEY)).toBe("collapsed");
	});

	test("expanding brings the labels back", async () => {
		shellApi();
		const user = userEvent.setup();
		await renderAt(<Sidebar user={ME as never} />, { path: "/servers" });
		await user.click(toggle());
		await user.click(toggle());
		expect(
			screen.getByRole("complementary", { name: "Primary" }),
		).not.toHaveAttribute("data-collapsed");
		expect(screen.getByRole("heading", { name: "Team" })).toBeInTheDocument();
		expect(window.localStorage.getItem(KEY)).toBe("expanded");
	});

	test("a collapsed row shows its name in a tooltip on hover", async () => {
		shellApi();
		const user = userEvent.setup();
		await renderAt(<Sidebar user={ME as never} />, { path: "/servers" });
		await user.click(toggle());
		await user.hover(link("Servers"));
		await waitFor(() => expect(tooltip()).toHaveTextContent("Servers"));
	});

	test("the collapsed disabled rows explain why in their tooltip", async () => {
		shellApi();
		const user = userEvent.setup();
		await renderAt(<Sidebar user={ME as never} />, { path: "/servers" });
		await user.click(toggle());
		await user.hover(screen.getByRole("button", { name: /Canvas/ }));
		await waitFor(() =>
			expect(tooltip()).toHaveTextContent("Canvas — Open a project first"),
		);
	});

	test("a keyboard activation collapses without the glide", async () => {
		shellApi();
		const user = userEvent.setup();
		await renderAt(<Sidebar user={ME as never} />, { path: "/servers" });
		toggle().focus();
		await user.keyboard("{Enter}");
		expect(
			screen.getByRole("complementary", { name: "Primary" }),
		).toHaveAttribute("data-collapsed", "true");
		const { result } = renderHook(() => useSidebar());
		expect(result.current.animate).toBe(false);
	});

	test("starts collapsed when that was the stored choice", async () => {
		// The store reads storage once at load; collapse it through the UI first, then mount again.
		shellApi();
		const user = userEvent.setup();
		const first = await renderAt(<Sidebar user={ME as never} />, {
			path: "/servers",
		});
		await user.click(toggle());
		first.unmount();
		await renderAt(<Sidebar user={ME as never} />, { path: "/servers" });
		expect(
			screen.getByRole("complementary", { name: "Primary" }),
		).toHaveAttribute("data-collapsed", "true");
	});
});

function Drawer() {
	const { openDrawer } = useSidebarDrawer();
	return (
		<>
			<button type="button" onClick={openDrawer}>
				menu
			</button>
			<Sidebar user={ME as never} />
		</>
	);
}

describe("Sidebar as a drawer", () => {
	async function mountNarrow(path = "/servers") {
		media.set(NARROW, true);
		shellApi();
		const user = userEvent.setup();
		const view = await renderAt(<Drawer />, { path });
		return { user, ...view };
	}

	const aside = () => screen.getByLabelText("Primary", { selector: "aside" });

	test("is shut and inert until opened, with a scrim and no collapse key", async () => {
		await mountNarrow();
		expect(aside()).toHaveAttribute("data-drawer", "closed");
		expect(aside()).toHaveAttribute("inert");
		expect(
			screen.queryByRole("button", { name: /(Collapse|Expand) sidebar/ }),
		).toBeNull();
		expect(
			document.querySelector('button[aria-label="Close navigation"]'),
		).toHaveAttribute("aria-hidden", "true");
	});

	test("opening it shows the drawer and moves focus to its close button", async () => {
		const { user } = await mountNarrow();
		await user.click(screen.getByRole("button", { name: "menu" }));
		await waitFor(() => expect(aside()).toHaveAttribute("data-drawer", "open"));
		expect(aside()).not.toHaveAttribute("inert");
		await waitFor(() =>
			expect(
				within(aside()).getByRole("button", { name: "Close navigation" }),
			).toHaveFocus(),
		);
	});

	test("Escape closes it and hands focus back to the menu button", async () => {
		const { user } = await mountNarrow();
		const menu = screen.getByRole("button", { name: "menu" });
		await user.click(menu);
		await waitFor(() => expect(aside()).toHaveAttribute("data-drawer", "open"));
		await user.keyboard("{Escape}");
		await waitFor(() =>
			expect(aside()).toHaveAttribute("data-drawer", "closed"),
		);
		expect(menu).toHaveFocus();
	});

	test("the close button closes it", async () => {
		const { user } = await mountNarrow();
		await user.click(screen.getByRole("button", { name: "menu" }));
		await user.click(
			within(aside()).getByRole("button", { name: "Close navigation" }),
		);
		await waitFor(() =>
			expect(aside()).toHaveAttribute("data-drawer", "closed"),
		);
	});

	test("the scrim closes it", async () => {
		const { user } = await mountNarrow();
		await user.click(screen.getByRole("button", { name: "menu" }));
		await waitFor(() => expect(aside()).toHaveAttribute("data-drawer", "open"));
		const scrim = document.querySelector(
			'button[aria-label="Close navigation"][tabindex="-1"]',
		) as HTMLElement;
		expect(scrim).not.toHaveAttribute("aria-hidden");
		fireEvent.click(scrim);
		await waitFor(() =>
			expect(aside()).toHaveAttribute("data-drawer", "closed"),
		);
	});

	test("following a link goes there and closes it", async () => {
		const { user, router } = await mountNarrow("/servers");
		await user.click(screen.getByRole("button", { name: "menu" }));
		await waitFor(() => expect(aside()).toHaveAttribute("data-drawer", "open"));
		await user.click(
			within(aside()).getByRole("link", { name: "Status pages" }),
		);
		await waitFor(() =>
			expect(router.state.location.pathname).toBe("/status-pages"),
		);
		await waitFor(() =>
			expect(aside()).toHaveAttribute("data-drawer", "closed"),
		);
	});

	test("a link to the page already open closes it too", async () => {
		const { user, router } = await mountNarrow("/servers");
		await user.click(screen.getByRole("button", { name: "menu" }));
		await waitFor(() => expect(aside()).toHaveAttribute("data-drawer", "open"));
		await user.click(within(aside()).getByRole("link", { name: /^Servers/ }));
		await waitFor(() =>
			expect(aside()).toHaveAttribute("data-drawer", "closed"),
		);
		expect(router.state.location.pathname).toBe("/servers");
	});

	test("Escape inside an open menu is left for the menu", async () => {
		const { user } = await mountNarrow();
		await user.click(screen.getByRole("button", { name: "menu" }));
		await waitFor(() => expect(aside()).toHaveAttribute("data-drawer", "open"));
		await user.click(
			within(aside()).getByRole("button", { name: /Account menu for/ }),
		);
		await screen.findByRole("menu");
		await user.keyboard("{Escape}");
		await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
		expect(aside()).toHaveAttribute("data-drawer", "open");
	});

	test("Tab wraps from the last control to the first, Shift+Tab the other way", async () => {
		const { user } = await mountNarrow();
		await user.click(screen.getByRole("button", { name: "menu" }));
		await waitFor(() => expect(aside()).toHaveAttribute("data-drawer", "open"));
		const focusable = [
			...aside().querySelectorAll<HTMLElement>(
				'a[href], button:not([disabled]), [tabindex]:not([tabindex="-1"])',
			),
		].filter((el) => el.getAttribute("aria-disabled") !== "true");
		const first = focusable[0] as HTMLElement;
		const last = focusable.at(-1) as HTMLElement;
		last.focus();
		fireEvent.keyDown(last, { key: "Tab" });
		expect(first).toHaveFocus();
		fireEvent.keyDown(first, { key: "Tab", shiftKey: true });
		expect(last).toHaveFocus();
	});

	test("growing past the breakpoint leaves no open drawer behind", async () => {
		const { user } = await mountNarrow();
		await user.click(screen.getByRole("button", { name: "menu" }));
		await waitFor(() => expect(aside()).toHaveAttribute("data-drawer", "open"));
		media.set(NARROW, false);
		media.set(NARROW, true);
		expect(aside()).toHaveAttribute("data-drawer", "closed");
	});

	test("while narrow, the stored rail choice does not make the drawer an icon rail", async () => {
		window.localStorage.setItem(KEY, "collapsed");
		await mountNarrow();
		expect(aside()).not.toHaveAttribute("data-collapsed");
	});
});
