import { describe, expect, test } from "bun:test";
import { actorText, STATUS_KIND } from "./status";

describe("actorText", () => {
	test("a person is just their name", () => {
		expect(actorText("user", "alice")).toBe("alice");
	});

	test("a token or an MCP client names its kind", () => {
		expect(actorText("token", "gate")).toBe("token · gate");
		expect(actorText("mcp", "claude")).toBe("mcp · claude");
	});
});

describe("STATUS_KIND", () => {
	test("in-flight statuses use a working icon", () => {
		expect(STATUS_KIND.building).toBe("building");
		expect(STATUS_KIND.verifying).toBe("verifying");
	});
});
