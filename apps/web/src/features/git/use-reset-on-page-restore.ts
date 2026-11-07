import { useEffect, useRef } from "react";

/**
 * Runs `reset` when the browser restores this page from its back/forward cache
 * (`pageshow` with `persisted`). A mutation that navigated away on success is
 * still "success" in the restored page, so its button stays disabled until it
 * is reset.
 */
export function useResetOnPageRestore(...resets: Array<() => void>): void {
	const latest = useRef(resets);
	latest.current = resets;
	useEffect(() => {
		const onPageShow = (event: PageTransitionEvent) => {
			if (!event.persisted) return;
			for (const reset of latest.current) reset();
		};
		window.addEventListener("pageshow", onPageShow);
		return () => window.removeEventListener("pageshow", onPageShow);
	}, []);
}
