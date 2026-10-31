import { describe, expect, test } from "bun:test";
import {
	OTHER_LAYER_SELECTOR,
	type PressTarget,
	shouldDismissOnOutsidePress,
} from "./sheet-dismiss";

/** A target whose ancestors match the given selectors, whole or by part. */
function target(
	matches: string[] = [],
	options: { isConnected?: boolean } = {},
): PressTarget {
	return {
		isConnected: options.isConnected ?? true,
		closest(selector) {
			const wanted = selector.split(",");
			return matches.some((match) => wanted.includes(match)) ? {} : null;
		},
	};
}

const ROW = "[data-cluster]";

describe("shouldDismissOnOutsidePress", () => {
	test("a plain click on the page closes the sheet", () => {
		expect(shouldDismissOnOutsidePress({ target: target(), button: 0 })).toBe(
			true,
		);
	});

	test("only the primary mouse button dismisses", () => {
		for (const button of [1, 2]) {
			expect(shouldDismissOnOutsidePress({ target: target(), button })).toBe(
				false,
			);
		}
	});

	test("a single touch dismisses, a pinch does not", () => {
		expect(
			shouldDismissOnOutsidePress({ target: target(), touchCount: 1 }),
		).toBe(true);
		expect(
			shouldDismissOnOutsidePress({ target: target(), touchCount: 2 }),
		).toBe(false);
		expect(
			shouldDismissOnOutsidePress({ target: target(), touchCount: 0 }),
		).toBe(false);
	});

	test("a press that is not on an element does not dismiss", () => {
		expect(shouldDismissOnOutsidePress({ target: null, button: 0 })).toBe(
			false,
		);
	});

	test("a press inside another layer never dismisses", () => {
		// One row per kind of layer the page stacks over a sheet.
		const layers = OTHER_LAYER_SELECTOR.split(",");
		expect(layers).toContain('[role="dialog"]');
		expect(layers).toContain("[data-toast]");
		expect(layers).toContain('[data-slot="select-content"]');
		expect(layers).toContain('[data-slot="dialog-overlay"]');
		for (const layer of layers) {
			expect(
				shouldDismissOnOutsidePress({ target: target([layer]), button: 0 }),
			).toBe(false);
		}
	});

	test("a row that retargets the sheet keeps it open for its own handler", () => {
		expect(
			shouldDismissOnOutsidePress({ target: target([ROW]), button: 0 }, ROW),
		).toBe(false);
	});

	test("without a retarget selector the same row dismisses", () => {
		expect(
			shouldDismissOnOutsidePress({ target: target([ROW]), button: 0 }),
		).toBe(true);
	});

	test("a target that left the document is not judged outside", () => {
		expect(
			shouldDismissOnOutsidePress({
				target: target([], { isConnected: false }),
				button: 0,
			}),
		).toBe(false);
	});
});
