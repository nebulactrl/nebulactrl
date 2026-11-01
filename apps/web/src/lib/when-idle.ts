/**
 * Runs `task` once the browser has nothing better to do (or after `timeout`
 * ms at the latest), and returns what cancels it. Safari has no
 * `requestIdleCallback`, so it falls back to a timer of the same length.
 */
export function whenIdle(task: () => void, timeout = 1500): () => void {
	if (typeof window.requestIdleCallback === "function") {
		const handle = window.requestIdleCallback(task, { timeout });
		return () => window.cancelIdleCallback(handle);
	}
	const handle = window.setTimeout(task, Math.min(timeout, 200));
	return () => window.clearTimeout(handle);
}
