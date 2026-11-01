import {
	ArrowUpRightIcon,
	CopyIcon,
	type LucideIcon,
	SettingsIcon,
	Trash2Icon,
} from "lucide-react";
import {
	ContextMenuItem,
	ContextMenuSeparator,
} from "#/components/motion/context-menu";
import {
	DropdownMenuItem,
	DropdownMenuSeparator,
} from "#/components/ui/dropdown-menu";

/** What the menu on a project card can do. */
export type CardAction = "open" | "settings" | "copy-id" | "delete";

/** The rows in order; the delete row sits below a separator. */
const SAFE_ROWS = [
	{ action: "open", label: "Open", icon: ArrowUpRightIcon },
	{ action: "settings", label: "Settings", icon: SettingsIcon },
	{ action: "copy-id", label: "Copy project ID", icon: CopyIcon },
] as const satisfies readonly {
	action: CardAction;
	label: string;
	icon: LucideIcon;
}[];

const DELETE_LABEL = "Delete project…";

/** The card's actions as dropdown rows, for the `⋯` button. */
export function CardDropdownRows({ run }: { run(action: CardAction): void }) {
	return (
		<>
			{SAFE_ROWS.map(({ action, label, icon: Icon }) => (
				<DropdownMenuItem
					key={action}
					render={<button type="button" />}
					onClick={() => run(action)}
				>
					<Icon aria-hidden />
					{label}
				</DropdownMenuItem>
			))}
			<DropdownMenuSeparator />
			<DropdownMenuItem
				render={<button type="button" />}
				variant="destructive"
				onClick={() => run("delete")}
			>
				<Trash2Icon aria-hidden />
				{DELETE_LABEL}
			</DropdownMenuItem>
		</>
	);
}

/** The same actions as context-menu rows, for a right-click on the card. */
export function CardContextRows({ run }: { run(action: CardAction): void }) {
	return (
		<>
			{SAFE_ROWS.map(({ action, label, icon: Icon }) => (
				<ContextMenuItem
					key={action}
					textValue={label}
					onSelect={() => run(action)}
				>
					<Icon aria-hidden />
					{label}
				</ContextMenuItem>
			))}
			<ContextMenuSeparator />
			<ContextMenuItem
				tone="destructive"
				textValue={DELETE_LABEL}
				onSelect={() => run("delete")}
			>
				<Trash2Icon aria-hidden />
				{DELETE_LABEL}
			</ContextMenuItem>
		</>
	);
}
