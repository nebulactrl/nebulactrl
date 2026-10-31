import { createContext, useContext } from "react";

/** The docs layout's scrolling element: pages need it for deep links and the TOC. */
export const DocsScrollContext = createContext<HTMLElement | null>(null);

export function useDocsScroll(): HTMLElement | null {
	return useContext(DocsScrollContext);
}
