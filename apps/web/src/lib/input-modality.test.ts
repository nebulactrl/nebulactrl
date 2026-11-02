import { describe, expect, test } from "bun:test";
import { lastInputWasKeyboard } from "./input-modality";

const press = (key: string) =>
	document.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true }));

describe("lastInputWasKeyboard", () => {
	test("is true after a key press and false again after a pointer press", () => {
		press("Enter");
		expect(lastInputWasKeyboard()).toBe(true);
		document.dispatchEvent(new Event("pointerdown", { bubbles: true }));
		expect(lastInputWasKeyboard()).toBe(false);
	});

	test.each(["mousedown", "touchstart"])(
		"a %s also ends keyboard use",
		(type) => {
			press("a");
			expect(lastInputWasKeyboard()).toBe(true);
			document.dispatchEvent(new Event(type, { bubbles: true }));
			expect(lastInputWasKeyboard()).toBe(false);
		},
	);

	test.each(["Shift", "Control", "Alt", "Meta"])(
		"holding %s alone starts a pointer gesture, not keyboard navigation",
		(key) => {
			document.dispatchEvent(new Event("pointerdown", { bubbles: true }));
			press(key);
			expect(lastInputWasKeyboard()).toBe(false);
		},
	);

	test("counts a key pressed on an element before any handler can stop it", () => {
		const button = document.createElement("button");
		button.addEventListener("keydown", (event) => event.stopPropagation());
		document.body.append(button);
		document.dispatchEvent(new Event("pointerdown", { bubbles: true }));
		button.dispatchEvent(
			new KeyboardEvent("keydown", { key: "Tab", bubbles: true }),
		);
		expect(lastInputWasKeyboard()).toBe(true);
	});
});
