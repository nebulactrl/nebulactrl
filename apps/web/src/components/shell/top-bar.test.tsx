import {
	afterAll,
	afterEach,
	beforeAll,
	describe,
	expect,
	mock,
	test,
} from "bun:test";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { PALETTE_LABEL } from "./command-palette";
import { ShellActionsProvider } from "./shell-actions";
import { renderAt, shellApi, stubMedia } from "./shell-test-kit";
import { TopBar } from "./top-bar";

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

let media: ReturnType<typeof stubMedia> | undefined;
afterEach(() => {
	media?.restore();
	media = undefined;
});

function bar() {
	return (
		<ShellActionsProvider>
			<TopBar />
		</ShellActionsProvider>
	);
}

describe("TopBar outside a project", () => {
	test.each([
		["/servers", "Acme"],
		["/", "Acme"],
		["/account/profile", "Your account"],
		["/admin/updates", "This installation"],
		["/nowhere", "Page not found"],
	])("at %s the bar says %s", async (path, crumb) => {
		shellApi();
		await renderAt(bar(), { path });
		await waitFor(() =>
			expect(screen.getByRole("banner")).toHaveTextContent(crumb),
		);
		expect(screen.queryByRole("button", { name: /Switch project/ })).toBeNull();
	});

	test("has no menu button on a wide screen", async () => {
		shellApi();
		await renderAt(bar(), { path: "/servers" });
		expect(
			screen.queryByRole("button", { name: "Open navigation" }),
		).toBeNull();
	});

	test("both search buttons open the palette", async () => {
		shellApi();
		const user = userEvent.setup();
		await renderAt(bar(), { path: "/servers" });
		const buttons = screen.getAllByRole("button", { name: PALETTE_LABEL });
		expect(buttons).toHaveLength(2);
		await user.click(buttons[0] as HTMLElement);
		expect(
			await screen.findByRole("dialog", { name: "Command palette" }),
		).toBeInTheDocument();
		await user.keyboard("{Escape}");
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		await user.click(buttons[1] as HTMLElement);
		expect(
			await screen.findByRole("dialog", { name: "Command palette" }),
		).toBeInTheDocument();
	});

	test("the search field advertises its shortcut", async () => {
		shellApi();
		await renderAt(bar(), { path: "/servers" });
		const field = screen.getAllByRole("button", {
			name: PALETTE_LABEL,
		})[0] as HTMLElement;
		expect(field).toHaveAttribute("aria-keyshortcuts", "Control+K Meta+K");
		expect(field).toHaveTextContent(/(⌘|Ctrl)K/);
	});
});

describe("TopBar inside a project", () => {
	test("shows the project and environment switchers and the deploy key", async () => {
		shellApi();
		await renderAt(bar(), { path: "/p/p1/production" });
		expect(
			await screen.findByRole("button", {
				name: "Project: Shop. Switch project",
			}),
		).toBeInTheDocument();
		expect(
			await screen.findByRole("button", { name: /Production/ }),
		).toBeInTheDocument();
		expect(
			await screen.findByRole("button", { name: "Deploy options" }),
		).toBeInTheDocument();
	});

	test("a project's settings page has the project switcher but no environment", async () => {
		shellApi();
		await renderAt(bar(), { path: "/p/p1/settings" });
		expect(
			await screen.findByRole("button", {
				name: "Project: Shop. Switch project",
			}),
		).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Deploy options" })).toBeNull();
		expect(
			screen.queryByRole("button", { name: /Production|staging/ }),
		).toBeNull();
	});
});

describe("TopBar on a narrow screen", () => {
	test("shows a menu button that opens the navigation drawer", async () => {
		media = stubMedia({ "(max-width: 759px)": true });
		shellApi();
		await renderAt(bar(), { path: "/servers" });
		const button = screen.getByRole("button", { name: "Open navigation" });
		expect(button).toHaveAttribute("aria-controls", "primary-navigation");
		expect(button).toHaveAttribute("aria-expanded", "false");
		fireEvent.click(button);
		await waitFor(() =>
			expect(button).toHaveAttribute("aria-expanded", "true"),
		);
		// leave the shared drawer store shut for the next test
		fireEvent.click(button);
	});
});
