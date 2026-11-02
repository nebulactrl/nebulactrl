import {
	afterAll,
	afterEach,
	beforeAll,
	beforeEach,
	describe,
	expect,
	mock,
	test,
} from "bun:test";
import {
	act,
	fireEvent,
	renderHook,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { problem } from "#/test/factories";
import { AppShell } from "./app-shell";
import { ME, renderAt, shellApi, stubMedia, where } from "./shell-test-kit";
import { useSidebar, useSidebarDrawer } from "./use-sidebar";

beforeAll(() => {
	mock.module("virtual:docs-search", () => ({
		default: { pages: [], rows: [] },
	}));
});
afterAll(() => {
	mock.module("virtual:docs-search", () => ({
		default: { pages: [], rows: [] },
	}));
});

const NARROW = "(max-width: 759px)";
let media: ReturnType<typeof stubMedia>;

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

function shell() {
	return (
		<AppShell user={ME as never}>
			<h1>Page content</h1>
		</AppShell>
	);
}

const aside = () => screen.getByRole("complementary", { name: "Primary" });

describe("AppShell frame", () => {
	test("puts the sidebar, the top bar and the page in one frame, with the page in the main landmark", async () => {
		shellApi();
		await renderAt(shell(), { path: "/servers" });
		expect(aside()).toBeInTheDocument();
		expect(screen.getByRole("banner")).toBeInTheDocument();
		const main = screen.getByRole("main");
		expect(main).toHaveAttribute("id", "main-content");
		expect(
			within(main).getByRole("heading", { name: "Page content" }),
		).toBeInTheDocument();
	});

	test("the first thing a keyboard reaches is a link that skips the chrome", async () => {
		shellApi();
		const user = userEvent.setup();
		await renderAt(shell(), { path: "/servers" });
		await user.tab();
		const skip = screen.getByRole("link", { name: "Skip to content" });
		expect(skip).toHaveFocus();
		expect(skip).toHaveAttribute("href", "#main-content");
	});

	test("the top bar names the team's page", async () => {
		shellApi();
		await renderAt(shell(), { path: "/servers" });
		await waitFor(() =>
			expect(screen.getByRole("banner")).toHaveTextContent("Acme"),
		);
	});

	test("inside a project the top bar offers its switchers and the sidebar its pages", async () => {
		shellApi();
		await renderAt(shell(), { path: "/p/p1/staging" });
		expect(
			await screen.findByRole("button", {
				name: "Project: Shop. Switch project",
			}),
		).toBeInTheDocument();
		expect(screen.getByRole("link", { name: "Canvas" })).toHaveAttribute(
			"href",
			"/p/p1/staging",
		);
	});

	test("no live-updates warning while the feed is up", async () => {
		const api = shellApi();
		await renderAt(shell(), { path: "/servers" });
		await waitFor(() =>
			expect(api.calls.some((c) => c.path === "/events")).toBe(true),
		);
		expect(screen.queryByText(/Live updates paused/)).toBeNull();
	});

	test("warns that live updates are paused when the feed cannot connect", async () => {
		shellApi({ "GET /events": problem(500, { code: "internal" }) });
		await renderAt(shell(), { path: "/servers" });
		expect(
			await screen.findByText("Live updates paused — retrying"),
		).toBeInTheDocument();
	});
});

describe("AppShell shortcuts", () => {
	test("Ctrl+B collapses the sidebar and Ctrl+B again expands it, remembering the choice", async () => {
		shellApi();
		await renderAt(shell(), { path: "/servers" });
		fireEvent.keyDown(window, { key: "b", ctrlKey: true });
		await waitFor(() =>
			expect(aside()).toHaveAttribute("data-collapsed", "true"),
		);
		expect(window.localStorage.getItem("nebulactrl:sidebar")).toBe("collapsed");
		fireEvent.keyDown(window, { key: "b", ctrlKey: true });
		await waitFor(() => expect(aside()).not.toHaveAttribute("data-collapsed"));
	});

	test("Ctrl+B typed into a field does not touch the sidebar", async () => {
		shellApi();
		await renderAt(
			<AppShell user={ME as never}>
				<input aria-label="name" />
			</AppShell>,
			{ path: "/servers" },
		);
		fireEvent.keyDown(screen.getByLabelText("name"), {
			key: "b",
			ctrlKey: true,
			bubbles: true,
		});
		expect(aside()).not.toHaveAttribute("data-collapsed");
	});

	test("Ctrl+K opens the palette with the shell's entries, and Enter runs the best match", async () => {
		shellApi();
		const { router } = await renderAt(shell(), { path: "/" });
		fireEvent.keyDown(window, { key: "k", ctrlKey: true });
		const input = await screen.findByRole("combobox");
		await waitFor(() =>
			expect(screen.getAllByRole("option").length).toBeGreaterThan(3),
		);
		fireEvent.change(input, { target: { value: "status pages" } });
		fireEvent.keyDown(input, { key: "Enter" });
		await waitFor(() => expect(where(router)).toBe("/status-pages"));
		expect(
			screen.queryByRole("dialog", { name: "Command palette" }),
		).toBeNull();
	});

	test("the top bar's search button opens the same palette", async () => {
		shellApi();
		const user = userEvent.setup();
		await renderAt(shell(), { path: "/servers" });
		await user.click(
			screen.getAllByRole("button", {
				name: /Search projects, services, docs and actions/,
			})[0] as HTMLElement,
		);
		expect(
			await screen.findByRole("dialog", { name: "Command palette" }),
		).toBeInTheDocument();
	});

	test("the fleet's entries are in the palette", async () => {
		shellApi();
		await renderAt(shell(), { path: "/servers" });
		fireEvent.keyDown(window, { key: "k", ctrlKey: true });
		const input = await screen.findByRole("combobox");
		fireEvent.change(input, { target: { value: "Open fleet" } });
		expect(
			await screen.findByRole("option", { name: /Open fleet/ }),
		).toBeInTheDocument();
	});
});

describe("AppShell on a narrow screen", () => {
	test("the menu button opens the drawer and the page behind it goes inert", async () => {
		media.set(NARROW, true);
		shellApi();
		const user = userEvent.setup();
		await renderAt(shell(), { path: "/servers" });
		const column = screen.getByRole("main").parentElement as HTMLElement;
		expect(column).not.toHaveAttribute("inert");
		await user.click(screen.getByRole("button", { name: "Open navigation" }));
		await waitFor(() => expect(aside()).toHaveAttribute("data-drawer", "open"));
		expect(column).toHaveAttribute("inert");
		await user.keyboard("{Escape}");
		await waitFor(() => expect(column).not.toHaveAttribute("inert"));
	});

	test("Ctrl+B opens the drawer instead of collapsing the rail", async () => {
		media.set(NARROW, true);
		shellApi();
		await renderAt(shell(), { path: "/servers" });
		fireEvent.keyDown(window, { key: "b", ctrlKey: true });
		await waitFor(() => expect(aside()).toHaveAttribute("data-drawer", "open"));
		expect(window.localStorage.getItem("nebulactrl:sidebar")).toBeNull();
	});
});
