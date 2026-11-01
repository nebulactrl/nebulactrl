import type { InspectorTab } from "../tab-ids";

/** The id of the Connection variables section at the top of a database's Variables tab. */
const CONNECTION_ANCHOR = "connection";

const WAIT_MS = 1_000;

/**
 * Opens Variables and brings its Connection variables section to the top
 * (`?tab=variables#connection`). The tab mounts after the route changes, so
 * the section is looked for on the next frames, for up to a second.
 */
export function openConnectionVariables(
	openTab: (tab: InspectorTab) => void,
): void {
	openTab("variables");
	const started = performance.now();
	const look = () => {
		const target = document.getElementById(CONNECTION_ANCHOR);
		if (target) {
			target.scrollIntoView({ block: "start" });
			const { pathname, search } = window.location;
			window.history.replaceState(
				window.history.state,
				"",
				`${pathname}${search}#${CONNECTION_ANCHOR}`,
			);
			return;
		}
		if (performance.now() - started < WAIT_MS) requestAnimationFrame(look);
	};
	requestAnimationFrame(look);
}
