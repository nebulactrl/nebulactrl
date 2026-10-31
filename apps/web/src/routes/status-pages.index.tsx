import { createFileRoute } from "@tanstack/react-router";
import { statusPagesQuery } from "#/features/status-pages/queries";
import { StatusPagesPage } from "#/features/status-pages/status-pages-page";

export const Route = createFileRoute("/status-pages/")({
	// Starts the list request and returns at once; the intent preload of the
	// sidebar link then has the cards in the cache when the click renders.
	loader: ({ context }) => {
		void context.queryClient.prefetchQuery(statusPagesQuery());
	},
	component: StatusPagesPage,
});
