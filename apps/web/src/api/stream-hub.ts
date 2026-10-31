// One tab's live connections, owned in one place.
//
// Browsers allow six HTTP/1.1 connections per host, and a server-sent stream
// holds one for as long as it is open. A page that opens a stream per
// component (the feed, a log tail, a deployment log, a run output…) starves
// every ordinary request behind them: the page sits on its skeletons while
// `curl` answers in milliseconds. The installer's IP mode serves plain HTTP,
// so this is also the production transport, not only a dev-proxy artifact.
//
// So components never open streams. They subscribe here, and the hub:
//
// - shares: subscribers of the same `key` ride one stream (refcounted; it
//   closes when the last one leaves, and a late joiner gets the buffered
//   events first);
// - bounds: at most `maxActive` streams run at once, counting a stream that
//   was aborted but has not yet settled, so the cap holds at every instant.
//   The pinned stream (the invalidation feed) always runs; of the rest the
//   most recently subscribed wins, and the others report `waiting` until a
//   slot frees up, then restart (`onRestart`: the stream replays its tail);
// - releases: a hidden tab holds nothing, except a stream marked `background`
//   (the feed, when another mechanism already bounds it browser-wide). It
//   resumes on return.
//
// The hub knows nothing about HTTP or React: `open` is whatever runs a stream.

export type StreamStatus =
	| "connecting"
	| "live"
	| "paused"
	| "waiting"
	| "ended";

/** What `open` is given to run one stream until `signal` aborts or it ends. */
export interface StreamRun<T> {
	signal: AbortSignal;
	onEvent(data: T): void;
	onStatus(status: Exclude<StreamStatus, "waiting">): void;
}

export interface StreamSpec<T> {
	/** Subscribers with the same key share one stream. */
	key: string;
	/** Runs the stream; resolves when it ends or `signal` aborts. Never rejects. */
	open(run: StreamRun<T>): Promise<void>;
	/** Ranks above every unpinned stream when slots are scarce. */
	pinned?: boolean;
	/** Keeps running while the tab is hidden. Default false. */
	background?: boolean;
	/** Events kept for late joiners, oldest dropped first. Default 0. */
	replay?: number;
}

export interface StreamListener<T> {
	onEvent(data: T): void;
	onStatus(status: StreamStatus): void;
	/** The stream restarts from its beginning: drop what you accumulated. */
	onRestart?(): void;
}

interface Entry {
	key: string;
	open(run: StreamRun<unknown>): Promise<void>;
	pinned: boolean;
	background: boolean;
	replayLimit: number;
	listeners: Set<StreamListener<unknown>>;
	/** Recency of the latest subscribe; higher is newer. */
	stamp: number;
	status: StreamStatus;
	replay: unknown[];
	controller: AbortController | undefined;
	/** Set from `open` until its promise settles, aborted or not. */
	running: Promise<void> | undefined;
	finished: boolean;
	starts: number;
}

export class StreamHub {
	readonly #entries = new Map<string, Entry>();
	/** Every run that has not settled yet, including entries since removed. */
	readonly #running = new Set<Promise<void>>();
	readonly #maxActive: number;
	#stamp = 0;
	#hidden = false;

	constructor(maxActive: number) {
		this.#maxActive = maxActive;
	}

	/** Streams currently holding a connection (settling ones included). */
	get openCount(): number {
		return this.#running.size;
	}

	subscribe<T>(spec: StreamSpec<T>, listener: StreamListener<T>): () => void {
		let entry = this.#entries.get(spec.key);
		if (!entry) {
			entry = {
				key: spec.key,
				open: spec.open as Entry["open"],
				pinned: spec.pinned ?? false,
				background: spec.background ?? false,
				replayLimit: spec.replay ?? 0,
				listeners: new Set(),
				stamp: 0,
				status: "waiting",
				replay: [],
				controller: undefined,
				running: undefined,
				finished: false,
				starts: 0,
			};
			this.#entries.set(spec.key, entry);
		}
		const joined = entry;
		const shared = listener as StreamListener<unknown>;
		joined.stamp = ++this.#stamp;
		joined.listeners.add(shared);
		for (const data of joined.replay) shared.onEvent(data);
		shared.onStatus(joined.status);
		this.#rebalance();

		let subscribed = true;
		return () => {
			if (!subscribed) return;
			subscribed = false;
			joined.listeners.delete(shared);
			if (joined.listeners.size === 0) {
				this.#stop(joined);
				// A newer subscribe may have recreated the key; only drop our own.
				if (this.#entries.get(joined.key) === joined) {
					this.#entries.delete(joined.key);
				}
			}
			this.#rebalance();
		};
	}

	/** A hidden tab releases every connection; a visible one takes them back. */
	setHidden(hidden: boolean): void {
		if (this.#hidden === hidden) return;
		this.#hidden = hidden;
		this.#rebalance();
	}

	#setStatus(entry: Entry, status: StreamStatus): void {
		if (entry.status === status) return;
		entry.status = status;
		for (const listener of [...entry.listeners]) listener.onStatus(status);
	}

	/** Aborts the entry's run. The connection is still counted until it settles. */
	#stop(entry: Entry): void {
		entry.controller?.abort();
		entry.controller = undefined;
	}

	#start(entry: Entry): void {
		const controller = new AbortController();
		entry.controller = controller;
		entry.replay = [];
		if (entry.starts++ > 0) {
			for (const listener of [...entry.listeners]) listener.onRestart?.();
		}
		this.#setStatus(entry, "connecting");
		const live = () => entry.controller === controller;
		const running: Promise<void> = entry
			.open({
				signal: controller.signal,
				onEvent: (data) => {
					if (!live()) return;
					if (entry.replayLimit > 0) {
						entry.replay.push(data);
						if (entry.replay.length > entry.replayLimit) entry.replay.shift();
					}
					for (const listener of [...entry.listeners]) listener.onEvent(data);
				},
				onStatus: (status) => {
					if (live()) this.#setStatus(entry, status);
				},
			})
			.catch(() => {})
			.finally(() => {
				this.#running.delete(running);
				if (entry.running === running) entry.running = undefined;
				if (live()) {
					// It ended on its own: the slot is free and it will not restart.
					entry.controller = undefined;
					entry.finished = true;
					this.#setStatus(entry, "ended");
				}
				this.#rebalance();
			});
		entry.running = running;
		this.#running.add(running);
	}

	#rebalance(): void {
		const wanted = new Set(
			[...this.#entries.values()]
				.filter(
					(entry) => !entry.finished && (!this.#hidden || entry.background),
				)
				.sort(
					(a, b) => Number(b.pinned) - Number(a.pinned) || b.stamp - a.stamp,
				)
				.slice(0, this.#maxActive),
		);
		for (const entry of this.#entries.values()) {
			if (entry.finished || wanted.has(entry)) continue;
			this.#stop(entry);
			this.#setStatus(entry, "waiting");
		}
		for (const entry of wanted) {
			if (entry.controller || entry.running) continue;
			// A run that was just aborted still holds its socket: wait for it to
			// settle (its `finally` rebalances) rather than exceed the cap.
			if (this.#running.size >= this.#maxActive) break;
			this.#start(entry);
		}
	}
}
