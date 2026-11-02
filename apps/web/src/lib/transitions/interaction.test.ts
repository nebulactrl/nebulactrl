import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { startInteractionRecorder, takeInteraction } from "./interaction";

beforeEach(() => {
	startInteractionRecorder();
	// Whatever an earlier test left unconsumed.
	takeInteraction();
});

afterEach(() => {
	document.body.innerHTML = "";
});

function el<K extends keyof HTMLElementTagNameMap>(
	tag: K,
	attrs: Record<string, string> = {},
	...children: Node[]
): HTMLElementTagNameMap[K] {
	const node = document.createElement(tag);
	for (const [name, value] of Object.entries(attrs))
		node.setAttribute(name, value);
	node.append(...children);
	return node;
}

function pointerDown(target: Element, button = 0) {
	target.dispatchEvent(
		new MouseEvent("pointerdown", { bubbles: true, button }),
	);
}

function keyDown(target: Element, key: string, repeat = false) {
	target.dispatchEvent(
		new KeyboardEvent("keydown", { key, repeat, bubbles: true }),
	);
}

describe("takeInteraction", () => {
	test("is null when nothing was pressed", () => {
		expect(takeInteraction()).toBeNull();
	});

	test("reports a primary-button press once; a later navigation cannot adopt it again", () => {
		const button = el("button");
		document.body.append(button);
		pointerDown(button);
		const taken = takeInteraction();
		expect(taken?.interaction).toMatchObject({
			source: "pointer",
			frequent: false,
			repeating: false,
		});
		expect(taken?.interaction.ageMs).toBeGreaterThanOrEqual(0);
		expect(takeInteraction()).toBeNull();
	});

	test("measures the age against the time it is given", () => {
		const button = el("button");
		document.body.append(button);
		pointerDown(button);
		const taken = takeInteraction(performance.now() + 5000);
		expect(taken?.interaction.ageMs).toBeGreaterThanOrEqual(5000);
	});

	test("ignores a secondary-button press", () => {
		const button = el("button");
		document.body.append(button);
		pointerDown(button, 2);
		expect(takeInteraction()).toBeNull();
	});

	test("the last press wins", () => {
		const button = el("button");
		document.body.append(button);
		pointerDown(button);
		keyDown(button, "Enter");
		expect(takeInteraction()?.interaction.source).toBe("keyboard");
	});

	test("a history navigation (back, forward) is recorded without an element", () => {
		window.dispatchEvent(new Event("popstate"));
		const taken = takeInteraction();
		expect(taken?.interaction.source).toBe("history");
		expect(taken?.pressed).toBeNull();
	});
});

describe("keyboard input", () => {
	test("Enter is a single keyboard press", () => {
		const link = el("a");
		document.body.append(link);
		keyDown(link, "Enter");
		expect(takeInteraction()?.interaction).toMatchObject({
			source: "keyboard",
			repeating: false,
		});
	});

	test("a held key and scrubbing keys are a stream of input", () => {
		const link = el("a");
		document.body.append(link);
		keyDown(link, "a", true);
		expect(takeInteraction()?.interaction.repeating).toBe(true);
		keyDown(link, "ArrowDown");
		expect(takeInteraction()?.interaction.repeating).toBe(true);
	});

	test.each(["Shift", "Control", "Alt", "Meta"])(
		"a held %s starts a gesture or chord and records nothing",
		(key) => {
			const link = el("a");
			document.body.append(link);
			keyDown(link, key);
			expect(takeInteraction()).toBeNull();
		},
	);
});

describe("frequent navigation", () => {
	test("a press in the primary sidebar is frequent", () => {
		const link = el("a");
		document.body.append(el("aside", { "aria-label": "Primary" }, link));
		pointerDown(link);
		expect(takeInteraction()?.interaction.frequent).toBe(true);
	});

	test("a press in the command palette is frequent", () => {
		const item = el("div");
		document.body.append(
			el("div", { role: "dialog", "aria-label": "Command palette" }, item),
		);
		pointerDown(item);
		expect(takeInteraction()?.interaction.frequent).toBe(true);
	});

	test("an element marked data-vt-frequent is frequent", () => {
		const item = el("span");
		document.body.append(el("div", { "data-vt-frequent": "" }, item));
		pointerDown(item);
		expect(takeInteraction()?.interaction.frequent).toBe(true);
	});

	test("a press elsewhere, and one in another aside, is not", () => {
		const other = el("button");
		document.body.append(el("aside", { "aria-label": "Secondary" }, other));
		pointerDown(other);
		expect(takeInteraction()?.interaction.frequent).toBe(false);
	});
});

describe("the pressed element", () => {
	test("is the nearest keyed ancestor of the press", () => {
		const inner = el("span");
		const card = el("div", { "data-vt": "project:p1" }, inner);
		document.body.append(card);
		pointerDown(inner);
		expect(takeInteraction()?.pressed).toBe(card);
	});

	test("for a press on a link is the keyed child inside it", () => {
		const label = el("span", { "data-vt": "nav:servers" });
		const link = el("a", {}, label);
		document.body.append(link);
		pointerDown(link);
		expect(takeInteraction()?.pressed).toBe(label);
	});

	test("an anchor is never a pressed source", () => {
		const anchor = el("div", { "data-vt": "project:p1", "data-vt-anchor": "" });
		const button = el("button", {}, anchor);
		document.body.append(button);
		pointerDown(anchor);
		expect(takeInteraction()?.pressed).toBeNull();
	});

	test("is null for a press that was not on a keyed element", () => {
		const button = el("button");
		document.body.append(button);
		pointerDown(button);
		expect(takeInteraction()?.pressed).toBeNull();
	});

	test("is null when the element has left the page by the time of the navigation", () => {
		const card = el("div", { "data-vt": "project:p1" });
		document.body.append(card);
		pointerDown(card);
		card.remove();
		expect(takeInteraction()?.pressed).toBeNull();
	});

	test("a press on the document itself has no element", () => {
		document.dispatchEvent(
			new MouseEvent("pointerdown", { bubbles: true, button: 0 }),
		);
		const taken = takeInteraction();
		expect(taken?.interaction.source).toBe("pointer");
		expect(taken?.pressed).toBeNull();
	});
});
