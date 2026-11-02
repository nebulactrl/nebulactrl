import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import { fireEvent, render, screen } from "@testing-library/react";
import type { KeyboardEvent } from "react";
import { trapTabKey } from "#/components/ui/focus-trap";

// happy-dom has no layout, so every element reports zero client rects and
// would count as hidden. Elements under `[data-test-hidden]` keep that answer;
// everything else behaves as if it were rendered.
const originalGetClientRects = HTMLElement.prototype.getClientRects;
beforeEach(() => {
	HTMLElement.prototype.getClientRects = function (this: HTMLElement) {
		const hidden = this.closest("[data-test-hidden]") !== null;
		return (hidden ? [] : [{}]) as unknown as DOMRectList;
	};
});
afterEach(() => {
	HTMLElement.prototype.getClientRects = originalGetClientRects;
});

function Trap({
	onKeyDown = trapTabKey,
	children,
}: {
	onKeyDown?: (event: KeyboardEvent<HTMLElement>) => void;
	children: React.ReactNode;
}) {
	return (
		<div role="dialog" aria-label="trap" tabIndex={-1} onKeyDown={onKeyDown}>
			{children}
		</div>
	);
}

function tab(element: Element, shiftKey = false) {
	// fireEvent returns false when a handler called preventDefault.
	return !fireEvent.keyDown(element, { key: "Tab", shiftKey });
}

describe("trapTabKey", () => {
	it("wraps Tab from the last control to the first", () => {
		render(
			<Trap>
				<button type="button">one</button>
				<button type="button">two</button>
			</Trap>,
		);
		const last = screen.getByRole("button", { name: "two" });
		last.focus();
		expect(tab(last)).toBe(true);
		expect(screen.getByRole("button", { name: "one" })).toHaveFocus();
	});

	it("wraps Shift+Tab from the first control to the last", () => {
		render(
			<Trap>
				<button type="button">one</button>
				<button type="button">two</button>
			</Trap>,
		);
		const first = screen.getByRole("button", { name: "one" });
		first.focus();
		expect(tab(first, true)).toBe(true);
		expect(screen.getByRole("button", { name: "two" })).toHaveFocus();
	});

	it("wraps Shift+Tab from the popup itself to the last control", () => {
		render(
			<Trap>
				<button type="button">one</button>
				<button type="button">two</button>
			</Trap>,
		);
		const popup = screen.getByRole("dialog");
		popup.focus();
		expect(tab(popup, true)).toBe(true);
		expect(screen.getByRole("button", { name: "two" })).toHaveFocus();
	});

	it("leaves Tab alone in the middle of the popup", () => {
		render(
			<Trap>
				<button type="button">one</button>
				<button type="button">two</button>
				<button type="button">three</button>
			</Trap>,
		);
		const middle = screen.getByRole("button", { name: "two" });
		middle.focus();
		expect(tab(middle)).toBe(false);
		expect(tab(middle, true)).toBe(false);
		expect(middle).toHaveFocus();
	});

	it("ignores keys other than Tab", () => {
		render(
			<Trap>
				<button type="button">only</button>
			</Trap>,
		);
		const only = screen.getByRole("button", { name: "only" });
		only.focus();
		expect(!fireEvent.keyDown(only, { key: "Enter" })).toBe(false);
	});

	it("does nothing when another handler already prevented the key", () => {
		render(
			<Trap
				onKeyDown={(event) => {
					event.preventDefault();
					trapTabKey(event);
				}}
			>
				<button type="button">one</button>
				<button type="button">two</button>
			</Trap>,
		);
		const last = screen.getByRole("button", { name: "two" });
		last.focus();
		tab(last);
		expect(last).toHaveFocus();
	});

	it("skips a disabled last control, so the real last stop wraps", () => {
		render(
			<Trap>
				<button type="button">one</button>
				<button type="button">two</button>
				<button type="button" disabled>
					gone
				</button>
			</Trap>,
		);
		const last = screen.getByRole("button", { name: "two" });
		last.focus();
		expect(tab(last)).toBe(true);
		expect(screen.getByRole("button", { name: "one" })).toHaveFocus();
	});

	it("skips disabled inputs, selects and textareas", () => {
		render(
			<Trap>
				<input aria-label="a" />
				<input aria-label="b" disabled />
				<select aria-label="c" disabled />
				<textarea aria-label="d" disabled />
			</Trap>,
		);
		const first = screen.getByLabelText("a");
		first.focus();
		// `a` is both first and last tabbable: Tab wraps onto itself.
		expect(tab(first)).toBe(true);
		expect(first).toHaveFocus();
	});

	it("skips controls with a negative tabindex, hidden or inert subtrees, and visually absent ones", () => {
		render(
			<Trap>
				<button type="button">first</button>
				<button type="button">last</button>
				<button type="button" tabIndex={-1}>
					skipped negative
				</button>
				<div hidden>
					<button type="button">in hidden</button>
				</div>
				<div inert>
					<button type="button">in inert</button>
				</div>
				<div data-test-hidden>
					<button type="button">no layout</button>
				</div>
			</Trap>,
		);
		const last = screen.getByRole("button", { name: "last" });
		last.focus();
		expect(tab(last)).toBe(true);
		expect(screen.getByRole("button", { name: "first" })).toHaveFocus();
	});

	it("skips the primitive's focus guards", () => {
		render(
			<Trap>
				<span data-base-ui-focus-guard="">
					<button type="button">guard</button>
				</span>
				<button type="button">real</button>
			</Trap>,
		);
		const real = screen.getByRole("button", { name: "real" });
		real.focus();
		// The guard is not a stop: `real` is the only one, so Tab wraps onto it.
		expect(tab(real)).toBe(true);
		expect(real).toHaveFocus();
	});

	it("holds focus on the popup when it has no tabbable control", () => {
		render(
			<Trap>
				<p>just text</p>
			</Trap>,
		);
		const popup = screen.getByRole("dialog");
		popup.focus();
		expect(tab(popup)).toBe(true);
		expect(popup).toHaveFocus();
	});

	it("includes links with an href, and ignores anchors without one", () => {
		render(
			<Trap>
				<a href="/x">settings</a>
				{/* biome-ignore lint/a11y/useValidAnchor: an anchor without an href is the case under test */}
				<a>plain</a>
			</Trap>,
		);
		const link = screen.getByRole("link", { name: "settings" });
		link.focus();
		expect(tab(link)).toBe(true);
		expect(link).toHaveFocus();
	});

	it("leaves a key from a portalled child (outside the popup's DOM) alone", () => {
		render(
			<>
				<Trap>
					<button type="button">one</button>
					<button type="button">two</button>
				</Trap>
				<button type="button">elsewhere</button>
			</>,
		);
		// Simulate React's bubbling of a portal event: the handler fires with a
		// target that is not inside currentTarget.
		const popup = screen.getByRole("dialog");
		const outside = screen.getByRole("button", { name: "elsewhere" });
		outside.focus();
		let prevented = false;
		trapTabKey({
			key: "Tab",
			shiftKey: false,
			defaultPrevented: false,
			currentTarget: popup,
			target: outside,
			preventDefault: () => {
				prevented = true;
			},
		} as unknown as KeyboardEvent<HTMLElement>);
		expect(prevented).toBe(false);
		expect(outside).toHaveFocus();
	});

	it("ignores an event whose target is not a node", () => {
		render(
			<Trap>
				<button type="button">one</button>
			</Trap>,
		);
		let prevented = false;
		trapTabKey({
			key: "Tab",
			shiftKey: false,
			defaultPrevented: false,
			currentTarget: screen.getByRole("dialog"),
			target: window,
			preventDefault: () => {
				prevented = true;
			},
		} as unknown as KeyboardEvent<HTMLElement>);
		expect(prevented).toBe(false);
	});
});
