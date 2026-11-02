import { describe, expect, test } from "bun:test";
import type { ApprovalView } from "#/api/gen/types.gen";
import { approvalStatus, requesterText } from "./model";

describe("approvalStatus", () => {
	const cases: [ApprovalView["status"], string, RegExp][] = [
		["pending", "Waiting for approval", /Nothing has run/],
		["approved", "Approved", /repeat the call now/],
		["declined", "Declined", /will not run/],
		["used", "Approved and used", /ran once/],
		["expired", "Expired", /ran out of time/],
		["stale", "Out of date", /service changed/],
	];
	test.each(cases)("%s reads as %s", (status, label, detail) => {
		const s = approvalStatus({ status });
		expect(s.label).toBe(label);
		expect(s.detail).toMatch(detail);
	});
});

describe("requesterText", () => {
	test("names an MCP token and the client it came through", () => {
		expect(
			requesterText({
				kind: "mcp",
				tokenName: "ci-agent",
				clientName: "Claude Code",
			}),
		).toBe("MCP token ci-agent, through Claude Code");
	});
	test("names a REST API token without a client", () => {
		expect(requesterText({ kind: "token", tokenName: "deploy" })).toBe(
			"API token deploy",
		);
	});
});
