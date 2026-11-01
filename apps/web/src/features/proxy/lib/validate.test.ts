import { describe, expect, test } from "bun:test";
import {
	certCovers,
	normalizeHostname,
	validateCorsOrigin,
	validateHeaderName,
	validateHostname,
	validateListenPort,
	validatePathPrefix,
	validateRedirectRegex,
	validateUpstreamAddress,
	validateUsername,
} from "./validate";

describe("validateHostname", () => {
	test("accepts hostnames and rejects schemes, paths and spaces", () => {
		expect(validateHostname("app.example.com")).toBeNull();
		expect(validateHostname("a")).toBeNull();
		expect(validateHostname("https://app.example.com")).not.toBeNull();
		expect(validateHostname("app.example.com/x")).not.toBeNull();
		expect(validateHostname("bad host")).not.toBeNull();
		expect(validateHostname("-lead.example.com")).not.toBeNull();
		expect(validateHostname("")).not.toBeNull();
	});
	test("normalises case and a trailing dot", () => {
		expect(normalizeHostname("  App.Example.COM. ")).toBe("app.example.com");
	});
});

describe("validatePathPrefix (mirrors ValidPathPrefix)", () => {
	test("empty means the whole host", () => {
		expect(validatePathPrefix("")).toBeNull();
	});
	test.each(["/api", "/api/v1", "/a.b_c~d-e"])("accepts %s", (prefix) => {
		expect(validatePathPrefix(prefix)).toBeNull();
	});
	test.each([
		"/",
		"/api/",
		"api",
		"//api",
		"/a//b",
		"/a/../b",
		"/a/..",
		"/a b",
		"/a?x",
	])("rejects %s", (prefix) => {
		expect(validatePathPrefix(prefix)).not.toBeNull();
	});
	test("caps the length at 200", () => {
		expect(validatePathPrefix(`/${"a".repeat(199)}`)).toBeNull();
		expect(validatePathPrefix(`/${"a".repeat(200)}`)).not.toBeNull();
	});
});

describe("validateHeaderName", () => {
	test("request headers the proxy owns are refused", () => {
		expect(validateHeaderName("X-Custom", true)).toBeNull();
		expect(validateHeaderName("Host", true)).not.toBeNull();
		expect(validateHeaderName("x-forwarded-for", true)).not.toBeNull();
		expect(validateHeaderName("constructor", true)).toBeNull();
	});
	test("Set-Cookie cannot be added on responses either", () => {
		expect(validateHeaderName("Set-Cookie", false)).not.toBeNull();
		expect(validateHeaderName("Cache-Control", false)).toBeNull();
	});
	test("names are tokens", () => {
		expect(validateHeaderName("Bad Name", false)).not.toBeNull();
		expect(validateHeaderName("", false)).not.toBeNull();
	});
});

describe("validateRedirectRegex (RE2 syntax)", () => {
	test("accepts plain and anchored regexes", () => {
		expect(validateRedirectRegex("^/old/(.*)$")).toBeNull();
		expect(validateRedirectRegex("^https?://example\\.com/(.*)")).toBeNull();
		expect(validateRedirectRegex("(?i)^/Docs")).toBeNull();
		expect(validateRedirectRegex("^/(?P<rest>.*)$")).toBeNull();
	});
	test("rejects what RE2 cannot compile", () => {
		expect(validateRedirectRegex("(?=look)ahead")).not.toBeNull();
		expect(validateRedirectRegex("(a)\\1")).not.toBeNull();
		expect(validateRedirectRegex("(unclosed")).not.toBeNull();
		expect(validateRedirectRegex("")).not.toBeNull();
		expect(validateRedirectRegex("a".repeat(513))).not.toBeNull();
	});
});

describe("validateListenPort", () => {
	test("1024-65535 minus the proxy's own ports", () => {
		expect(validateListenPort(25565)).toBeNull();
		expect(validateListenPort(1024)).toBeNull();
		expect(validateListenPort(1023)).not.toBeNull();
		expect(validateListenPort(65536)).not.toBeNull();
		expect(validateListenPort(8099)).not.toBeNull();
		expect(validateListenPort(9000)).not.toBeNull();
		expect(validateListenPort(Number.NaN)).not.toBeNull();
		expect(validateListenPort(2000.5)).not.toBeNull();
	});
});

describe("validateUpstreamAddress", () => {
	test("host:port with names, IPv4 and bracketed IPv6", () => {
		expect(validateUpstreamAddress("192.168.1.20:8123")).toBeNull();
		expect(validateUpstreamAddress("nas.tail1234.ts.net:443")).toBeNull();
		expect(validateUpstreamAddress("[fd7a:115c::1]:80")).toBeNull();
		expect(validateUpstreamAddress("host.docker.internal:3000")).toBeNull();
	});
	test("rejects missing or invalid ports and hosts", () => {
		expect(validateUpstreamAddress("")).not.toBeNull();
		expect(validateUpstreamAddress("192.168.1.20")).not.toBeNull();
		expect(validateUpstreamAddress("192.168.1.20:0")).not.toBeNull();
		expect(validateUpstreamAddress("192.168.1.20:70000")).not.toBeNull();
		expect(validateUpstreamAddress("fd7a::1:80")).not.toBeNull();
		expect(validateUpstreamAddress("http://nas:80")).not.toBeNull();
	});
});

describe("other validators", () => {
	test("CORS origins", () => {
		expect(validateCorsOrigin("*")).toBeNull();
		expect(validateCorsOrigin("https://app.example.com")).toBeNull();
		expect(validateCorsOrigin("http://localhost:5173")).toBeNull();
		expect(validateCorsOrigin("app.example.com")).not.toBeNull();
		expect(validateCorsOrigin("https://app.example.com/")).not.toBeNull();
	});
	test("basic-auth usernames", () => {
		expect(validateUsername("alice")).toBeNull();
		expect(validateUsername("a.b_c-d")).toBeNull();
		expect(validateUsername("alice:bob")).not.toBeNull();
		expect(validateUsername("")).not.toBeNull();
	});
	test("certificate names cover exact hosts and one-label wildcards", () => {
		expect(certCovers(["app.example.com"], "APP.example.com")).toBe(true);
		expect(certCovers(["*.example.com"], "app.example.com")).toBe(true);
		expect(certCovers(["*.example.com"], "a.b.example.com")).toBe(false);
		expect(certCovers(["*.example.com"], "example.com")).toBe(false);
		expect(certCovers([], "example.com")).toBe(false);
	});
});
