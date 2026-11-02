import { afterEach, describe, expect, test } from "bun:test";
import { morphText, setMorphText, sharedAffixes } from "./text-morph";

describe("sharedAffixes", () => {
	test("Copy -> Copied keeps the shared start", () => {
		expect(sharedAffixes("Copy", "Copied")).toEqual({ start: 3, end: 0 });
	});

	test("shared end is found after the start", () => {
		// "Copy page as Markdown" -> "Copied as Markdown"
		const prev = "Copy page as Markdown";
		const next = "Copied as Markdown";
		const { start, end } = sharedAffixes(prev, next);
		expect(prev.slice(0, start)).toBe("Cop");
		expect(prev.slice(prev.length - end)).toBe(" as Markdown");
	});

	test("the two shared runs never overlap", () => {
		expect(sharedAffixes("aa", "aaa")).toEqual({ start: 2, end: 0 });
		expect(sharedAffixes("abab", "ab")).toEqual({ start: 2, end: 0 });
	});

	test("texts that share nothing change as a whole", () => {
		expect(sharedAffixes("Processing", "Done")).toEqual({ start: 0, end: 0 });
	});

	test("identical texts share everything", () => {
		expect(sharedAffixes("Save", "Save")).toEqual({ start: 4, end: 0 });
	});
});

describe("setMorphText", () => {
	test("sets the text and its source of truth at once, and cancels a pending morph", () => {
		const el = document.createElement("span");
		const cleared: unknown[] = [];
		const realClear = window.clearTimeout;
		window.clearTimeout = ((handle?: number) => {
			cleared.push(handle);
		}) as typeof window.clearTimeout;
		try {
			(el as unknown as { _morphTimer?: number })._morphTimer = 77;
			setMorphText(el, "Saved");
		} finally {
			window.clearTimeout = realClear;
		}
		expect(el.textContent).toBe("Saved");
		expect(el.dataset.text).toBe("Saved");
		expect(cleared).toEqual([77]);
	});
});

describe("morphText", () => {
	const realMatchMedia = window.matchMedia;
	const realSetTimeout = window.setTimeout;
	let pending: Array<() => void>;

	function fullMotion() {
		window.matchMedia = ((query: string) => ({
			matches: false,
			media: query,
			addEventListener() {},
			removeEventListener() {},
		})) as unknown as typeof window.matchMedia;
		pending = [];
		window.setTimeout = ((task: () => void) => {
			pending.push(task);
			return pending.length;
		}) as unknown as typeof window.setTimeout;
	}

	afterEach(() => {
		window.matchMedia = realMatchMedia;
		window.setTimeout = realSetTimeout;
		document.body.innerHTML = "";
	});

	function label(text: string): HTMLElement {
		const el = document.createElement("span");
		el.textContent = text;
		document.body.append(el);
		return el;
	}

	test("does nothing when the text is unchanged", () => {
		const el = label("Save");
		const before = el.innerHTML;
		morphText(el, "Save");
		expect(el.innerHTML).toBe(before);
	});

	test("under reduced motion the new text lands at once", () => {
		const el = label("Copy");
		morphText(el, "Copied");
		expect(el.textContent).toBe("Copied");
		expect(el.dataset.text).toBe("Copied");
		expect(el.querySelector(".t-morph-swap")).toBeNull();
	});

	test("an element that is not on the page changes at once", () => {
		fullMotion();
		const el = document.createElement("span");
		el.textContent = "Copy";
		morphText(el, "Copied");
		expect(el.textContent).toBe("Copied");
		expect(el.querySelector(".t-morph-swap")).toBeNull();
	});

	test("keeps the shared letters in place and cross-fades only the part that differs", () => {
		fullMotion();
		const el = label("Copy page as Markdown");
		morphText(el, "Copied as Markdown");

		const swap = el.querySelector(".t-morph-swap");
		expect(swap?.classList.contains("is-morphing")).toBe(true);
		expect(swap?.querySelector(".t-morph-old")?.textContent).toBe("y page");
		expect(
			swap?.querySelector(".t-morph-old")?.getAttribute("aria-hidden"),
		).toBe("true");
		expect(swap?.querySelector(".t-morph-new")?.textContent).toBe("ied");
		expect(el.firstChild?.textContent).toBe("Cop");
		expect(el.lastChild?.textContent).toBe(" as Markdown");
		// The text a second change would start from is the new text.
		expect(el.dataset.text).toBe("Copied as Markdown");
	});

	test("settles to the plain new text once the morph has run", () => {
		fullMotion();
		const el = label("Save");
		morphText(el, "Saved");
		expect(el.querySelector(".t-morph-swap")).not.toBeNull();
		expect(pending).toHaveLength(1);
		pending[0]?.();
		expect(el.textContent).toBe("Saved");
		expect(el.querySelector(".t-morph-swap")).toBeNull();
	});

	test("text that shares nothing swaps as a whole", () => {
		fullMotion();
		const el = label("Processing");
		morphText(el, "Done");
		const swap = el.querySelector(".t-morph-swap");
		expect(swap?.querySelector(".t-morph-old")?.textContent).toBe("Processing");
		expect(swap?.querySelector(".t-morph-new")?.textContent).toBe("Done");
		expect(el.childNodes).toHaveLength(3);
	});

	test("a second change mid-flight starts from the text the first one was heading to", () => {
		fullMotion();
		const el = label("Save");
		morphText(el, "Saved");
		morphText(el, "Saving");
		const swap = el.querySelector(".t-morph-swap");
		expect(swap?.querySelector(".t-morph-old")?.textContent).toBe("ed");
		expect(swap?.querySelector(".t-morph-new")?.textContent).toBe("ing");
		expect(el.dataset.text).toBe("Saving");
	});
});
