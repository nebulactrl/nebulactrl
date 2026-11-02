import { describe, expect, test } from "bun:test";
import { runPresentation } from "./model";

describe("runPresentation with a status the dashboard does not know", () => {
	test("shows the raw status text with an unknown icon instead of hiding the run", () => {
		expect(runPresentation("cancelled")).toEqual({
			kind: "unknown",
			label: "cancelled",
		});
	});

	test("the three known statuses never fall through to unknown", () => {
		expect(runPresentation("succeeded").kind).toBe("healthy");
		expect(runPresentation("failed").kind).toBe("failed");
		expect(runPresentation("running").kind).toBe("building");
	});
});
