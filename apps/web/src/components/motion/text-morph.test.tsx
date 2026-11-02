import { afterEach, describe, expect, jest, test } from "bun:test";
import { render } from "@testing-library/react";
import { TextMorph } from "./text-morph";

let restoreMotion: (() => void) | undefined;

/** Tests run with reduced motion; the full-motion branch needs the preference off. */
function useFullMotion() {
	const real = window.matchMedia;
	window.matchMedia = ((query: string) => ({
		matches: false,
		media: query,
		onchange: null,
		addEventListener() {},
		removeEventListener() {},
		addListener() {},
		removeListener() {},
		dispatchEvent: () => false,
	})) as unknown as typeof window.matchMedia;
	return () => {
		window.matchMedia = real;
	};
}

afterEach(() => {
	jest.useRealTimers();
	restoreMotion?.();
	restoreMotion = undefined;
});

function label(container: HTMLElement): HTMLElement {
	return container.firstElementChild as HTMLElement;
}

describe("TextMorph", () => {
	test("the first text lands as plain text with no morph markup", () => {
		const { container } = render(<TextMorph text="Copy" className="extra" />);
		const el = label(container);
		expect(el).toHaveTextContent("Copy");
		expect(el.querySelector(".t-morph-swap")).toBeNull();
		expect(el).toHaveClass("t-morph", "extra");
	});

	test("only the letters that differ cross-blur, the shared ones stay put", () => {
		restoreMotion = useFullMotion();
		const { container, rerender } = render(<TextMorph text="Copy" />);
		jest.useFakeTimers();
		rerender(<TextMorph text="Copied" />);
		const el = label(container);
		const swap = el.querySelector(".t-morph-swap");
		expect(swap).not.toBeNull();
		// "Cop" is shared; "y" leaves (hidden from assistive tech), "ied" arrives.
		expect(el.firstChild?.textContent).toBe("Cop");
		const out = swap?.querySelector(".t-morph-old");
		expect(out).toHaveTextContent("y");
		expect(out).toHaveAttribute("aria-hidden", "true");
		expect(swap?.querySelector(".t-morph-new")).toHaveTextContent("ied");
		expect(swap).toHaveClass("is-morphing");
	});

	test("settles back to the plain new text once the morph has run", () => {
		restoreMotion = useFullMotion();
		const { container, rerender } = render(<TextMorph text="Save" />);
		jest.useFakeTimers();
		rerender(<TextMorph text="Saved" />);
		const el = label(container);
		expect(el.querySelector(".t-morph-swap")).not.toBeNull();
		jest.advanceTimersByTime(2000);
		expect(el.querySelector(".t-morph-swap")).toBeNull();
		expect(el.textContent).toBe("Saved");
		expect(el.dataset.text).toBe("Saved");
	});

	test("text that shares nothing swaps whole", () => {
		restoreMotion = useFullMotion();
		const { container, rerender } = render(<TextMorph text="Processing" />);
		jest.useFakeTimers();
		rerender(<TextMorph text="Done" />);
		const el = label(container);
		expect(el.querySelector(".t-morph-old")).toHaveTextContent("Processing");
		expect(el.querySelector(".t-morph-new")).toHaveTextContent("Done");
		jest.advanceTimersByTime(2000);
		expect(el.textContent).toBe("Done");
	});

	test("instant changes land at once with no morph markup", () => {
		restoreMotion = useFullMotion();
		const { container, rerender } = render(<TextMorph text="Copy" instant />);
		rerender(<TextMorph text="Copied" instant />);
		const el = label(container);
		expect(el.textContent).toBe("Copied");
		expect(el.querySelector(".t-morph-swap")).toBeNull();
	});

	test("reduced motion lands the new text at once", () => {
		const { container, rerender } = render(<TextMorph text="Save" />);
		rerender(<TextMorph text="Saved" />);
		const el = label(container);
		expect(el.textContent).toBe("Saved");
		expect(el.querySelector(".t-morph-swap")).toBeNull();
	});

	test("re-rendering with the same text starts no morph", () => {
		const { container, rerender } = render(<TextMorph text="Copy" />);
		rerender(<TextMorph text="Copy" className="other" />);
		expect(label(container).querySelector(".t-morph-swap")).toBeNull();
		expect(label(container).textContent).toBe("Copy");
	});

	test("a second change mid-morph starts from the text being shown", () => {
		restoreMotion = useFullMotion();
		const { container, rerender } = render(<TextMorph text="Save" />);
		jest.useFakeTimers();
		rerender(<TextMorph text="Saved" />);
		rerender(<TextMorph text="Saving" />);
		const el = label(container);
		// Morphs from "Saved" (the text in flight), not from the original "Save".
		expect(el.querySelector(".t-morph-old")).toHaveTextContent("ed");
		expect(el.querySelector(".t-morph-new")).toHaveTextContent("ing");
		jest.advanceTimersByTime(2000);
		expect(el.textContent).toBe("Saving");
	});
});
