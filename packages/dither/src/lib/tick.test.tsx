import { afterEach, beforeEach, describe, expect, spyOn, test } from "bun:test";
import { render, waitFor } from "@testing-library/react";
import { useTick } from "./tick";

function Counter({ value }: { value: string }) {
	return (
		<span data-testid="count" ref={useTick()}>
			{value}
		</span>
	);
}

function stubMatchMedia(reduce: boolean) {
	const original = window.matchMedia;
	window.matchMedia = ((query: string) =>
		({
			matches: reduce && query.includes("prefers-reduced-motion"),
			media: query,
		}) as MediaQueryList) as typeof window.matchMedia;
	return () => {
		window.matchMedia = original;
	};
}

let restore: (() => void) | null = null;
// The shared preload pins reduced motion on; these tests need full motion
// unless they say otherwise.
beforeEach(() => {
	restore = stubMatchMedia(false);
});
afterEach(() => {
	restore?.();
	restore = null;
});

describe("useTick", () => {
	test("never animates the first value", async () => {
		const animate = spyOn(Element.prototype, "animate");
		try {
			render(<Counter value="1" />);
			// Let any pending mutation callbacks flush.
			await Promise.resolve();
			expect(animate).not.toHaveBeenCalled();
		} finally {
			animate.mockRestore();
		}
	});

	test("rolls up and unblurs when the text changes", async () => {
		const animate = spyOn(Element.prototype, "animate");
		try {
			const { rerender } = render(<Counter value="1" />);
			rerender(<Counter value="2" />);
			await waitFor(() => expect(animate).toHaveBeenCalledTimes(2));

			const [roll, blur] = animate.mock.calls;
			expect(roll?.[0]).toEqual([
				{ transform: "translateY(60%)" },
				{ transform: "none" },
			]);
			expect(roll?.[1]).toMatchObject({ duration: 420 });
			expect(blur?.[0]).toEqual([
				{ opacity: 0, filter: "blur(3px)" },
				{ opacity: 1, filter: "blur(0px)" },
			]);
			expect(blur?.[1]).toMatchObject({ duration: 220 });
		} finally {
			animate.mockRestore();
		}
	});

	test("does not animate when the text is rewritten to the same value", async () => {
		const animate = spyOn(Element.prototype, "animate");
		try {
			const { getByTestId } = render(<Counter value="7" />);
			const el = getByTestId("count");
			// A mutation that leaves the text identical is not a tick.
			el.textContent = "7";
			await Promise.resolve();
			await Promise.resolve();
			expect(animate).not.toHaveBeenCalled();
		} finally {
			animate.mockRestore();
		}
	});

	test("stays still under prefers-reduced-motion", async () => {
		restore?.();
		restore = stubMatchMedia(true);
		const animate = spyOn(Element.prototype, "animate");
		try {
			const { rerender, getByTestId } = render(<Counter value="1" />);
			rerender(<Counter value="2" />);
			await waitFor(() => expect(getByTestId("count")).toHaveTextContent("2"));
			await Promise.resolve();
			expect(animate).not.toHaveBeenCalled();
		} finally {
			animate.mockRestore();
		}
	});

	test("stops observing once the element unmounts", async () => {
		const animate = spyOn(Element.prototype, "animate");
		try {
			const { getByTestId, unmount } = render(<Counter value="1" />);
			const el = getByTestId("count");
			unmount();
			el.textContent = "2";
			await Promise.resolve();
			await Promise.resolve();
			expect(animate).not.toHaveBeenCalled();
		} finally {
			animate.mockRestore();
		}
	});
});
