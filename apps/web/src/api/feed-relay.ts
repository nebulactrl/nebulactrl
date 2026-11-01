// One invalidation feed for every tab of the app, not one per tab.
//
// The six-connections-per-host limit is per browser, not per tab: five tabs
// each holding their own `/events` stream leave one connection for everything
// else, and the sixth tab never finishes loading. So the tabs elect a leader
// with a Web Lock; only the leader opens the stream and broadcasts what it
// hears, and the others listen on a BroadcastChannel. When the leader's tab
// closes the browser releases its lock, the next waiter takes over, and the
// tabs refetch once because events were missed in between.
//
// Web Locks need a secure context (https or localhost). On plain HTTP they are
// absent, and each tab then opens its own stream: still bounded by the stream
// hub (one feed per tab, none while hidden).

import type { StreamStatus } from "./stream-hub";

const FEED_LOCK = "nebulactrl-events-feed";

export type FeedMessage<T> =
	| { t: "event"; event: T }
	| { t: "status"; status: StreamStatus }
	/** A tab that just started asks the leader for the current status. */
	| { t: "hello" }
	/** A new leader took over: events may have been missed. */
	| { t: "resync" };

export interface FeedLocks {
	request(
		name: string,
		options: { signal: AbortSignal },
		callback: () => Promise<void>,
	): Promise<unknown>;
}

export interface FeedChannel<T> {
	post(message: FeedMessage<T>): void;
	listen(handler: (message: FeedMessage<T>) => void): void;
	close(): void;
}

interface FeedHandlers<T> {
	onEvent(event: T): void;
	onStatus(status: StreamStatus): void;
}

export interface FeedRelayOptions<T> extends FeedHandlers<T> {
	locks: FeedLocks | undefined;
	channel: FeedChannel<T> | undefined;
	/**
	 * Opens the real stream and returns its unsubscribe. `shared` is true when
	 * this tab is the elected leader (one stream for every tab, so it may keep
	 * running while hidden) and false when it is alone with its own.
	 */
	connect(handlers: FeedHandlers<T>, shared: boolean): () => void;
	/** Another tab took over the stream: refetch what was missed. */
	onResync(): void;
}

/** Starts this tab's share of the feed. Returns the stop function. */
export function startFeedRelay<T>(options: FeedRelayOptions<T>): () => void {
	const { locks, channel } = options;
	if (!locks || !channel) {
		return options.connect(options, false);
	}

	const abort = new AbortController();
	let leading = false;
	let status: StreamStatus = "connecting";
	let disconnect: (() => void) | undefined;
	let release: (() => void) | undefined;

	channel.listen((message) => {
		if (leading) {
			if (message.t === "hello") channel.post({ t: "status", status });
			return;
		}
		if (message.t === "event") options.onEvent(message.event);
		else if (message.t === "status") options.onStatus(message.status);
		else if (message.t === "resync") options.onResync();
	});
	channel.post({ t: "hello" });

	locks
		.request(FEED_LOCK, { signal: abort.signal }, () => {
			// Granted after stop(): nothing to hold.
			if (abort.signal.aborted) return Promise.resolve();
			const held = Promise.withResolvers<void>();
			leading = true;
			release = held.resolve;
			channel.post({ t: "resync" });
			disconnect = options.connect(
				{
					onEvent: (event) => {
						options.onEvent(event);
						channel.post({ t: "event", event });
					},
					onStatus: (next) => {
						status = next;
						options.onStatus(next);
						channel.post({ t: "status", status: next });
					},
				},
				true,
			);
			return held.promise;
		})
		// A request aborted while queued rejects with AbortError: that is stop().
		.catch(() => {});

	return () => {
		abort.abort();
		disconnect?.();
		release?.();
		channel.close();
	};
}
