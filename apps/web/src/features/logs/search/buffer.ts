// The searchable window over a log stream. The stream hands over its whole line
// array on every flush; this keeps what it learned about the lines already seen
// and only does work for lines that are new (parse, count, match) or gone
// (uncount). A new query re-matches the buffer once.

import { FacetStats } from "./facets";
import { isBlankLine, type LogInput, type ParsedLine, parsedFor } from "./line";
import { type Matcher, matchAll } from "./match";

export const BUFFER_CAP = 5000;

export interface BufferLine extends LogInput {
	/** Ascending within a stream. */
	id: number;
}

interface Entry<L> {
	line: L;
	parsed: ParsedLine;
	hit: boolean;
}

export class LogBuffer<L extends BufferLine> {
	readonly stats = new FacetStats();
	/** The lines matching the current query, in order. A new array whenever it changes. */
	visible: L[] = [];
	private entries: Entry<L>[] = [];
	private matcher: Matcher = matchAll;
	private lastId = 0;
	/** The first line of the previous sync, to notice lines added in front of it. */
	private firstId: number | undefined;

	constructor(
		private readonly cap = BUFFER_CAP,
		/** Leave out lines with nothing visible in them. */
		private readonly skipBlank = false,
	) {}

	/** Lines in the buffer, matching or not. */
	get total(): number {
		return this.entries.length;
	}

	/** Switches the query; every buffered line is matched again. */
	setMatcher(matcher: Matcher) {
		if (matcher === this.matcher) return;
		this.matcher = matcher;
		const visible: L[] = [];
		for (const entry of this.entries) {
			entry.hit = matcher(entry.parsed);
			if (entry.hit) visible.push(entry.line);
		}
		this.visible = visible;
	}

	/** Forgets every line; the next sync takes the stream's lines afresh. */
	reset() {
		this.entries = [];
		this.visible = [];
		this.stats.reset();
		this.lastId = 0;
		this.firstId = undefined;
	}

	/** Takes the stream's current lines. Returns whether anything changed. */
	sync(lines: readonly L[]): boolean {
		const last = lines[lines.length - 1];
		if (!last) {
			if (this.entries.length === 0 && this.lastId === 0) return false;
			this.reset();
			return true;
		}
		const first = lines[0] as L;
		// A stream that restarted numbers its lines from 1 again. A history that
		// loaded an older page starts earlier than before: the order is new.
		if (
			last.id < this.lastId ||
			(this.firstId !== undefined && first.id < this.firstId)
		) {
			this.reset();
		}
		this.firstId = first.id;

		let from = lines.length;
		while (from > 0 && (lines[from - 1] as L).id > this.lastId) from -= 1;

		let dropped = 0;
		let droppedHits = 0;
		const drop = (entry: Entry<L>) => {
			this.stats.remove(entry.parsed);
			dropped += 1;
			if (entry.hit) droppedHits += 1;
		};
		let head = 0;
		while (
			head < this.entries.length &&
			(this.entries[head] as Entry<L>).line.id < first.id
		) {
			drop(this.entries[head] as Entry<L>);
			head += 1;
		}

		const added: Entry<L>[] = [];
		const addedVisible: L[] = [];
		for (let i = from; i < lines.length; i++) {
			const line = lines[i] as L;
			if (this.skipBlank && isBlankLine(line.line)) continue;
			const parsed = parsedFor(line);
			const hit = this.matcher(parsed);
			this.stats.add(parsed);
			added.push({ line, parsed, hit });
			if (hit) addedVisible.push(line);
		}
		this.lastId = last.id;
		if (
			dropped === 0 &&
			added.length === 0 &&
			this.entries.length <= this.cap
		) {
			return false;
		}

		const entries =
			head === 0
				? this.entries.concat(added)
				: this.entries.slice(head).concat(added);
		const over = entries.length - this.cap;
		if (over > 0) {
			for (const entry of entries.slice(0, over)) drop(entry);
			entries.splice(0, over);
		}
		this.entries = entries;
		const visible = this.visible;
		if (droppedHits === 0 && addedVisible.length === 0)
			return dropped > 0 || added.length > 0;
		// The dropped lines are the oldest, so they come off the front of old + new.
		const combined = visible.concat(addedVisible);
		this.visible = droppedHits === 0 ? combined : combined.slice(droppedHits);
		return true;
	}
}
