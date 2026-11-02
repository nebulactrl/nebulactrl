import { describe, expect, test } from "bun:test";
import { fireEvent, render } from "@testing-library/react";
import { INSPECTOR_TABS, type InspectorTab, TAB_CHORD_KEY } from "./tab-ids";
import { CHORD_WINDOW_MS, useTabChords } from "./use-tab-chords";

function Harness({
	enabled = true,
	onTab,
}: {
	enabled?: boolean;
	onTab(tab: InspectorTab): void;
}) {
	useTabChords(enabled, onTab);
	return (
		<div>
			<input aria-label="name" />
			<textarea aria-label="notes" />
			<div contentEditable suppressContentEditableWarning data-testid="rich" />
			<button type="button">plain</button>
		</div>
	);
}

/** A key press whose timeStamp the test controls (the chord window reads it). */
function press(
	target: EventTarget,
	key: string,
	at: number,
	init: KeyboardEventInit = {},
): KeyboardEvent {
	const event = new KeyboardEvent("keydown", {
		key,
		bubbles: true,
		cancelable: true,
		...init,
	});
	Object.defineProperty(event, "timeStamp", { value: at });
	target.dispatchEvent(event);
	return event;
}

function setup(enabled = true) {
	const tabs: InspectorTab[] = [];
	const view = render(
		<Harness enabled={enabled} onTab={(t) => tabs.push(t)} />,
	);
	return { tabs, view };
}

describe("useTabChords", () => {
	test("g then the tab's key opens each tab", () => {
		const { tabs } = setup();
		let at = 100;
		for (const tab of INSPECTOR_TABS) {
			press(document.body, "g", at);
			press(document.body, TAB_CHORD_KEY[tab], at + 10);
			at += 100;
		}
		expect(tabs).toEqual([...INSPECTOR_TABS]);
	});

	test("the second key is claimed: its default is prevented", () => {
		setup();
		const first = press(document.body, "g", 100);
		const second = press(document.body, "o", 150);
		expect(first.defaultPrevented).toBe(false);
		expect(second.defaultPrevented).toBe(true);
	});

	test("a key that is not a tab does nothing and disarms the chord", () => {
		const { tabs } = setup();
		press(document.body, "g", 100);
		const stray = press(document.body, "x", 110);
		press(document.body, "o", 120);
		expect(stray.defaultPrevented).toBe(false);
		expect(tabs).toEqual([]);
	});

	test("o without g does nothing", () => {
		const { tabs } = setup();
		const event = press(document.body, "o", 100);
		expect(tabs).toEqual([]);
		expect(event.defaultPrevented).toBe(false);
	});

	test("g g o: a second g re-arms the chord", () => {
		const { tabs } = setup();
		press(document.body, "g", 100);
		press(document.body, "g", 200);
		press(document.body, "l", 250);
		expect(tabs).toEqual(["logs"]);
	});

	test("the second key counts the window from the g, inclusive of its last ms", () => {
		const { tabs } = setup();
		press(document.body, "g", 100);
		press(document.body, "d", 100 + CHORD_WINDOW_MS);
		expect(tabs).toEqual(["deployments"]);
	});

	test("a second key after the window has closed is ignored", () => {
		const { tabs } = setup();
		press(document.body, "g", 100);
		press(document.body, "d", 100 + CHORD_WINDOW_MS + 1);
		expect(tabs).toEqual([]);
	});

	test("capital letters work (Caps Lock)", () => {
		const { tabs } = setup();
		press(document.body, "G", 100);
		press(document.body, "S", 150);
		expect(tabs).toEqual(["settings"]);
	});

	test("Shift+G (capital G typed with Shift) does not arm the chord", () => {
		const { tabs } = setup();
		press(document.body, "G", 100, { shiftKey: true });
		press(document.body, "o", 150);
		expect(tabs).toEqual([]);
	});

	for (const [name, modifier] of [
		["Cmd", { metaKey: true }],
		["Ctrl", { ctrlKey: true }],
		["Alt", { altKey: true }],
	] as const) {
		test(`${name}+G does not arm the chord`, () => {
			const { tabs } = setup();
			press(document.body, "g", 100, modifier);
			press(document.body, "o", 150);
			expect(tabs).toEqual([]);
		});

		test(`${name}+O after g is a different shortcut, not a tab jump`, () => {
			const { tabs } = setup();
			press(document.body, "g", 100);
			const event = press(document.body, "o", 150, modifier);
			expect(tabs).toEqual([]);
			expect(event.defaultPrevented).toBe(false);
		});
	}

	test("an auto-repeated key is ignored", () => {
		const { tabs } = setup();
		press(document.body, "g", 100);
		press(document.body, "o", 150, { repeat: true });
		expect(tabs).toEqual([]);
		press(document.body, "o", 160);
		expect(tabs).toEqual(["overview"]);
	});

	test("a key another handler already handled is ignored", () => {
		const { tabs } = setup();
		press(document.body, "g", 100);
		const handled = new KeyboardEvent("keydown", {
			key: "o",
			bubbles: true,
			cancelable: true,
		});
		const stop = (event: Event) => event.preventDefault();
		document.body.addEventListener("keydown", stop, { once: true });
		Object.defineProperty(handled, "timeStamp", { value: 150 });
		document.body.dispatchEvent(handled);
		expect(tabs).toEqual([]);
	});

	test("typing in an input, textarea or editable region never triggers it", () => {
		const { tabs, view } = setup();
		const fields = [
			view.getByLabelText("name"),
			view.getByLabelText("notes"),
			view.getByTestId("rich"),
		];
		for (const field of fields) {
			press(field, "g", 100);
			press(field, "o", 150);
		}
		expect(tabs).toEqual([]);
	});

	test("a chord started in a field is not finished outside it", () => {
		const { tabs, view } = setup();
		press(view.getByLabelText("name"), "g", 100);
		press(document.body, "o", 150);
		expect(tabs).toEqual([]);
	});

	test("a button focused inside the panel does not block it", () => {
		const { tabs, view } = setup();
		const button = view.getByRole("button", { name: "plain" });
		press(button, "g", 100);
		press(button, "m", 150);
		expect(tabs).toEqual(["metrics"]);
	});

	test("an open dialog or menu keeps the keys to itself", () => {
		const { tabs } = setup();
		const dialog = document.createElement("div");
		dialog.setAttribute("role", "dialog");
		dialog.setAttribute("data-open", "");
		document.body.append(dialog);
		press(document.body, "g", 100);
		press(document.body, "o", 150);
		expect(tabs).toEqual([]);
		dialog.remove();
		press(document.body, "g", 200);
		press(document.body, "o", 250);
		expect(tabs).toEqual(["overview"]);
	});

	test("a closed dialog left in the DOM does not block it", () => {
		const { tabs } = setup();
		const dialog = document.createElement("div");
		dialog.setAttribute("role", "dialog");
		document.body.append(dialog);
		press(document.body, "g", 100);
		press(document.body, "v", 150);
		expect(tabs).toEqual(["variables"]);
	});

	test("while disabled nothing listens, and enabling it starts listening", () => {
		const tabs: InspectorTab[] = [];
		const onTab = (t: InspectorTab) => tabs.push(t);
		const view = render(<Harness enabled={false} onTab={onTab} />);
		press(document.body, "g", 100);
		press(document.body, "o", 150);
		expect(tabs).toEqual([]);
		view.rerender(<Harness enabled onTab={onTab} />);
		press(document.body, "g", 200);
		press(document.body, "o", 250);
		expect(tabs).toEqual(["overview"]);
	});

	test("unmounting removes the listener", () => {
		const { tabs, view } = setup();
		view.unmount();
		press(document.body, "g", 100);
		press(document.body, "o", 150);
		expect(tabs).toEqual([]);
	});

	test("a new callback is used without losing an armed chord", () => {
		const first: InspectorTab[] = [];
		const second: InspectorTab[] = [];
		const view = render(<Harness onTab={(t) => first.push(t)} />);
		press(document.body, "g", 100);
		view.rerender(<Harness onTab={(t) => second.push(t)} />);
		press(document.body, "a", 150);
		expect(first).toEqual([]);
		expect(second).toEqual(["analytics"]);
	});

	test("fireEvent key presses reach it too (real DOM events)", () => {
		const { tabs } = setup();
		fireEvent.keyDown(document.body, { key: "g" });
		fireEvent.keyDown(document.body, { key: "b" });
		expect(tabs).toEqual(["backups"]);
	});
});
