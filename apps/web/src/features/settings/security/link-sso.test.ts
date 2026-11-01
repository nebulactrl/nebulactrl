import { describe, expect, test } from "bun:test";
import { linkLabel, unlinkedProviders } from "./link-sso";

const okta = { id: "p1", name: "Okta" };
const github = { id: "p2", name: "GitHub" };

describe("unlinkedProviders", () => {
	test("offers every provider when nothing is linked", () => {
		expect(unlinkedProviders([okta, github], [])).toEqual([okta, github]);
	});
	test("leaves out the providers already linked, by id", () => {
		expect(unlinkedProviders([okta, github], [{ providerId: "p1" }])).toEqual([
			github,
		]);
		expect(
			unlinkedProviders([okta], [{ providerId: "p1" }, { providerId: "p9" }]),
		).toEqual([]);
	});
});

describe("linkLabel", () => {
	test("one provider reads as the plain action, several name the provider", () => {
		expect(linkLabel(okta, 1)).toBe("Link your SSO account");
		expect(linkLabel(okta, 2)).toBe("Link Okta");
	});
});
