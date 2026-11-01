import { describe, expect, test } from "bun:test";
import {
	ALL_SCOPES,
	canSkipProductionApproval,
	describeScopes,
	expiresAtFor,
	mcpClientConfig,
	presetById,
	tokenKindFor,
	tokenState,
} from "./token-presets";

describe("presets", () => {
	test("full access carries every scope", () => {
		expect([...presetById("full").scopes].sort()).toEqual(
			[...ALL_SCOPES].sort(),
		);
	});

	test("MCP read-only is exactly projects:read + logs:read + servers:read", () => {
		expect([...presetById("mcp-read").scopes].sort()).toEqual([
			"logs:read",
			"projects:read",
			"servers:read",
		]);
	});

	test("MCP deploy adds deployments:write and nothing destructive", () => {
		const scopes = presetById("mcp-deploy").scopes;
		expect(scopes).toContain("deployments:write");
		expect(scopes).toContain("projects:read");
		expect(scopes).not.toContain("deployments:rollback");
		expect(scopes).not.toContain("admin");
		expect(scopes).not.toContain("projects:write");
	});

	test("proxy:write is in full access and in no read-only or MCP preset", () => {
		expect(ALL_SCOPES).toContain("proxy:write");
		expect(presetById("full").scopes).toContain("proxy:write");
		for (const id of ["read-only", "mcp-read", "mcp-deploy"] as const) {
			expect(presetById(id).scopes).not.toContain("proxy:write");
		}
		expect(describeScopes(["projects:read", "servers:read"])).toBe("Read-only");
	});
});

describe("describeScopes", () => {
	test("names the recognised sets and falls back to the scopes", () => {
		expect(describeScopes([...ALL_SCOPES])).toBe("Full access");
		expect(describeScopes(["admin"])).toBe("Full access");
		expect(describeScopes(["servers:read", "projects:read"])).toBe("Read-only");
		expect(
			describeScopes(["projects:read", "servers:read", "deployments:write"]),
		).toBe("Read + deploy");
		expect(describeScopes(["projects:write"])).toBe("projects:write");
		expect(describeScopes(null)).toBe("No scopes");
	});
});

describe("tokenState and expiry", () => {
	const now = Date.parse("2026-10-26T12:00:00Z");
	test("revoked wins over everything", () => {
		expect(tokenState({ revokedAt: "2026-10-01T00:00:00Z" }, now)).toBe(
			"revoked",
		);
	});
	test("an expiry in the past is expired, in the future active", () => {
		expect(tokenState({ expiresAt: "2026-10-26T11:59:59Z" }, now)).toBe(
			"expired",
		);
		expect(tokenState({ expiresAt: "2026-11-26T00:00:00Z" }, now)).toBe(
			"active",
		);
		expect(tokenState({}, now)).toBe("active");
	});
	test("expiresAtFor adds whole days; null means never", () => {
		expect(expiresAtFor(30, new Date("2026-10-26T12:00:00Z"))).toBe(
			"2026-11-25T12:00:00.000Z",
		);
		expect(expiresAtFor(null)).toBeUndefined();
	});
});

describe("mcpClientConfig", () => {
	test("produces mcpServers with a bearer header that parses", () => {
		const parsed = JSON.parse(
			mcpClientConfig("https://cp.example.com/mcp", "nct_abc"),
		);
		expect(parsed.mcpServers.nebulactrl.url).toBe("https://cp.example.com/mcp");
		expect(parsed.mcpServers.nebulactrl.headers.Authorization).toBe(
			"Bearer nct_abc",
		);
	});
});

describe("token kind and approval rights", () => {
	test("MCP presets make MCP tokens, the others REST tokens", () => {
		for (const id of ["mcp-read", "mcp-deploy", "mcp-configure"] as const) {
			expect(tokenKindFor(id, "rest")).toBe("mcp");
		}
		for (const id of ["full", "read-only"] as const) {
			expect(tokenKindFor(id, "mcp")).toBe("rest");
		}
	});

	test("Custom follows the chosen kind", () => {
		expect(tokenKindFor("custom", "mcp")).toBe("mcp");
		expect(tokenKindFor("custom", "rest")).toBe("rest");
	});

	test("only the instance admin or a team admin may skip approvals", () => {
		expect(canSkipProductionApproval("owner", false)).toBe(true);
		expect(canSkipProductionApproval("admin", false)).toBe(true);
		expect(canSkipProductionApproval("developer", true)).toBe(true);
		expect(canSkipProductionApproval("developer", false)).toBe(false);
		expect(canSkipProductionApproval(undefined, false)).toBe(false);
	});
});
