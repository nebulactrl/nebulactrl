import { describe, expect, spyOn, test } from "bun:test";
import { render, waitFor } from "@testing-library/react";
import { LiveValue } from "./live-value";

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

/** One macrotask: every pending MutationObserver callback has run by then. */
const flush = () => new Promise<void>((resolve) => setTimeout(resolve, 0));

describe("LiveValue", () => {
	test("shows the formatted text it is given, with the extra class", () => {
		const { container } = render(
			<LiveValue className="text-lg">12.5%</LiveValue>,
		);
		const span = container.firstElementChild as HTMLElement;
		expect(span).toHaveTextContent("12.5%");
		expect(span).toHaveClass("tabular-nums", "text-lg");
	});

	test("the first value does not animate", async () => {
		const animate = spyOn(Element.prototype, "animate");
		try {
			render(<LiveValue>212 MiB</LiveValue>);
			await flush();
			expect(animate).not.toHaveBeenCalled();
		} finally {
			animate.mockRestore();
		}
	});

	test("a changed value replaces the text and plays the roll", async () => {
		const restore = useFullMotion();
		const animate = spyOn(Element.prototype, "animate");
		try {
			const { container, rerender } = render(<LiveValue>212 MiB</LiveValue>);
			rerender(<LiveValue>240 MiB</LiveValue>);
			const span = container.firstElementChild as HTMLElement;
			expect(span).toHaveTextContent("240 MiB");
			await waitFor(() => expect(animate).toHaveBeenCalled());
			const frames = animate.mock.calls.map(([keyframes]) => keyframes);
			expect(frames).toContainEqual([
				{ transform: "translateY(60%)" },
				{ transform: "none" },
			]);
		} finally {
			animate.mockRestore();
			restore();
		}
	});

	test("re-rendering with the same value does not animate again", async () => {
		const restore = useFullMotion();
		const { rerender } = render(<LiveValue>1</LiveValue>);
		const animate = spyOn(Element.prototype, "animate");
		try {
			rerender(<LiveValue>1</LiveValue>);
			await flush();
			expect(animate).not.toHaveBeenCalled();
		} finally {
			animate.mockRestore();
			restore();
		}
	});

	test("reduced motion swaps the text without animating", async () => {
		const animate = spyOn(Element.prototype, "animate");
		try {
			const { container, rerender } = render(<LiveValue>1</LiveValue>);
			rerender(<LiveValue>2</LiveValue>);
			await flush();
			expect(container.firstElementChild).toHaveTextContent("2");
			expect(animate).not.toHaveBeenCalled();
		} finally {
			animate.mockRestore();
		}
	});
});
