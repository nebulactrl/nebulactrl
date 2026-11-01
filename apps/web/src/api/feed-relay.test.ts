import { describe, expect, test } from "bun:test";
import {
	type FeedChannel,
	type FeedLocks,
	type FeedMessage,
	startFeedRelay,
} from "./feed-relay";
import type { StreamStatus } from "./stream-hub";

/** Web Locks with one exclusive holder and a FIFO queue, like the browser's. */
class FakeLocks implements FeedLocks {
	#held = false;
	readonly #queue: Array<() => void> = [];

	request(
		_name: string,
		options: { signal: AbortSignal },
		callback: () => Promise<void>,
	): Promise<unknown> {
		const { promise, resolve, reject } = Promise.withResolvers<unknown>();
		const grant = () => {
			this.#held = true;
			void callback().finally(() => {
				this.#held = false;
				this.#queue.shift()?.();
			});
			resolve(undefined);
		};
		options.signal.addEventListener(
			"abort",
			() => {
				const at = this.#queue.indexOf(grant);
				if (at >= 0) {
					this.#queue.splice(at, 1);
					reject(new DOMException("aborted", "AbortError"));
				}
			},
			{ once: true },
		);
		if (this.#held) this.#queue.push(grant);
		else grant();
		return promise;
	}
}

/** A BroadcastChannel bus: a message reaches every other channel, never the sender. */
class FakeBus {
	readonly #channels = new Set<{
		handler: ((message: FeedMessage<string>) => void) | undefined;
	}>();

	open(): FeedChannel<string> {
		const self: {
			handler: ((message: FeedMessage<string>) => void) | undefined;
		} = { handler: undefined };
		this.#channels.add(self);
		return {
			post: (message) => {
				for (const other of this.#channels) {
					if (other !== self) other.handler?.(message);
				}
			},
			listen: (handler) => {
				self.handler = handler;
			},
			close: () => {
				this.#channels.delete(self);
			},
		};
	}
}

/** One tab: its feed callbacks recorded, and how often it opened the real stream. */
function tab(locks: FakeLocks | undefined, bus: FakeBus | undefined) {
	const seen = {
		events: [] as string[],
		statuses: [] as StreamStatus[],
		resyncs: 0,
		connects: 0,
		shared: [] as boolean[],
		disconnects: 0,
		emit: (_event: string) => {},
		report: (_status: StreamStatus) => {},
	};
	const stop = startFeedRelay<string>({
		locks,
		channel: bus?.open(),
		connect: ({ onEvent, onStatus }, shared) => {
			seen.connects++;
			seen.shared.push(shared);
			seen.emit = onEvent;
			seen.report = onStatus;
			return () => {
				seen.disconnects++;
			};
		},
		onEvent: (event) => seen.events.push(event),
		onStatus: (status) => seen.statuses.push(status),
		onResync: () => {
			seen.resyncs++;
		},
	});
	return { seen, stop };
}

async function settle(): Promise<void> {
	for (let turn = 0; turn < 10; turn++) await Promise.resolve();
}

describe("startFeedRelay", () => {
	test("only the first tab opens the stream; the others hear its events and status", async () => {
		const locks = new FakeLocks();
		const bus = new FakeBus();
		const a = tab(locks, bus);
		const b = tab(locks, bus);
		const c = tab(locks, bus);
		await settle();
		expect([a.seen.connects, b.seen.connects, c.seen.connects]).toEqual([
			1, 0, 0,
		]);
		a.seen.report("live");
		a.seen.emit("deploy.finished");
		expect(a.seen.events).toEqual(["deploy.finished"]);
		expect(b.seen.events).toEqual(["deploy.finished"]);
		expect(c.seen.events).toEqual(["deploy.finished"]);
		expect(b.seen.statuses.at(-1)).toBe("live");
	});

	test("a tab that starts later learns the current status from the leader", async () => {
		const locks = new FakeLocks();
		const bus = new FakeBus();
		const a = tab(locks, bus);
		await settle();
		a.seen.report("paused");
		const late = tab(locks, bus);
		await settle();
		expect(late.seen.connects).toBe(0);
		expect(late.seen.statuses.at(-1)).toBe("paused");
	});

	test("when the leader stops the next tab takes over and the rest resync", async () => {
		const locks = new FakeLocks();
		const bus = new FakeBus();
		const a = tab(locks, bus);
		const b = tab(locks, bus);
		const c = tab(locks, bus);
		await settle();
		a.stop();
		await settle();
		expect(a.seen.disconnects).toBe(1);
		expect(b.seen.connects).toBe(1);
		expect(c.seen.connects).toBe(0);
		// The new leader's own data is fresh; the follower refetches what it missed.
		expect(b.seen.resyncs).toBe(0);
		expect(c.seen.resyncs).toBe(1);
		b.seen.emit("service.updated");
		expect(c.seen.events).toEqual(["service.updated"]);
	});

	test("a follower that stops never opens the stream and leaves the leader alone", async () => {
		const locks = new FakeLocks();
		const bus = new FakeBus();
		const a = tab(locks, bus);
		const b = tab(locks, bus);
		await settle();
		b.stop();
		await settle();
		expect(a.seen.disconnects).toBe(0);
		expect(b.seen.connects).toBe(0);
		a.stop();
		await settle();
		expect(a.seen.disconnects).toBe(1);
		expect(b.seen.connects).toBe(0);
	});

	test("without Web Locks (plain HTTP) every tab opens its own stream", () => {
		const bus = new FakeBus();
		const a = tab(undefined, bus);
		const b = tab(undefined, bus);
		expect([a.seen.connects, b.seen.connects]).toEqual([1, 1]);
		expect([a.seen.shared, b.seen.shared]).toEqual([[false], [false]]);
		a.stop();
		expect(a.seen.disconnects).toBe(1);
	});

	test("without a BroadcastChannel the tab opens its own stream", () => {
		const a = tab(new FakeLocks(), undefined);
		expect(a.seen.connects).toBe(1);
		expect(a.seen.shared).toEqual([false]);
	});

	test("stopping before the lock is granted opens nothing", async () => {
		const locks = new FakeLocks();
		const bus = new FakeBus();
		const first = tab(locks, bus);
		const queued = tab(locks, bus);
		queued.stop();
		first.stop();
		await settle();
		expect(queued.seen.connects).toBe(0);
		expect(first.seen.disconnects).toBe(1);
	});

	test("the elected leader connects as shared, so it can outlive its own visibility", async () => {
		const locks = new FakeLocks();
		const bus = new FakeBus();
		const a = tab(locks, bus);
		await settle();
		expect(a.seen.shared).toEqual([true]);
	});
});
