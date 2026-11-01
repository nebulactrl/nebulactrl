import { describe, expect, test } from "bun:test";
import {
	isLocalHost,
	normalisePrefix,
	showsCertificateStatus,
	stripPrefixExample,
	wwwRedirectHelp,
} from "./domain-copy";

describe("isLocalHost", () => {
	test("reserved suffixes, bare names and addresses are local", () => {
		for (const host of [
			"localhost",
			"shop.localhost",
			"nas.local",
			"app.internal",
			"intranet",
			"10.0.0.4",
			"::1",
			"*.dev.localhost",
			"Shop.LOCALHOST.",
		]) {
			expect(isLocalHost(host)).toBe(true);
		}
	});

	test("a public name is not", () => {
		for (const host of ["example.com", "www.shop.dev", "*.shop.io", ""]) {
			expect(isLocalHost(host)).toBe(false);
		}
	});
});

describe("showsCertificateStatus", () => {
	test("a local name with no issued certificate has nothing to say", () => {
		expect(showsCertificateStatus("shop.localhost", null, true)).toBe(false);
		expect(
			showsCertificateStatus("shop.localhost", { status: "unknown" }, true),
		).toBe(false);
	});

	test("a public name, an uploaded certificate and an issued one still show", () => {
		expect(showsCertificateStatus("shop.dev", null, true)).toBe(true);
		expect(showsCertificateStatus("shop.localhost", null, false)).toBe(true);
		expect(
			showsCertificateStatus("shop.localhost", { status: "active" }, true),
		).toBe(true);
	});
});

describe("wwwRedirectHelp", () => {
	test("a public name needs a record for each hostname", () => {
		const text = wwwRedirectHelp({
			on: false,
			hostname: "shop.dev",
			counterpart: "www.shop.dev",
		});
		expect(text).toContain("each needs its own DNS record");
		expect(text).toContain("Also route www.shop.dev");
	});

	test("a local name says it needs none", () => {
		const text = wwwRedirectHelp({
			on: true,
			hostname: "shop.localhost",
			counterpart: "www.shop.localhost",
		});
		expect(text).not.toContain("own DNS record");
		expect(text).toContain("need no DNS record");
		expect(text).toContain("www.shop.localhost answers with");
	});
});

describe("stripPrefixExample", () => {
	test("uses the prefix being edited", () => {
		expect(stripPrefixExample("/api", "")).toEqual({
			from: "/api/users",
			to: "/users",
		});
		expect(stripPrefixExample("docs/", "")).toEqual({
			from: "/docs/users",
			to: "/users",
		});
	});

	test("an added prefix goes on after the strip", () => {
		expect(stripPrefixExample("/api", "/v2")).toEqual({
			from: "/api/users",
			to: "/v2/users",
		});
	});

	test("has no example without a prefix", () => {
		expect(stripPrefixExample("", "/v2")).toBeNull();
		expect(stripPrefixExample("  /", "")).toBeNull();
	});
});

describe("normalisePrefix", () => {
	test("adds the leading slash and drops the trailing ones", () => {
		expect(normalisePrefix("api//")).toBe("/api");
		expect(normalisePrefix("/")).toBe("");
	});
});
