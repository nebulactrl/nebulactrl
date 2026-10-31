import { describe, expect, test } from "bun:test";
import { splitDeltaGlyph, statDelta, statValueText } from "./stat-math";

describe("statValueText", () => {
	test("numbers get thousands separators", () => {
		expect(statValueText(12480)).toBe("12,480");
		expect(statValueText(0)).toBe("0");
	});
	test("strings show as given", () => {
		expect(statValueText("184 ms")).toBe("184 ms");
	});
});

describe("statDelta", () => {
	test("up is good by default", () => {
		expect(statDelta(8.1, "up", "vs last week")).toEqual({
			text: "▲ 8.1%",
			tone: "good",
			spoken: "Up 8.1% vs last week",
		});
	});
	test("good=down flips the tone, not the arrow", () => {
		const latencyUp = statDelta(9.6, "down");
		expect(latencyUp.text).toBe("▲ 9.6%");
		expect(latencyUp.tone).toBe("bad");
		const errorsDown = statDelta(-31.2, "down");
		expect(errorsDown.text).toBe("▼ 31.2%");
		expect(errorsDown.tone).toBe("good");
		expect(errorsDown.spoken).toBe("Down 31.2%");
	});
	test("zero is neutral with no arrow", () => {
		expect(statDelta(0, "up", "no change")).toEqual({
			text: "0%",
			tone: "neutral",
			spoken: "No change no change",
		});
		expect(statDelta(0.01).tone).toBe("neutral");
	});
	test("non-finite is neutral", () => {
		expect(statDelta(Number.NaN).tone).toBe("neutral");
	});
});

describe("splitDeltaGlyph", () => {
	test("the arrow becomes a direction, the words stay", () => {
		expect(splitDeltaGlyph("▲ 8.1%")).toEqual({
			direction: "up",
			text: "8.1%",
		});
		expect(splitDeltaGlyph("▼ 3%")).toEqual({ direction: "down", text: "3%" });
		expect(splitDeltaGlyph("● 0%")).toEqual({ direction: "flat", text: "0%" });
	});
	test("text with no glyph has no direction", () => {
		expect(splitDeltaGlyph("0%")).toEqual({ direction: null, text: "0%" });
		expect(splitDeltaGlyph("new")).toEqual({ direction: null, text: "new" });
	});
});
