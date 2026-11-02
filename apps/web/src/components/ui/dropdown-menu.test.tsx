import { describe, expect, it, mock } from "bun:test";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuGroup,
	DropdownMenuItem,
	DropdownMenuLabel,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "#/components/ui/dropdown-menu";

function Example({
	onRename = () => {},
	onDelete = () => {},
	onOpenChange,
	contentProps,
	open,
}: {
	onRename?: () => void;
	onDelete?: () => void;
	onOpenChange?: (open: boolean, details: { reason: string }) => void;
	contentProps?: Partial<React.ComponentProps<typeof DropdownMenuContent>>;
	open?: boolean;
}) {
	return (
		<>
			<button type="button">outside</button>
			<DropdownMenu
				{...(open === undefined ? {} : { open })}
				onOpenChange={onOpenChange}
			>
				<DropdownMenuTrigger>Actions</DropdownMenuTrigger>
				<DropdownMenuContent {...contentProps}>
					<DropdownMenuGroup>
						<DropdownMenuLabel>Project</DropdownMenuLabel>
						<DropdownMenuItem onClick={onRename}>Rename</DropdownMenuItem>
						<DropdownMenuItem disabled>Archive</DropdownMenuItem>
					</DropdownMenuGroup>
					<DropdownMenuSeparator />
					<DropdownMenuItem variant="destructive" onClick={onDelete}>
						Delete
					</DropdownMenuItem>
				</DropdownMenuContent>
			</DropdownMenu>
		</>
	);
}

const queryMenu = () => screen.queryByRole("menu");

describe("DropdownMenu", () => {
	it("opens from its trigger as a menu with its items", async () => {
		const user = userEvent.setup();
		render(<Example />);
		const trigger = screen.getByRole("button", { name: "Actions" });
		expect(trigger).toHaveAttribute("aria-haspopup", "menu");
		expect(trigger).toHaveAttribute("aria-expanded", "false");
		expect(queryMenu()).toBeNull();

		await user.click(trigger);

		const menu = await screen.findByRole("menu");
		expect(trigger).toHaveAttribute("aria-expanded", "true");
		expect(menu).toHaveAttribute("data-slot", "dropdown-menu-content");
		expect(screen.getAllByRole("menuitem").map((i) => i.textContent)).toEqual([
			"Rename",
			"Archive",
			"Delete",
		]);
	});

	it("choosing an item runs its callback and closes the menu", async () => {
		const user = userEvent.setup();
		const onRename = mock(() => {});
		const onDelete = mock(() => {});
		const onOpenChange = mock((_o: boolean, _d: { reason: string }) => {});
		render(
			<Example
				onRename={onRename}
				onDelete={onDelete}
				onOpenChange={onOpenChange}
			/>,
		);
		await user.click(screen.getByRole("button", { name: "Actions" }));

		await user.click(await screen.findByRole("menuitem", { name: "Rename" }));

		expect(onRename).toHaveBeenCalledTimes(1);
		expect(onDelete).not.toHaveBeenCalled();
		await waitFor(() => expect(queryMenu()).toBeNull());
		expect(onOpenChange.mock.calls.at(-1)?.[0]).toBe(false);
		expect(onOpenChange.mock.calls.at(-1)?.[1].reason).toBe("item-press");
	});

	it("a disabled item is announced as disabled and does nothing when clicked", async () => {
		const user = userEvent.setup();
		const onOpenChange = mock((_o: boolean, _d: { reason: string }) => {});
		render(<Example onOpenChange={onOpenChange} />);
		await user.click(screen.getByRole("button", { name: "Actions" }));
		const archive = await screen.findByRole("menuitem", { name: "Archive" });
		expect(archive).toHaveAttribute("aria-disabled", "true");
		expect(archive).toHaveAttribute("data-disabled");

		await user.click(archive);

		expect(queryMenu()).not.toBeNull();
		expect(onOpenChange.mock.calls.map((c) => c[0])).toEqual([true]);
	});

	it("ArrowDown on the trigger opens it with the first item highlighted, arrows move the highlight and Enter activates", async () => {
		const user = userEvent.setup();
		const onDelete = mock(() => {});
		const onRename = mock(() => {});
		render(<Example onDelete={onDelete} onRename={onRename} />);
		screen.getByRole("button", { name: "Actions" }).focus();
		await user.keyboard("{ArrowDown}");
		const rename = await screen.findByRole("menuitem", { name: "Rename" });
		const del = screen.getByRole("menuitem", { name: "Delete" });
		await waitFor(() =>
			expect(rename.hasAttribute("data-highlighted")).toBe(true),
		);
		expect(del.hasAttribute("data-highlighted")).toBe(false);

		await user.keyboard("{ArrowUp}");
		await waitFor(() =>
			expect(del.hasAttribute("data-highlighted")).toBe(true),
		);
		expect(rename.hasAttribute("data-highlighted")).toBe(false);

		await user.keyboard("{Enter}");
		expect(onDelete).toHaveBeenCalledTimes(1);
		expect(onRename).not.toHaveBeenCalled();
		await waitFor(() => expect(queryMenu()).toBeNull());
	});

	it("Escape closes the menu and returns focus to the trigger", async () => {
		const user = userEvent.setup();
		const onOpenChange = mock((_o: boolean, _d: { reason: string }) => {});
		render(<Example onOpenChange={onOpenChange} />);
		const trigger = screen.getByRole("button", { name: "Actions" });
		await user.click(trigger);
		await screen.findByRole("menu");

		await user.keyboard("{Escape}");

		await waitFor(() => expect(queryMenu()).toBeNull());
		expect(onOpenChange.mock.calls.at(-1)?.[1].reason).toBe("escape-key");
		await waitFor(() => expect(trigger).toHaveFocus());
	});

	it("a press outside closes the menu", async () => {
		const user = userEvent.setup();
		const onOpenChange = mock((_o: boolean, _d: { reason: string }) => {});
		render(<Example onOpenChange={onOpenChange} />);
		await user.click(screen.getByRole("button", { name: "Actions" }));
		await screen.findByRole("menu");

		await user.click(screen.getByRole("button", { name: "outside" }));

		await waitFor(() => expect(queryMenu()).toBeNull());
		expect(onOpenChange.mock.calls.at(-1)?.[1].reason).toBe("outside-press");
	});

	it("marks the menu data-instant when opened by key, not by pointer", async () => {
		const user = userEvent.setup();
		const { unmount } = render(<Example />);
		await user.click(screen.getByRole("button", { name: "Actions" }));
		expect(await screen.findByRole("menu")).not.toHaveAttribute("data-instant");
		unmount();

		render(<Example />);
		screen.getByRole("button", { name: "Actions" }).focus();
		await user.keyboard("{Enter}");
		expect(await screen.findByRole("menu")).toHaveAttribute("data-instant");
		await user.click(document.body);
	});

	it("controlled: announces the request but follows `open`", async () => {
		const user = userEvent.setup();
		const onOpenChange = mock((_o: boolean, _d: { reason: string }) => {});
		render(<Example open={false} onOpenChange={onOpenChange} />);

		await user.click(screen.getByRole("button", { name: "Actions" }));

		expect(onOpenChange.mock.calls[0]?.[0]).toBe(true);
		expect(queryMenu()).toBeNull();
	});

	it("controlled open renders the menu at once", async () => {
		render(<Example open />);
		expect(await screen.findByRole("menu")).toBeInTheDocument();
	});

	it("positions on the requested side, defaulting to bottom/start", async () => {
		const user = userEvent.setup();
		const { unmount } = render(<Example />);
		await user.click(screen.getByRole("button", { name: "Actions" }));
		const menu = await screen.findByRole("menu");
		expect(menu.parentElement).toHaveAttribute("data-side", "bottom");
		expect(menu.parentElement).toHaveAttribute("data-align", "start");
		unmount();

		render(
			<Example
				contentProps={{ side: "top", align: "end", className: "mine" }}
			/>,
		);
		await user.click(screen.getByRole("button", { name: "Actions" }));
		const placed = await screen.findByRole("menu");
		expect(placed.parentElement).toHaveAttribute("data-side", "top");
		expect(placed.parentElement).toHaveAttribute("data-align", "end");
		expect(placed).toHaveClass("mine");
	});

	it("items carry their variant, inset, label and separator slots", async () => {
		const user = userEvent.setup();
		render(
			<DropdownMenu open>
				<DropdownMenuTrigger>t</DropdownMenuTrigger>
				<DropdownMenuContent>
					<DropdownMenuGroup>
						<DropdownMenuLabel inset>Section</DropdownMenuLabel>
						<DropdownMenuItem inset>Indented</DropdownMenuItem>
					</DropdownMenuGroup>
					<DropdownMenuSeparator />
					<DropdownMenuItem variant="destructive">Remove</DropdownMenuItem>
				</DropdownMenuContent>
			</DropdownMenu>,
		);
		await screen.findByRole("menu");
		expect(screen.getByText("Section")).toHaveAttribute(
			"data-slot",
			"dropdown-menu-label",
		);
		expect(screen.getByText("Section")).toHaveAttribute("data-inset", "true");
		expect(screen.getByRole("menuitem", { name: "Indented" })).toHaveAttribute(
			"data-inset",
			"true",
		);
		expect(screen.getByRole("menuitem", { name: "Indented" })).toHaveAttribute(
			"data-variant",
			"default",
		);
		expect(screen.getByRole("menuitem", { name: "Remove" })).toHaveAttribute(
			"data-variant",
			"destructive",
		);
		expect(screen.getByRole("separator")).toHaveAttribute(
			"data-slot",
			"dropdown-menu-separator",
		);
		expect(screen.getByRole("group")).toHaveAttribute(
			"data-slot",
			"dropdown-menu-group",
		);
		void user;
	});

	it("an item rendered as a <button> activates like any other item", async () => {
		const user = userEvent.setup();
		const onClick = mock(() => {});
		render(
			<DropdownMenu>
				<DropdownMenuTrigger>Open</DropdownMenuTrigger>
				<DropdownMenuContent>
					<DropdownMenuItem render={<button type="button" />} onClick={onClick}>
						As button
					</DropdownMenuItem>
				</DropdownMenuContent>
			</DropdownMenu>,
		);
		await user.click(screen.getByRole("button", { name: "Open" }));
		await user.click(
			await screen.findByRole("menuitem", { name: "As button" }),
		);
		expect(onClick).toHaveBeenCalledTimes(1);
	});
});
