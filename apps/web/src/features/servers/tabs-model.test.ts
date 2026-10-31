import { describe, expect, test } from "bun:test";
import {
	legacyServerRedirect,
	type ServerTarget,
	serverTabOf,
} from "./tabs-model";

const redirectOf = (hash: string, query = "") =>
	legacyServerRedirect(hash, new URLSearchParams(query));

describe("legacyServerRedirect", () => {
	const cases: [string, ServerTarget][] = [
		["overview", { tab: "overview" }],
		["host", { tab: "overview" }],
		["charts", { tab: "overview" }],
		["services", { tab: "overview" }],
		["traffic", { tab: "traffic" }],
		["proxy", { tab: "proxy" }],
		["security", { tab: "security" }],
		["updates", { tab: "updates" }],
		["settings", { tab: "settings", section: "general" }],
		["mesh", { tab: "settings", section: "mesh" }],
	];

	test.each(cases)("#%s opens its tab", (id, target) => {
		const expected = { ...target, drain: false };
		expect(redirectOf(`#${id}`)).toEqual(expected);
		expect(redirectOf(id)).toEqual(expected);
		expect(redirectOf("", `tab=${id}`)).toEqual(expected);
	});

	test("no hash and no ?tab= is not an old address", () => {
		expect(redirectOf("")).toBeNull();
		expect(redirectOf("", "drain=1")).toBeNull();
	});

	test("an anchor the page never had is not an old address", () => {
		expect(redirectOf("#nope")).toBeNull();
		expect(redirectOf("", "tab=nope")).toBeNull();
		expect(redirectOf("#toString")).toBeNull();
	});

	test("the hash wins over ?tab=", () => {
		expect(redirectOf("#proxy", "tab=security")?.tab).toBe("proxy");
	});

	test("an empty hash falls back to ?tab=", () => {
		expect(redirectOf("#", "tab=updates")?.tab).toBe("updates");
	});

	test("drain travels along", () => {
		expect(redirectOf("#proxy", "drain=1")?.drain).toBe(true);
		expect(redirectOf("", "tab=settings&drain=true")?.drain).toBe(true);
		expect(redirectOf("#proxy", "drain=0")?.drain).toBe(false);
	});
});

describe("serverTabOf", () => {
	const id = "srv-1";

	test("each tab has its own path", () => {
		expect(serverTabOf("/servers/srv-1", id)).toBe("overview");
		expect(serverTabOf("/servers/srv-1/", id)).toBe("overview");
		expect(serverTabOf("/servers/srv-1/traffic", id)).toBe("traffic");
		expect(serverTabOf("/servers/srv-1/proxy", id)).toBe("proxy");
		expect(serverTabOf("/servers/srv-1/security", id)).toBe("security");
		expect(serverTabOf("/servers/srv-1/updates", id)).toBe("updates");
	});

	test("every settings page is the Settings tab", () => {
		expect(serverTabOf("/servers/srv-1/settings", id)).toBe("settings");
		expect(serverTabOf("/servers/srv-1/settings/mesh", id)).toBe("settings");
	});

	test("anything else is the Overview", () => {
		expect(serverTabOf("/servers/srv-1/overview", id)).toBe("overview");
		expect(serverTabOf("/servers/srv-1/nope", id)).toBe("overview");
		expect(serverTabOf("/servers", id)).toBe("overview");
		expect(serverTabOf("/servers/srv-2/proxy", id)).toBe("overview");
	});
});
