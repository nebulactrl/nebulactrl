import { afterEach, describe, expect, it } from "bun:test";
import { useLocation } from "@tanstack/react-router";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuTrigger,
} from "#/components/ui/dropdown-menu";
import { renderWithProviders } from "#/test/render";
import { ProjectMenuGroup } from "./project-menu-group";

const PROJECT = { id: "p1", name: "Shop" };
const realClipboard = Object.getOwnPropertyDescriptor(navigator, "clipboard");

afterEach(() => {
	if (realClipboard)
		Object.defineProperty(navigator, "clipboard", realClipboard);
	else Reflect.deleteProperty(navigator, "clipboard");
});

function Where() {
	const location = useLocation();
	return <p data-testid="where">{`${location.pathname}#${location.hash}`}</p>;
}

async function openMenu(onRename: () => void) {
	const user = userEvent.setup();
	await renderWithProviders(
		<>
			<Where />
			<DropdownMenu>
				<DropdownMenuTrigger>Menu</DropdownMenuTrigger>
				<DropdownMenuContent>
					<ProjectMenuGroup project={PROJECT} onRename={onRename} />
				</DropdownMenuContent>
			</DropdownMenu>
		</>,
		{ path: "/p/p1/prod" },
	);
	await user.click(screen.getByRole("button", { name: "Menu" }));
	return user;
}

describe("ProjectMenuGroup", () => {
	it("titles its rows as the project's own", async () => {
		await openMenu(() => {});
		expect(await screen.findByText("Project")).toBeInTheDocument();
		expect(
			screen.getAllByRole("menuitem").map((item) => item.textContent),
		).toEqual(["Rename project…", "Copy project ID", "Environments"]);
		expect(screen.queryByText(/delete/i)).not.toBeInTheDocument();
	});

	it("only asks to rename: the dialog is the caller's", async () => {
		let asked = 0;
		const user = await openMenu(() => {
			asked += 1;
		});
		await user.click(
			await screen.findByRole("menuitem", { name: "Rename project…" }),
		);
		expect(asked).toBe(1);
	});

	it("copies the project id and says so", async () => {
		const user = await openMenu(() => {});
		await user.click(
			await screen.findByRole("menuitem", { name: "Copy project ID" }),
		);
		expect(await screen.findByText("Copied")).toBeInTheDocument();
		expect(await navigator.clipboard.readText()).toBe("p1");
	});

	it("tells the reader how to copy by hand when the clipboard is blocked", async () => {
		const user = await openMenu(() => {});
		// user-event installs its own clipboard on setup; the browser has none here.
		Object.defineProperty(navigator, "clipboard", {
			configurable: true,
			value: undefined,
		});
		await user.click(
			await screen.findByRole("menuitem", { name: "Copy project ID" }),
		);
		expect(await screen.findByText("Nothing was copied")).toBeInTheDocument();
		expect(screen.getByText(/copy by hand: p1/)).toBeInTheDocument();
	});

	it("opens the environments section of the project's settings", async () => {
		const user = await openMenu(() => {});
		await user.click(
			await screen.findByRole("menuitem", { name: "Environments" }),
		);
		await screen.findByText("/p/p1/settings#environments");
	});
});
