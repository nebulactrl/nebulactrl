import { describe, expect, it } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
	ContextMenu,
	ContextMenuContent,
} from "#/components/motion/context-menu";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuTrigger,
} from "#/components/ui/dropdown-menu";
import { renderWithProviders } from "#/test/render";
import {
	type CardAction,
	CardContextRows,
	CardDropdownRows,
} from "./card-menu";

const ROWS: [string, CardAction][] = [
	["Open", "open"],
	["Settings", "settings"],
	["Copy project ID", "copy-id"],
	["Delete project…", "delete"],
];

describe("CardDropdownRows", () => {
	async function open(run: (action: CardAction) => void) {
		const user = userEvent.setup();
		await renderWithProviders(
			<DropdownMenu>
				<DropdownMenuTrigger>Actions</DropdownMenuTrigger>
				<DropdownMenuContent>
					<CardDropdownRows run={run} />
				</DropdownMenuContent>
			</DropdownMenu>,
		);
		await user.click(screen.getByRole("button", { name: "Actions" }));
		return user;
	}

	it("lists the safe rows first and the delete row last, below a separator", async () => {
		await open(() => {});
		const items = await screen.findAllByRole("menuitem");
		expect(items.map((item) => item.textContent)).toEqual(
			ROWS.map(([label]) => label),
		);
		expect(screen.getAllByRole("separator")).toHaveLength(1);
	});

	it.each(ROWS)("%s runs its own action", async (label, action) => {
		const ran: CardAction[] = [];
		const user = await open((next) => ran.push(next));
		await user.click(await screen.findByRole("menuitem", { name: label }));
		expect(ran).toEqual([action]);
	});
});

describe("CardContextRows", () => {
	async function open(run: (action: CardAction) => void) {
		await renderWithProviders(
			<ContextMenu defaultOpen>
				<ContextMenuContent ariaLabel="Actions">
					<CardContextRows run={run} />
				</ContextMenuContent>
			</ContextMenu>,
		);
		return userEvent.setup();
	}

	it("offers the same rows in the same order", async () => {
		await open(() => {});
		const menu = await screen.findByRole("menu", { name: "Actions" });
		const items = within(menu).getAllByRole("menuitem");
		expect(items.map((item) => item.textContent)).toEqual(
			ROWS.map(([label]) => label),
		);
	});

	it.each(ROWS)("%s runs its own action", async (label, action) => {
		const ran: CardAction[] = [];
		const user = await open((next) => ran.push(next));
		await user.click(await screen.findByRole("menuitem", { name: label }));
		expect(ran).toEqual([action]);
	});
});
