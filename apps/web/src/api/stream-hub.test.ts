import { describe, expect, test } from "bun:test";
import {
	StreamHub,
	type StreamListener,
	type StreamRun,
	type StreamStatus,
} from "./stream-hub";

/**
 * A stream that stays open until aborted or ended by the test. After an abort
 * it settles a microtask later, like a socket closing, unless `autoSettle` is
 * off and the test calls `settleAbort()` itself.
 */
class FakeStream {
	opened = 0;
	aborted = 0;
	run: StreamRun<string> | undefined;
	#finish: (() => void) | undefined;
	readonly #autoSettle: boolean;

	constructor(autoSettle = true) {
		this.#autoSettle = autoSettle;
	}

	open = (run: StreamRun<string>): Promise<void> => {
		this.opened++;
		this.run = run;
		const { promise, resolve } = Promise.withResolvers<void>();
		this.#finish = resolve;
		run.signal.addEventListener(
			"abort",
			() => {
				this.aborted++;
				if (this.#autoSettle) queueMicrotask(resolve);
			},
			{ once: true },
		);
		return promise;
	};

	get isOpen(): boolean {
		return this.run !== undefined && !this.run.signal.aborted;
	}

	emit(data: string) {
		this.run?.onEvent(data);
	}

	settleAbort() {
		this.#finish?.();
	}

	/** The stream ends on its own, as a log stream does on `event: end`. */
	end() {
		this.run?.onStatus("ended");
		this.#finish?.();
	}
}

interface Recorder extends StreamListener<string> {
	events: string[];
	statuses: StreamStatus[];
	restarts: number;
}

function recorder(): Recorder {
	const rec: Recorder = {
		events: [],
		statuses: [],
		restarts: 0,
		onEvent: (data) => rec.events.push(data),
		onStatus: (status) => rec.statuses.push(status),
		onRestart: () => {
			rec.restarts++;
			rec.events = [];
		},
	};
	return rec;
}

/** Lets queued promise callbacks (a settling stream, the hub's rebalance) run. */
async function settle(): Promise<void> {
	for (let turn = 0; turn < 10; turn++) await Promise.resolve();
}

describe("StreamHub subscribe and refcount", () => {
	test("the first subscriber opens one stream and receives its events", () => {
		const hub = new StreamHub(2);
		const stream = new FakeStream();
		const rec = recorder();
		hub.subscribe({ key: "a", open: stream.open }, rec);
		expect(stream.opened).toBe(1);
		stream.run?.onStatus("live");
		stream.emit("one");
		expect(rec.events).toEqual(["one"]);
		expect(rec.statuses.at(-1)).toBe("live");
	});

	test("subscribers of one key share the stream, and a late joiner gets the replay", () => {
		const hub = new StreamHub(2);
		const stream = new FakeStream();
		const first = recorder();
		hub.subscribe({ key: "a", replay: 10, open: stream.open }, first);
		stream.emit("one");
		stream.emit("two");
		const late = recorder();
		hub.subscribe({ key: "a", replay: 10, open: stream.open }, late);
		expect(stream.opened).toBe(1);
		expect(late.events).toEqual(["one", "two"]);
		stream.emit("three");
		expect(first.events).toEqual(["one", "two", "three"]);
		expect(late.events).toEqual(["one", "two", "three"]);
	});

	test("the replay keeps only the newest events", () => {
		const hub = new StreamHub(2);
		const stream = new FakeStream();
		hub.subscribe({ key: "a", replay: 2, open: stream.open }, recorder());
		for (const data of ["1", "2", "3"]) stream.emit(data);
		const late = recorder();
		hub.subscribe({ key: "a", replay: 2, open: stream.open }, late);
		expect(late.events).toEqual(["2", "3"]);
	});

	test("the stream closes when the last subscriber leaves, not before", async () => {
		const hub = new StreamHub(2);
		const stream = new FakeStream();
		const offA = hub.subscribe({ key: "a", open: stream.open }, recorder());
		const offB = hub.subscribe({ key: "a", open: stream.open }, recorder());
		offA();
		await settle();
		expect(stream.aborted).toBe(0);
		expect(stream.isOpen).toBe(true);
		offB();
		expect(stream.aborted).toBe(1);
		await settle();
		expect(hub.openCount).toBe(0);
	});

	test("unsubscribing twice releases once", async () => {
		const hub = new StreamHub(2);
		const stream = new FakeStream();
		const off = hub.subscribe({ key: "a", open: stream.open }, recorder());
		const other = hub.subscribe({ key: "a", open: stream.open }, recorder());
		off();
		off();
		await settle();
		expect(stream.aborted).toBe(0);
		other();
		expect(stream.aborted).toBe(1);
	});

	test("a key subscribed again after it closed opens a fresh stream", async () => {
		const hub = new StreamHub(2);
		const stream = new FakeStream();
		hub.subscribe({ key: "a", open: stream.open }, recorder())();
		await settle();
		hub.subscribe({ key: "a", open: stream.open }, recorder());
		expect(stream.opened).toBe(2);
	});

	test("events from an aborted run are dropped", () => {
		const hub = new StreamHub(2);
		const stream = new FakeStream();
		const rec = recorder();
		const off = hub.subscribe({ key: "a", open: stream.open }, rec);
		const stale = stream.run;
		off();
		stale?.onEvent("late");
		expect(rec.events).toEqual([]);
	});
});

describe("StreamHub cap", () => {
	test("never runs more than maxActive streams, however many subscribe", async () => {
		const hub = new StreamHub(2);
		const streams = Array.from({ length: 6 }, () => new FakeStream());
		let peak = 0;
		const offs = streams.map((stream, i) => {
			const off = hub.subscribe(
				{ key: `k${i}`, open: stream.open },
				recorder(),
			);
			peak = Math.max(peak, hub.openCount);
			return off;
		});
		await settle();
		expect(streams.filter((s) => s.isOpen)).toHaveLength(2);
		// Close them one by one, newest first: waiters take over, still at most 2.
		for (const off of offs.toReversed()) {
			off();
			await settle();
			peak = Math.max(peak, hub.openCount);
		}
		expect(peak).toBeLessThanOrEqual(2);
		expect(hub.openCount).toBe(0);
	});

	test("the newest subscription wins; the displaced one waits, then restarts", async () => {
		const hub = new StreamHub(1);
		const a = new FakeStream();
		const b = new FakeStream();
		const recA = recorder();
		hub.subscribe({ key: "a", open: a.open }, recA);
		const offB = hub.subscribe({ key: "b", open: b.open }, recorder());
		expect(a.aborted).toBe(1);
		expect(recA.statuses.at(-1)).toBe("waiting");
		await settle();
		expect(b.opened).toBe(1);
		offB();
		await settle();
		expect(a.opened).toBe(2);
		expect(recA.restarts).toBe(1);
		expect(recA.statuses.at(-1)).toBe("connecting");
	});

	test("a new stream waits for an aborted one to settle, so the cap holds at every instant", async () => {
		const hub = new StreamHub(1);
		const a = new FakeStream(false);
		const b = new FakeStream();
		hub.subscribe({ key: "a", open: a.open }, recorder());
		hub.subscribe({ key: "b", open: b.open }, recorder());
		// `a` is aborted but its socket has not closed: `b` must not have started.
		await settle();
		expect(a.aborted).toBe(1);
		expect(b.opened).toBe(0);
		expect(hub.openCount).toBe(1);
		a.settleAbort();
		await settle();
		expect(b.opened).toBe(1);
		expect(hub.openCount).toBe(1);
	});

	test("a pinned stream is never displaced", async () => {
		const hub = new StreamHub(2);
		const feed = new FakeStream();
		hub.subscribe({ key: "feed", pinned: true, open: feed.open }, recorder());
		const logs = Array.from({ length: 3 }, () => new FakeStream());
		for (const [i, stream] of logs.entries()) {
			hub.subscribe({ key: `log${i}`, open: stream.open }, recorder());
			await settle();
		}
		expect(feed.isOpen).toBe(true);
		expect(feed.aborted).toBe(0);
		expect(logs.filter((s) => s.isOpen)).toHaveLength(1);
		expect(logs[2]?.isOpen).toBe(true);
		expect(hub.openCount).toBe(2);
	});

	test("a stream that ends on its own frees its slot for a waiting one", async () => {
		const hub = new StreamHub(1);
		const a = new FakeStream();
		const b = new FakeStream();
		hub.subscribe({ key: "a", open: a.open }, recorder());
		hub.subscribe({ key: "b", open: b.open }, recorder());
		await settle();
		expect(b.isOpen).toBe(true);
		b.end();
		await settle();
		expect(a.opened).toBe(2);
		expect(hub.openCount).toBe(1);
	});

	test("an ended stream is not restarted while its subscribers stay", async () => {
		const hub = new StreamHub(2);
		const a = new FakeStream();
		const rec = recorder();
		hub.subscribe({ key: "a", open: a.open }, rec);
		a.end();
		await settle();
		expect(rec.statuses.at(-1)).toBe("ended");
		expect(a.opened).toBe(1);
		expect(hub.openCount).toBe(0);
	});
});

describe("StreamHub hidden tabs", () => {
	test("a hidden tab holds no connection and resumes when shown", async () => {
		const hub = new StreamHub(2);
		const feed = new FakeStream();
		const log = new FakeStream();
		const recLog = recorder();
		hub.subscribe({ key: "feed", pinned: true, open: feed.open }, recorder());
		hub.subscribe({ key: "log", open: log.open }, recLog);
		hub.setHidden(true);
		await settle();
		expect(hub.openCount).toBe(0);
		expect(recLog.statuses.at(-1)).toBe("waiting");
		hub.setHidden(false);
		await settle();
		expect(feed.opened).toBe(2);
		expect(log.opened).toBe(2);
		expect(hub.openCount).toBe(2);
		expect(recLog.restarts).toBe(1);
	});

	test("subscribing while hidden opens nothing", async () => {
		const hub = new StreamHub(2);
		hub.setHidden(true);
		const stream = new FakeStream();
		hub.subscribe({ key: "a", open: stream.open }, recorder());
		await settle();
		expect(stream.opened).toBe(0);
		hub.setHidden(false);
		expect(stream.opened).toBe(1);
	});
});

describe("StreamHub background streams", () => {
	test("a background stream keeps running in a hidden tab; the rest are released", async () => {
		const hub = new StreamHub(2);
		const feed = new FakeStream();
		const log = new FakeStream();
		hub.subscribe(
			{ key: "feed", pinned: true, background: true, open: feed.open },
			recorder(),
		);
		hub.subscribe({ key: "log", open: log.open }, recorder());
		hub.setHidden(true);
		await settle();
		expect(feed.isOpen).toBe(true);
		expect(feed.aborted).toBe(0);
		expect(log.isOpen).toBe(false);
		expect(hub.openCount).toBe(1);
	});
});
