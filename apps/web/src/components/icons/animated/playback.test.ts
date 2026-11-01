import { describe, expect, test } from "bun:test";
import {
	iconVariant,
	type PlaybackContext,
	type PlaybackTrigger,
	shouldPlay,
} from "./playback";

const POINTER: PlaybackContext = {
	reducedMotion: false,
	keyboard: false,
	hoverCapable: true,
};

describe("shouldPlay", () => {
	test("reduced motion silences every trigger", () => {
		for (const trigger of ["hover", "state", "once"] as const) {
			expect(shouldPlay(trigger, { ...POINTER, reducedMotion: true })).toBe(
				false,
			);
		}
	});

	test("hover needs a fine pointer with true hover", () => {
		expect(shouldPlay("hover", POINTER)).toBe(true);
		expect(shouldPlay("hover", { ...POINTER, hoverCapable: false })).toBe(
			false,
		);
	});

	test("a one-shot is skipped when the keyboard started the action", () => {
		expect(shouldPlay("once", POINTER)).toBe(true);
		expect(shouldPlay("once", { ...POINTER, keyboard: true })).toBe(false);
	});

	test("a progress cue shows however the work was started", () => {
		const touchKeyboard = { ...POINTER, keyboard: true, hoverCapable: false };
		expect(shouldPlay("state", touchKeyboard)).toBe(true);
	});

	test("every trigger plays for a pointer user with motion allowed", () => {
		for (const trigger of ["hover", "state", "once"] as PlaybackTrigger[]) {
			expect(shouldPlay(trigger, POINTER)).toBe(true);
		}
	});
});

describe("iconVariant", () => {
	test("maps the playing flag to a variant label", () => {
		expect(iconVariant(true)).toBe("animate");
		expect(iconVariant(false)).toBe("normal");
	});
});
