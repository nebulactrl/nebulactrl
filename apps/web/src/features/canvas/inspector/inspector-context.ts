import { createContext, useContext } from "react";
import type { InspectorTab } from "./tab-ids";

/** What a tab body may ask of the inspector that hosts it. */
export interface InspectorActions {
	/** Close the inspector (e.g. after the inspected service was deleted). */
	close(): void;
	/** Switch the inspector to another tab. */
	openTab(tab: InspectorTab): void;
}

export const InspectorContext = createContext<InspectorActions>({
	close: () => {},
	openTab: () => {},
});

export function useInspectorActions(): InspectorActions {
	return useContext(InspectorContext);
}
