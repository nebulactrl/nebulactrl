import { createContext, useContext } from "react";

/**
 * What node and edge components read from the canvas without the canvas
 * rebuilding every node: the filter's matches (dimming) and the group
 * actions the frames' headers call.
 */
export interface CanvasUi {
	/** Ids passing the search/filter, or null when nothing is filtered. */
	matchIds: ReadonlySet<string> | null;
	/** Bumps every 30 seconds so "2m ago" labels move without a refetch. */
	tick: number;
	toggleGroup(groupId: string): void;
	renameGroup(groupId: string, name: string): void;
	ungroup(groupId: string): void;
	/** The group whose name the context menu asked to edit, until its title takes the request. */
	renamingGroupId: string | null;
	finishRenameRequest(): void;
}

const noop = () => {};

export const CanvasUiContext = createContext<CanvasUi>({
	matchIds: null,
	tick: 0,
	toggleGroup: noop,
	renameGroup: noop,
	ungroup: noop,
	renamingGroupId: null,
	finishRenameRequest: noop,
});

export function useCanvasUi(): CanvasUi {
	return useContext(CanvasUiContext);
}

/** The dimmed opacity of a card that does not match the search or filter. */
export const DIMMED_OPACITY = 0.25;
