import {
	createContext,
	type ReactNode,
	useContext,
	useMemo,
	useState,
} from "react";
import { NewProjectDialog } from "#/components/project/new-project-dialog";
import { CommandPalette } from "./command-palette";

export interface ShellActions {
	paletteOpen: boolean;
	setPaletteOpen(open: boolean): void;
	openPalette(): void;
	openNewProject(): void;
}

const ShellActionsContext = createContext<ShellActions | null>(null);

/**
 * The shell's overlays — the ⌘K palette and the new-project dialog — and the
 * actions that open them from anywhere under the shell (the top bar's
 * search button, the projects page, the palette's own entries).
 */
export function ShellActionsProvider({ children }: { children: ReactNode }) {
	const [paletteOpen, setPaletteOpen] = useState(false);
	const [newProjectOpen, setNewProjectOpen] = useState(false);

	const actions = useMemo<ShellActions>(
		() => ({
			paletteOpen,
			setPaletteOpen,
			openPalette: () => setPaletteOpen(true),
			openNewProject: () => setNewProjectOpen(true),
		}),
		[paletteOpen],
	);

	return (
		<ShellActionsContext.Provider value={actions}>
			{children}
			<CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} />
			<NewProjectDialog
				open={newProjectOpen}
				onOpenChange={setNewProjectOpen}
			/>
		</ShellActionsContext.Provider>
	);
}

export function useShellActions(): ShellActions {
	const actions = useContext(ShellActionsContext);
	if (!actions) {
		throw new Error("useShellActions must be used inside ShellActionsProvider");
	}
	return actions;
}
