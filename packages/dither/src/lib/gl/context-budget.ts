/**
 * The WebGL context budget shared by every DitherShader on the page.
 *
 * Browsers cap live WebGL contexts (Chrome drops the oldest past 16, phones
 * run out of GPU memory sooner). The budget keeps at most `MAX_LIVE_CONTEXTS`
 * of them:
 *
 * - A shader holds a context only while within `BUDGET_MARGIN` of the
 *   viewport (`near`) or on screen (`visible`). Further away it is `far`
 *   and hands its context back; it shows its fallback until it returns.
 * - When every slot is taken, a shader that comes on screen takes the slot
 *   of one that is only `near`. If every holder is on screen as well, it
 *   takes the slot of the one that came on screen longest ago: a sheet or
 *   dialog opened over a page full of shaders must draw, and the page's
 *   shaders behind it still report themselves visible (an intersection
 *   observer cannot see that they are covered). The order is total, so the
 *   shader that lost its slot never takes it back from a newer one; it gets
 *   the next free slot (the sheet closing).
 *
 * Pure bookkeeping: members bring `grant` / `revoke` callbacks, so the rules
 * are unit tested without WebGL.
 */

/** Most contexts alive at once. */
export const MAX_LIVE_CONTEXTS = 8;
/** How far above and below the viewport a shader is still `near`, px. */
export const BUDGET_MARGIN_PX = 200;
/** `rootMargin` inside which a shader is `near` and may hold a context. */
export const BUDGET_MARGIN = `${BUDGET_MARGIN_PX}px 0px`;

export type Proximity = "far" | "near" | "visible";

export interface BudgetMember {
	/** Take a context (it was free, or one was evicted for you). */
	grant(): void;
	/** Give your context back. */
	revoke(): void;
}

interface Entry {
	member: BudgetMember;
	proximity: Proximity;
	live: boolean;
	seq: number;
	/** When the member last came on screen; larger is more recent. */
	shown: number;
}

export class ContextBudget {
	private readonly entries = new Map<BudgetMember, Entry>();
	private seq = 0;

	readonly max: number;

	constructor(max = MAX_LIVE_CONTEXTS) {
		this.max = max;
	}

	/**
	 * Join the budget. `live` says the member already holds a context. A
	 * renderer holds none until it is granted one, so it joins not live and
	 * `far`, and the first proximity reports decide whether it gets a slot.
	 * Members that do join live and on screen are never evicted by a report
	 * alone, only by the cap.
	 */
	add(
		member: BudgetMember,
		live = true,
		proximity: Proximity = "visible",
	): void {
		this.entries.set(member, {
			member,
			proximity,
			live,
			seq: this.seq,
			shown: this.seq++,
		});
		this.rebalance();
	}

	setProximity(member: BudgetMember, proximity: Proximity): void {
		const entry = this.entries.get(member);
		if (!entry || entry.proximity === proximity) return;
		if (proximity === "visible") entry.shown = this.seq++;
		entry.proximity = proximity;
		this.rebalance();
	}

	/**
	 * Leave the budget. The member is going away (and disposes its own
	 * context), so it is not revoked; its slot goes to the next waiter.
	 */
	remove(member: BudgetMember): void {
		if (!this.entries.delete(member)) return;
		this.rebalance();
	}

	/**
	 * The member's context was lost outside the budget (the browser dropped
	 * it). It holds nothing now, so it is not revoked; like any waiter it is
	 * granted a slot again as soon as there is one for it.
	 */
	lose(member: BudgetMember): void {
		const entry = this.entries.get(member);
		if (!entry?.live) return;
		entry.live = false;
		this.rebalance();
	}

	/** Contexts currently held. */
	get live(): number {
		let count = 0;
		for (const entry of this.entries.values()) if (entry.live) count += 1;
		return count;
	}

	/** Members waiting for a context while within range. */
	get waiting(): number {
		let count = 0;
		for (const entry of this.entries.values()) {
			if (!entry.live && entry.proximity !== "far") count += 1;
		}
		return count;
	}

	get size(): number {
		return this.entries.size;
	}

	/**
	 * The live member to take a slot from: the oldest one that is only near,
	 * else the one on screen that came on screen longest ago. With a waiter,
	 * only a waiter on screen may take from the screen, and only from a
	 * member that came on screen before it did. Without one (a context that
	 * already exists must go), any on-screen member may give way.
	 */
	private evictable(waiter?: Entry): Entry | undefined {
		let best: Entry | undefined;
		for (const entry of this.entries.values()) {
			if (!entry.live || entry.proximity !== "near") continue;
			if (!best || entry.seq < best.seq) best = entry;
		}
		if (best || (waiter && waiter.proximity !== "visible")) return best;
		for (const entry of this.entries.values()) {
			if (!entry.live || entry.proximity !== "visible") continue;
			if (waiter && entry.shown >= waiter.shown) continue;
			if (!best || entry.shown < best.shown) best = entry;
		}
		return best;
	}

	private take(entry: Entry): void {
		entry.live = false;
		entry.member.revoke();
	}

	private give(entry: Entry): void {
		entry.live = true;
		entry.member.grant();
	}

	private rebalance(): void {
		// Far shaders always hand back.
		for (const entry of [...this.entries.values()]) {
			if (entry.live && entry.proximity === "far") this.take(entry);
		}
		// A member that joins live already holds its context and can overshoot
		// the cap: trim to it, only-near holders first, then the one on screen
		// longest, even when every holder is on screen.
		while (this.live > this.max) {
			const victim = this.evictable();
			if (!victim) break;
			this.take(victim);
		}
		// Serve the shaders that want one: on-screen first (the most recently
		// shown first), then by age.
		const waiting = [...this.entries.values()]
			.filter((entry) => !entry.live && entry.proximity !== "far")
			.sort(
				(a, b) =>
					Number(b.proximity === "visible") -
						Number(a.proximity === "visible") ||
					(a.proximity === "visible" ? b.shown - a.shown : a.seq - b.seq),
			);
		for (const entry of waiting) {
			// A grant can reenter (a renderer without WebGL2 leaves at once).
			if (entry.live || this.entries.get(entry.member) !== entry) continue;
			if (this.live < this.max) {
				this.give(entry);
			} else if (entry.proximity === "visible") {
				const victim = this.evictable(entry);
				if (!victim) continue;
				this.take(victim);
				this.give(entry);
			}
		}
	}
}

/** The page-wide budget every DitherShader joins. */
export const contextBudget = new ContextBudget();

/** Which `Proximity` the two intersection reports add up to. */
export function proximityOf(near: boolean, visible: boolean): Proximity {
	return visible ? "visible" : near ? "near" : "far";
}
