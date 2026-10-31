import { useMemo, useState } from "react";
import {
	CommandPalette as BeCommandPalette,
	type CommandItem,
} from "#/components/motion/command-palette";
import { useDocsQueryItems } from "#/features/docs/docs-search-source";
import { useCommandRegistry } from "./palette-registry";

const isMac =
	typeof navigator !== "undefined" &&
	/Mac|iPhone|iPad|iPod/.test(navigator.platform);

/** What the palette is called everywhere: the prompt, and the top bar's field that opens it. */
export const PALETTE_LABEL = "Search projects, services, docs and actions";
export const PALETTE_PROMPT = `${PALETTE_LABEL}…`;

/** "⌘" on a Mac keyboard, "Ctrl" everywhere else. */
export const MOD_KEY = isMac ? "⌘" : "Ctrl";

/** How many used rows are remembered to weight a typed search. */
const RECENT_KEPT = 24;
/** Rows the empty prompt's "Recent" section shows. */
const RECENT_SHOWN = 3;

/**
 * The ⌘K / Ctrl+K palette. The beUI palette owns keyboard navigation, fuzzy
 * filtering and the gliding active row; this adapter feeds it whatever the
 * features have registered through `palette-registry` (the shell registers
 * navigation; the canvas and the inspector add service verbs later).
 */
export function CommandPalette({
	open,
	onOpenChange,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
}) {
	const commands = useCommandRegistry();
	// The docs index is its own chunk; it loads the first time the palette opens.
	const docsItems = useDocsQueryItems(open);
	// Most recent first; the empty prompt shows the first few, a typed
	// search weighs all of them.
	const [recentIds, setRecentIds] = useState<string[]>([]);

	const { items, initialItems } = useMemo(() => {
		const remember = (id: string) =>
			setRecentIds((previous) =>
				[id, ...previous.filter((candidate) => candidate !== id)].slice(
					0,
					RECENT_KEPT,
				),
			);
		const items: CommandItem[] = commands.map((command) => ({
			id: command.id,
			label: command.label,
			group: command.group ?? "Action",
			...(command.hint ? { hint: command.hint } : {}),
			...(command.keywords ? { keywords: command.keywords } : {}),
			...(command.icon ? { icon: command.icon } : {}),
			...(command.shortcut ? { shortcut: command.shortcut } : {}),
			onSelect: () => {
				remember(command.id);
				command.run();
			},
		}));
		const byId = new Map(items.map((item) => [item.id, item]));
		// The empty prompt offers what can be done now. Disabled and rare rows
		// are one keystroke away, but they never lead.
		const hidden = new Set(
			commands
				.filter((command) => command.disabled || command.searchOnly)
				.map((command) => command.id),
		);
		const recent = recentIds.slice(0, RECENT_SHOWN).flatMap((id) => {
			const item = byId.get(id);
			return item ? [{ ...item, group: "Recent" }] : [];
		});
		const recentSet = new Set(recent.map((item) => item.id));
		return {
			items,
			initialItems: [
				...recent,
				...items.filter(
					(item) => !(recentSet.has(item.id) || hidden.has(item.id)),
				),
			],
		};
	}, [commands, recentIds]);

	return (
		<BeCommandPalette
			items={items}
			initialItems={initialItems}
			queryItems={docsItems}
			recent={recentIds}
			shortcut="k"
			placeholder={PALETTE_PROMPT}
			emptyMessage="Nothing to jump to yet."
			open={open}
			onOpenChange={onOpenChange}
		/>
	);
}
