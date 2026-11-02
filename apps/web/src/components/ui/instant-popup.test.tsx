import { describe, expect, it } from "bun:test";
import { render, screen } from "@testing-library/react";
import { createRef, type Ref } from "react";
import { useInstantPopupRef } from "#/components/ui/instant-popup";

function Popup({ forwardedRef }: { forwardedRef?: Ref<HTMLDivElement> }) {
	const ref = useInstantPopupRef<HTMLDivElement>(forwardedRef);
	return (
		<div ref={ref} data-testid="popup">
			popup
		</div>
	);
}

const press = (type: string, init: KeyboardEventInit | EventInit = {}) =>
	document.dispatchEvent(new Event(type, { bubbles: true, ...init }));
const key = (k: string) =>
	document.dispatchEvent(
		new KeyboardEvent("keydown", { key: k, bubbles: true }),
	);

describe("useInstantPopupRef", () => {
	it("marks the popup data-instant while the keyboard was the last input, and unmarks it after a pointer press", () => {
		press("pointerdown");
		render(<Popup />);
		const popup = screen.getByTestId("popup");
		expect(popup).not.toHaveAttribute("data-instant");

		key("ArrowDown");
		expect(popup).toHaveAttribute("data-instant");

		press("pointerdown");
		expect(popup).not.toHaveAttribute("data-instant");
	});

	it("marks a popup that mounts right after a key press", () => {
		key("Enter");
		render(<Popup />);
		expect(screen.getByTestId("popup")).toHaveAttribute("data-instant");
		press("mousedown");
	});

	it("follows mouse and touch presses too", () => {
		key("a");
		render(<Popup />);
		const popup = screen.getByTestId("popup");
		press("mousedown");
		expect(popup).not.toHaveAttribute("data-instant");
		key("a");
		expect(popup).toHaveAttribute("data-instant");
		press("touchstart");
		expect(popup).not.toHaveAttribute("data-instant");
	});

	it("does not treat a lone modifier key as keyboard input", () => {
		press("pointerdown");
		render(<Popup />);
		key("Shift");
		expect(screen.getByTestId("popup")).not.toHaveAttribute("data-instant");
	});

	it("stops following input once the popup unmounts", () => {
		press("pointerdown");
		const { unmount } = render(<Popup />);
		const popup = screen.getByTestId("popup");
		unmount();
		key("ArrowDown");
		expect(popup).not.toHaveAttribute("data-instant");
		press("pointerdown");
	});

	it("forwards the node to an object ref, and clears it on unmount", () => {
		const ref = createRef<HTMLDivElement>();
		const { unmount } = render(<Popup forwardedRef={ref} />);
		expect(ref.current).toBe(screen.getByTestId("popup"));
		unmount();
		expect(ref.current).toBeNull();
	});

	it("forwards the node to a callback ref, and calls it with null on unmount", () => {
		const seen: (HTMLDivElement | null)[] = [];
		const { unmount } = render(
			<Popup forwardedRef={(node) => void seen.push(node)} />,
		);
		expect(seen[0]).toBe(screen.getByTestId("popup"));
		unmount();
		expect(seen.at(-1)).toBeNull();
	});
});
