import { describe, expect, test } from "bun:test";
import {
	type BudgetMember,
	ContextBudget,
	MAX_LIVE_CONTEXTS,
	proximityOf,
} from "./context-budget";

function member() {
	const log: string[] = [];
	const m: BudgetMember & { log: string[] } = {
		log,
		grant: () => log.push("grant"),
		revoke: () => log.push("revoke"),
	};
	return m;
}

function fill(
	budget: ContextBudget,
	count: number,
	proximity: "near" | "visible",
) {
	const members = Array.from({ length: count }, member);
	for (const m of members) budget.add(m, true, proximity);
	return members;
}

describe("ContextBudget", () => {
	test("the cap is eight live contexts", () => {
		expect(MAX_LIVE_CONTEXTS).toBe(8);
		expect(new ContextBudget().max).toBe(8);
	});

	test("far shaders hand their context back and take it again when near", () => {
		const budget = new ContextBudget();
		const m = member();
		budget.add(m);
		budget.setProximity(m, "far");
		expect(budget.live).toBe(0);
		expect(m.log).toEqual(["revoke"]);
		budget.setProximity(m, "near");
		expect(budget.live).toBe(1);
		expect(m.log).toEqual(["revoke", "grant"]);
	});

	test("a ninth visible shader takes the slot of one that is only near", () => {
		const budget = new ContextBudget();
		const near = fill(budget, 8, "near");
		expect(budget.live).toBe(8);
		const ninth = member();
		budget.add(ninth, false, "visible");
		expect(ninth.log).toEqual(["grant"]);
		// The oldest near shader lost its context.
		expect(near[0]?.log).toEqual(["revoke"]);
		expect(near.slice(1).every((m) => m.log.length === 0)).toBe(true);
		expect(budget.live).toBe(8);
	});

	// A sheet opened over a page whose shaders hold every slot: the page's
	// shaders still report themselves visible behind it, but the sheet's must draw.
	test("a shader that comes on screen takes the slot of the one shown longest ago", () => {
		const budget = new ContextBudget();
		const page = fill(budget, 8, "visible");
		const sheet = member();
		budget.add(sheet, false, "visible");
		expect(sheet.log).toEqual(["grant"]);
		expect(page[0]?.log).toEqual(["revoke"]);
		expect(page.slice(1).every((m) => m.log.length === 0)).toBe(true);
		expect(budget.live).toBe(8);

		// Later rebalances never hand it back to the older shader.
		budget.add(member(), false, "near");
		expect(sheet.log).toEqual(["grant"]);
		expect(page[0]?.log).toEqual(["revoke"]);

		// The sheet closes: the page's shader gets its slot back.
		budget.remove(sheet);
		expect(page[0]?.log).toEqual(["revoke", "grant"]);
		expect(budget.live).toBe(8);
	});

	test("a near shader waits for a free slot instead of evicting", () => {
		const budget = new ContextBudget();
		fill(budget, 8, "near");
		const waiter = member();
		budget.add(waiter, false, "near");
		expect(waiter.log).toEqual([]);
		expect(budget.live).toBe(8);
	});

	test("removing a member frees its slot for the next waiter", () => {
		const budget = new ContextBudget();
		const holders = fill(budget, 8, "visible");
		const waiter = member();
		budget.add(waiter, false, "near");
		expect(waiter.log).toEqual([]);
		budget.remove(holders[0] as BudgetMember);
		expect(waiter.log).toEqual(["grant"]);
		expect(budget.size).toBe(8);
	});

	// Every renderer acquires a context on construction and joins as visible
	// until its first report, so twelve shaders mounting at once hold twelve
	// contexts unless the budget takes the surplus back at the door.
	test("startup overshoot with every shader on screen is trimmed at once", () => {
		const budget = new ContextBudget();
		const all = Array.from({ length: 13 }, member);
		for (const m of all) {
			budget.add(m);
			expect(budget.live).toBeLessThanOrEqual(budget.max);
		}
		expect(budget.live).toBe(8);
		expect(budget.waiting).toBe(5);
		// The oldest on screen gave way; the newest eight hold the slots.
		expect(all.slice(0, 5).every((m) => m.log.join() === "revoke")).toBe(true);
		expect(all.slice(5).every((m) => m.log.length === 0)).toBe(true);

		// Reports that change nothing never mint a slot or move one.
		for (const m of all) budget.setProximity(m, "visible");
		expect(budget.live).toBe(8);
		expect(all.slice(0, 5).every((m) => m.log.join() === "revoke")).toBe(true);

		// A holder leaving hands its slot to a waiter, still within the cap.
		budget.remove(all[12] as BudgetMember);
		expect(budget.live).toBe(8);
		expect(budget.waiting).toBe(4);
	});

	test("startup overshoot takes the slot of a shader that is only near first", () => {
		const budget = new ContextBudget();
		const near = fill(budget, 8, "near");
		const newcomer = member();
		budget.add(newcomer);
		expect(budget.live).toBe(8);
		expect(near[0]?.log).toEqual(["revoke"]);
		expect(newcomer.log).toEqual([]);
	});

	test("a shader that joins far holds nothing until it is reported near", () => {
		const budget = new ContextBudget();
		const m = member();
		budget.add(m, false, "far");
		expect(m.log).toEqual([]);
		budget.setProximity(m, "near");
		expect(m.log).toEqual(["grant"]);
		expect(budget.live).toBe(1);
	});

	test("a context lost outside the budget is granted again when a slot is free", () => {
		const budget = new ContextBudget();
		const m = member();
		budget.add(m, false, "visible");
		budget.lose(m);
		expect(m.log).toEqual(["grant", "grant"]);
		expect(budget.live).toBe(1);
	});

	test("losing a context the member does not hold changes nothing", () => {
		const budget = new ContextBudget();
		const holders = fill(budget, 8, "visible");
		const waiter = member();
		budget.add(waiter, false, "near");
		budget.lose(waiter);
		expect(waiter.log).toEqual([]);
		expect(budget.live).toBe(8);
		expect(holders.every((m) => m.log.length === 0)).toBe(true);
	});

	test("a member that leaves while being granted does not break the pass", () => {
		const budget = new ContextBudget();
		const holders = fill(budget, 8, "visible");
		const gone: BudgetMember = {
			grant: () => budget.remove(gone),
			revoke: () => {},
		};
		const stays = member();
		budget.add(gone, false, "near");
		budget.add(stays, false, "near");
		budget.remove(holders[0] as BudgetMember);
		expect(budget.size).toBe(8);
		expect(budget.live).toBeLessThanOrEqual(8);
		expect(stays.log).toEqual(["grant"]);
	});
});

describe("proximityOf", () => {
	test("visible beats near beats far", () => {
		expect(proximityOf(true, true)).toBe("visible");
		expect(proximityOf(true, false)).toBe("near");
		expect(proximityOf(false, false)).toBe("far");
	});
});
