import { createFileRoute, notFound } from "@tanstack/react-router";
import { lazy } from "react";

/**
 * The dither kit's catalogue: every component with its knobs. Dev only: the
 * route is a 404 in a production build, and the gallery chunk is not even
 * emitted (the lazy import sits behind a build-time constant).
 */
const DitherGalleryPage = import.meta.env.DEV
	? lazy(() =>
			import("#/features/dev/dither-gallery").then((m) => ({
				default: m.DitherGalleryPage,
			})),
		)
	: null;

export const Route = createFileRoute("/dev/dither")({
	beforeLoad: () => {
		if (!import.meta.env.DEV) throw notFound();
	},
	component: () => (DitherGalleryPage ? <DitherGalleryPage /> : null),
});
