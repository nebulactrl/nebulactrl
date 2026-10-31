import { createContext, type ReactNode, useContext } from "react";
import type { ServerView } from "./model";

interface ServerPage {
	server: ServerView;
	/** Opens the drain sheet, which the page's frame owns. */
	openDrain: () => void;
}

const ServerContext = createContext<ServerPage | null>(null);

/** Hands the loaded server to the tab under the server page's frame. */
export function ServerProvider({
	value,
	children,
}: {
	value: ServerPage;
	children: ReactNode;
}) {
	return <ServerContext value={value}>{children}</ServerContext>;
}

function usePage(): ServerPage {
	const page = useContext(ServerContext);
	if (!page) throw new Error("This needs the server page's frame");
	return page;
}

/** The server the page is about. Only valid under the server page's frame, which renders its tabs once the server loaded. */
export function useServer(): ServerView {
	return usePage().server;
}

/** Opens the drain sheet of the server the page is about. */
export function useOpenDrain(): () => void {
	return usePage().openDrain;
}
