import { describe, expect, test } from "bun:test";
import {
	classifyNavigation,
	INTERACTION_FRESH_MS,
	isRepeatingKey,
	type NavInput,
	type NavInteraction,
	RAPID_MS,
	selectArmed,
} from "./nav-plan";

function input(overrides: Partial<NavInput> = {}): NavInput {
	return {
		supported: true,
		pathChanged: true,
		reducedMotion: false,
		interaction: null,
		sinceLastMs: 10_000,
		...overrides,
	};
}

function press(overrides: Partial<NavInteraction> = {}): NavInteraction {
	return {
		source: "pointer",
		frequent: false,
		repeating: false,
		ageMs: 40,
		...overrides,
	};
}

describe("classifyNavigation", () => {
	test("a plain navigation plays the page variant with shared elements", () => {
		expect(classifyNavigation(input())).toMatchObject({
			variant: "page",
			shared: true,
		});
	});

	test("a card press plays the page variant", () => {
		expect(classifyNavigation(input({ interaction: press() }))).toMatchObject({
			variant: "page",
			shared: true,
		});
	});

	test("back and forward play the page variant", () => {
		expect(
			classifyNavigation(input({ interaction: press({ source: "history" }) })),
		).toMatchObject({ variant: "page", shared: true });
	});

	test("sidebar and palette presses are the fast variant", () => {
		expect(
			classifyNavigation(input({ interaction: press({ frequent: true }) })),
		).toMatchObject({ variant: "fast", shared: true });
	});

	test("a keyboard activation is the fast variant", () => {
		expect(
			classifyNavigation(input({ interaction: press({ source: "keyboard" }) })),
		).toMatchObject({ variant: "fast", shared: true });
	});

	test("a held or arrow-style key never animates", () => {
		const plan = classifyNavigation(
			input({ interaction: press({ source: "keyboard", repeating: true }) }),
		);
		expect(plan.variant).toBe("none");
		expect(plan.reason).toBe("repeating-key");
	});

	test("reduced motion is a plain cross-fade with nothing morphing", () => {
		expect(
			classifyNavigation(
				input({ reducedMotion: true, interaction: press({ frequent: true }) }),
			),
		).toMatchObject({ variant: "reduced", shared: false });
	});

	test("reduced motion still skips repeating keys", () => {
		expect(
			classifyNavigation(
				input({
					reducedMotion: true,
					interaction: press({ source: "keyboard", repeating: true }),
				}),
			).variant,
		).toBe("none");
	});

	test("no API means instant navigation, whatever else holds", () => {
		const plan = classifyNavigation(input({ supported: false }));
		expect(plan).toMatchObject({ variant: "none", shared: false });
		expect(plan.reason).toBe("unsupported");
	});

	test("a search-only or initial navigation does not animate", () => {
		const plan = classifyNavigation(input({ pathChanged: false }));
		expect(plan.variant).toBe("none");
		expect(plan.reason).toBe("same-path");
	});

	test("navigations in rapid succession animate only the first", () => {
		expect(
			classifyNavigation(input({ sinceLastMs: RAPID_MS - 1 })).variant,
		).toBe("none");
		expect(classifyNavigation(input({ sinceLastMs: RAPID_MS })).variant).toBe(
			"page",
		);
	});

	test("a stale press does not make the navigation frequent or repeating", () => {
		const stale = press({
			frequent: true,
			repeating: true,
			ageMs: INTERACTION_FRESH_MS + 1,
		});
		expect(classifyNavigation(input({ interaction: stale }))).toMatchObject({
			variant: "page",
			shared: true,
		});
	});
});

describe("isRepeatingKey", () => {
	test("auto-repeat and scrubbing keys are repeating", () => {
		expect(isRepeatingKey({ key: "j", repeat: true })).toBe(true);
		for (const key of ["ArrowDown", "ArrowUp", "ArrowLeft", "ArrowRight"]) {
			expect(isRepeatingKey({ key, repeat: false })).toBe(true);
		}
		expect(isRepeatingKey({ key: "PageDown", repeat: false })).toBe(true);
	});

	test("Enter, letters and shortcuts are not", () => {
		expect(isRepeatingKey({ key: "Enter", repeat: false })).toBe(false);
		expect(isRepeatingKey({ key: "g", repeat: false })).toBe(false);
		expect(isRepeatingKey({ key: "toString", repeat: false })).toBe(false);
	});
});

describe("selectArmed", () => {
	const card = { key: "project:1", element: "card" };
	const canvas = { key: "project:1", element: "canvas" };
	const header = { key: "server:2", element: "header" };

	test("a pressed element is armed on its own, anchors stay quiet", () => {
		expect(
			selectArmed({ shared: true, pressed: card, anchors: [header] }),
		).toEqual([card]);
	});

	test("without a pressed element the leaving page's anchors are armed", () => {
		expect(
			selectArmed({ shared: true, pressed: null, anchors: [canvas] }),
		).toEqual([canvas]);
	});

	test("duplicate anchor keys are armed once and the count is capped", () => {
		const many = [1, 2, 3, 4, 5].map((n) => ({
			key: `server:${n}`,
			element: `server-${n}`,
		}));
		expect(
			selectArmed({
				shared: true,
				pressed: null,
				anchors: [canvas, canvas, ...many],
			}).map((item) => item.key),
		).toEqual(["project:1", "server:1", "server:2"]);
	});

	test("nothing is armed when the navigation does not share elements", () => {
		expect(
			selectArmed({ shared: false, pressed: card, anchors: [canvas] }),
		).toEqual([]);
	});
});
