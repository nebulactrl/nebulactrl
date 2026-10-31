import { describe, expect, test } from "bun:test";
import { baseDomainHint } from "./base-domain-hint";

describe("baseDomainHint", () => {
	test("names the sslip.io fallback of the IP", () => {
		expect(baseDomainHint(" 203.0.113.10 ")).toBe(
			"Empty falls back to 203.0.113.10.sslip.io.",
		);
	});

	test("says there is no fallback without an IP", () => {
		expect(baseDomainHint("")).toContain("No public IP yet");
		expect(baseDomainHint("  ")).toContain("No public IP yet");
	});
});
