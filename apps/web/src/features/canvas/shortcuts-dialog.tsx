import { Fragment, memo } from "react";
import {
	Dialog,
	DialogBody,
	DialogContent,
	DialogDescription,
	DialogHeader,
	DialogTitle,
} from "#/components/ui/dialog";
import { Kbd } from "#/components/ui/kbd";
import { MANUAL_DASH, STAGED_DASH } from "./lib/cable";

interface Shortcut {
	keys: string[];
	action: string;
	/** Keys pressed one after the other ("g" then "o"), not together. */
	chord?: boolean;
}

interface ShortcutGroup {
	title: string;
	note?: string;
	shortcuts: readonly Shortcut[];
}

const GROUPS: readonly ShortcutGroup[] = [
	{
		title: "Canvas",
		shortcuts: [
			{ keys: ["⌘", "F"], action: "Search services" },
			{ keys: ["⌘", "A"], action: "Select all services" },
			{ keys: ["tab"], action: "Move through the services, then the Internet" },
			{ keys: ["←", "↑", "↓", "→"], action: "Move between services" },
			{ keys: ["↵"], action: "Open the selected service" },
			{
				keys: ["⌫"],
				action: "Delete the selected service (confirmed in its settings)",
			},
			{ keys: ["esc"], action: "Close the inspector, search or selection" },
			{ keys: ["+", "−"], action: "Zoom in or out" },
			{ keys: ["0"], action: "Zoom to 100%" },
			{ keys: ["1"], action: "Fit all services" },
			{ keys: ["⇧", "click"], action: "Select several services" },
			{ keys: ["⇧", "drag"], action: "Select an area" },
			{
				keys: ["right-click"],
				action:
					"Actions for a card, the Internet, a cable, a group or a selection; Create at the cursor",
			},
			{
				keys: ["⇧", "F10"],
				action:
					"Open that menu for the focused card or the Internet (also the ContextMenu key); a card's menu lists its cables",
			},
		],
	},
	{
		title: "Inspector tabs",
		note: "With a service open. Press the second key within a second of the first.",
		shortcuts: [
			{ keys: ["g", "o"], action: "Overview", chord: true },
			{ keys: ["g", "d"], action: "Deployments", chord: true },
			{ keys: ["g", "v"], action: "Variables", chord: true },
			{ keys: ["g", "m"], action: "Metrics", chord: true },
			{ keys: ["g", "a"], action: "Analytics", chord: true },
			{ keys: ["g", "b"], action: "Backups", chord: true },
			{ keys: ["g", "l"], action: "Logs", chord: true },
			{ keys: ["g", "s"], action: "Settings", chord: true },
		],
	},
	{
		title: "Anywhere",
		shortcuts: [
			{ keys: ["⌘", "K"], action: "Open the command palette" },
			{ keys: ["⌘", "B"], action: "Collapse or expand the sidebar" },
			{ keys: ["?"], action: "Show this list" },
		],
	},
];

/** What a cable's line says, drawn as `service-edge.tsx` draws it. */
const CABLES: readonly { meaning: string; dash?: string }[] = [
	{ meaning: "A variable references the other service" },
	{
		meaning: "A staged reference: live after the next deploy",
		dash: STAGED_DASH,
	},
	{ meaning: "Linked by hand: no variable references it", dash: MANUAL_DASH },
];

/** Static, so it is built once instead of on every render of the canvas. */
const CONTENT = (
	<DialogContent>
		<DialogHeader>
			<DialogTitle>Keyboard shortcuts</DialogTitle>
			<DialogDescription>
				On the canvas, in the inspector and anywhere in the app. Ctrl works in
				place of ⌘.
			</DialogDescription>
		</DialogHeader>
		<DialogBody className="flex flex-col gap-5">
			{GROUPS.map((group) => (
				<section key={group.title} aria-label={group.title}>
					<h3 className="font-medium text-[12px] text-ink-2">{group.title}</h3>
					{group.note ? (
						<p className="mt-0.5 text-[12px] text-ink-3">{group.note}</p>
					) : null}
					<ul className="mt-2.5 flex flex-col gap-2.5">
						{group.shortcuts.map((shortcut) => (
							<li
								key={shortcut.action}
								className="flex items-center justify-between gap-4 text-[13px]"
							>
								<span className="text-muted-foreground">{shortcut.action}</span>
								<span className="flex shrink-0 items-center gap-1">
									{shortcut.keys.map((key, index) => (
										<Fragment key={key}>
											{shortcut.chord && index > 0 ? (
												<span className="text-[11px] text-ink-3">then</span>
											) : null}
											<Kbd variant="cap">{key}</Kbd>
										</Fragment>
									))}
								</span>
							</li>
						))}
					</ul>
				</section>
			))}
			<section aria-label="Cables">
				<h3 className="font-medium text-[12px] text-ink-2">Cables</h3>
				<ul className="mt-2.5 flex flex-col gap-2.5">
					{CABLES.map((cable) => (
						<li
							key={cable.meaning}
							className="flex items-center justify-between gap-4 text-[13px]"
						>
							<span className="text-muted-foreground">{cable.meaning}</span>
							<svg
								aria-hidden
								width="40"
								height="4"
								viewBox="0 0 40 4"
								className="shrink-0 text-ink-2"
							>
								<line
									x1="0"
									y1="2"
									x2="40"
									y2="2"
									stroke="currentColor"
									strokeWidth="2"
									strokeLinecap="round"
									{...(cable.dash ? { strokeDasharray: cable.dash } : {})}
								/>
							</svg>
						</li>
					))}
				</ul>
			</section>
		</DialogBody>
	</DialogContent>
);

export const ShortcutsDialog = memo(function ShortcutsDialog({
	open,
	onOpenChange,
}: {
	open: boolean;
	onOpenChange(open: boolean): void;
}) {
	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			{CONTENT}
		</Dialog>
	);
});
