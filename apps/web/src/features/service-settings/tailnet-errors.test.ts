import { describe, expect, test } from "bun:test";
import { ApiError } from "#/api/client-config";
import { tailnetErrorCopy, tailnetErrorMessage } from "./tailnet-errors";

describe("tailnet error copy", () => {
	test("every tailnet code has a sentence", () => {
		for (const code of [
			"tailnet_not_configured",
			"tailnet_tag_not_allowed",
			"tailnet_https_disabled",
			"tailnet_funnel_unsupported",
			"tailnet_tls_unsupported",
			"agent_too_old",
		]) {
			expect(tailnetErrorCopy(code)?.length).toBeGreaterThan(20);
		}
		expect(tailnetErrorCopy("tailnet_not_configured")).toContain(
			"Team settings → Integrations",
		);
	});

	test("other codes are left to the server's message", () => {
		expect(tailnetErrorCopy("generation_conflict")).toBeUndefined();
		expect(tailnetErrorCopy(undefined)).toBeUndefined();
		expect(tailnetErrorMessage(new Error("boom"))).toBeUndefined();
	});

	test("reads the code off an API error", () => {
		expect(
			tailnetErrorMessage(new ApiError(422, "tailnet_funnel_unsupported", "x")),
		).toContain("Funnel needs Tailscale");
	});
});
