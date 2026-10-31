import { useEffect } from "react";

const APP_NAME = "NebulaCtrl";

/**
 * Sets the browser tab title for the mounted page, so tabs, history and screen
 * reader page announcements tell screens apart: most specific part first, the
 * app name last. Falsy parts (data still loading) are skipped. Unmounting
 * restores the title it replaced, so a page that sets none never inherits the
 * last page's.
 */
export function useDocumentTitle(parts: ReadonlyArray<string | undefined>) {
	const title = [...parts.filter(Boolean), APP_NAME].join(" · ");
	useEffect(() => {
		const previous = document.title;
		document.title = title;
		return () => {
			document.title = previous;
		};
	}, [title]);
}
