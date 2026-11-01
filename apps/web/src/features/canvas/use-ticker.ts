import { useEffect, useState } from "react";

/**
 * `Date.now()`, refreshed every `intervalMs` while `active`. Idle, it is
 * just the time at render, so a quiet card costs no timer.
 */
export function useTicker(active: boolean, intervalMs = 1000): number {
	const [now, setNow] = useState(() => Date.now());
	useEffect(() => {
		if (!active) return;
		setNow(Date.now());
		const timer = window.setInterval(() => setNow(Date.now()), intervalMs);
		return () => window.clearInterval(timer);
	}, [active, intervalMs]);
	return active ? now : Date.now();
}
