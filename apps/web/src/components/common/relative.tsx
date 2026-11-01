import { useSyncExternalStore } from "react";
import { absoluteTime, relativeTime } from "#/lib/format";

// A page can render dozens of these at once (a table of environments, a
// list of approvals). One shared interval drives every instance's
// re-render instead of each `<Relative>` mounting its own `setInterval` —
// fifty timers doing the same 30s tick would be fifty times the work for
// no extra freshness. Subscribers just react to a shared tick counter.
const TICK_MS = 30_000;
let tick = 0;
// `window.setInterval`/`window.clearInterval` (not the bare globals): the
// bundled `bun` types otherwise resolve `setInterval`'s return type to
// Bun's `Timer` instead of the DOM's `number`, matching the pattern
// `Copyable` already uses for `window.setTimeout`.
let intervalId: number | undefined;
const listeners = new Set<() => void>();

function subscribe(listener: () => void): () => void {
	listeners.add(listener);
	if (intervalId === undefined) {
		intervalId = window.setInterval(() => {
			tick += 1;
			for (const l of listeners) l();
		}, TICK_MS);
	}
	return () => {
		listeners.delete(listener);
		if (listeners.size === 0 && intervalId !== undefined) {
			window.clearInterval(intervalId);
			intervalId = undefined;
		}
	};
}

function getSnapshot(): number {
	return tick;
}

export interface RelativeProps {
	iso: string | null | undefined;
}

/**
 * A `<time>` reading "4m ago" with the absolute UTC instant as its
 * `title`, refreshing every 30s so a page left open doesn't drift into a
 * stale caption. Missing data (never happened) reads an em dash rather
 * than a fabricated timestamp.
 */
export function Relative({ iso }: RelativeProps) {
	// Subscribing (not reading) the shared tick is what forces this
	// component to re-render on the 30s boundary — the tick value itself is
	// never displayed.
	useSyncExternalStore(subscribe, getSnapshot, getSnapshot);

	if (!iso) {
		// `role="img"` (matching `StateDot`'s own decorative-glyph pattern):
		// a bare `<span>` has no role that accepts `aria-label`, but this
		// dash is standing in for a fact ("never happened"), not raw text.
		return (
			<span role="img" aria-label="never">
				—
			</span>
		);
	}

	return (
		<time className="tabular-nums" dateTime={iso} title={absoluteTime(iso)}>
			{relativeTime(iso)}
		</time>
	);
}
