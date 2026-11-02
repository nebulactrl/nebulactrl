import { describe, expect, test } from "bun:test";
import type { HardeningFix } from "#/api/gen/types.gen";
import { safeFixesNote } from "./security-model";

const onRequest = (id: string, state: HardeningFix["state"]): HardeningFix => ({
	id,
	state,
	default: false,
	title: id,
	why: "",
	risk: "",
	lynisTests: [],
	manual: [],
	rebootRequired: false,
});

describe("safeFixesNote", () => {
	test("several On request fixes are counted and sent to their own rows", () => {
		const note = safeFixesNote([
			onRequest("a", "pending"),
			onRequest("b", "unknown"),
			onRequest("c", "applied"),
		]);
		expect(note).toContain(
			"2 fixes are marked On request: the batch leaves them out, so apply them from their own rows.",
		);
		expect(note).toContain(
			"Every recommended fix is applied or not needed here.",
		);
	});
});
