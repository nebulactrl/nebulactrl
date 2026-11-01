import { describe, expect, test } from "bun:test";
import {
	INFO_TIP_CLOSED,
	type InfoTipEvent,
	type InfoTipState,
	infoTipEventFor,
	reduceInfoTip,
} from "./info-tip-state";

const run = (events: InfoTipEvent[], from: InfoTipState = INFO_TIP_CLOSED) =>
	events.reduce(reduceInfoTip, from);

describe("reduceInfoTip", () => {
	test("hover opens it and leaving closes it", () => {
		expect(run([{ type: "hover", open: true }])).toEqual({
			open: true,
			pinned: false,
		});
		expect(
			run([
				{ type: "hover", open: true },
				{ type: "hover", open: false },
			]),
		).toEqual(INFO_TIP_CLOSED);
	});

	test("a tap opens and pins it, since touch has no hover", () => {
		expect(run([{ type: "press" }])).toEqual({ open: true, pinned: true });
	});

	test("a click on a hover-opened tip pins it instead of closing it", () => {
		const state = run([{ type: "hover", open: true }, { type: "press" }]);
		expect(state).toEqual({ open: true, pinned: true });
		expect(reduceInfoTip(state, { type: "hover", open: false })).toEqual(state);
	});

	test("the second press unpins and closes", () => {
		expect(run([{ type: "press" }, { type: "press" }])).toEqual(
			INFO_TIP_CLOSED,
		);
	});

	test("focus opens it; blur closes it even when pinned", () => {
		expect(run([{ type: "focus", open: true }])).toEqual({
			open: true,
			pinned: false,
		});
		expect(run([{ type: "press" }, { type: "focus", open: false }])).toEqual(
			INFO_TIP_CLOSED,
		);
	});

	test("Escape and an outside press close a pinned tip", () => {
		expect(run([{ type: "press" }, { type: "dismiss" }])).toEqual(
			INFO_TIP_CLOSED,
		);
	});
});

describe("infoTipEventFor", () => {
	test("maps the reasons the tip acts on", () => {
		expect(infoTipEventFor("trigger-hover", true)).toEqual({
			type: "hover",
			open: true,
		});
		expect(infoTipEventFor("trigger-focus", false)).toEqual({
			type: "focus",
			open: false,
		});
		expect(infoTipEventFor("escape-key", false)).toEqual({ type: "dismiss" });
		expect(infoTipEventFor("outside-press", false)).toEqual({
			type: "dismiss",
		});
	});

	test("leaves the press to the button's click handler", () => {
		expect(infoTipEventFor("trigger-press", false)).toBeNull();
	});
});
