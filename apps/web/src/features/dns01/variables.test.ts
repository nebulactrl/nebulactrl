import { describe, expect, test } from "bun:test";
import type { DnsProviderSpec } from "./queries";
import {
	credentialProblem,
	isSecretVariable,
	rowsToValues,
	unknownVariables,
} from "./variables";

const field = (env: string, required = false, secret = true) => ({
	env,
	label: env,
	secret,
	required,
});

const cloudflare: DnsProviderSpec = {
	code: "cloudflare",
	name: "Cloudflare",
	docs: "https://go-acme.github.io/lego/dns/cloudflare/",
	fields: [field("CF_DNS_API_TOKEN", true), field("CF_ZONE_API_TOKEN")],
};

const ovh: DnsProviderSpec = {
	code: "ovh",
	name: "OVHcloud",
	docs: "https://go-acme.github.io/lego/dns/ovh/",
	oneOf: [
		["OVH_APPLICATION_KEY", "OVH_APPLICATION_SECRET", "OVH_CONSUMER_KEY"],
		["OVH_CLIENT_ID", "OVH_CLIENT_SECRET"],
		["OVH_ACCESS_TOKEN"],
	],
	fields: [
		field("OVH_ENDPOINT", false, false),
		field("OVH_APPLICATION_KEY"),
		field("OVH_APPLICATION_SECRET"),
		field("OVH_CONSUMER_KEY"),
		field("OVH_CLIENT_ID", false, false),
		field("OVH_CLIENT_SECRET"),
		field("OVH_ACCESS_TOKEN"),
	],
};

const pdns: DnsProviderSpec = {
	code: "pdns",
	name: "PowerDNS",
	docs: "https://go-acme.github.io/lego/dns/pdns/",
	generic: true,
	fields: [field("PDNS_API_KEY"), field("PDNS_API_URL", false, false)],
};

describe("rowsToValues", () => {
	test("trims names, drops blanks, the last row of a name wins", () => {
		expect(
			rowsToValues([
				{ id: "1", name: " PDNS_API_KEY ", value: "a" },
				{ id: "2", name: "", value: "x" },
				{ id: "3", name: "PDNS_API_URL", value: "   " },
				{ id: "4", name: "PDNS_API_KEY", value: "b" },
			]),
		).toEqual({ PDNS_API_KEY: "b" });
	});
});

describe("unknownVariables", () => {
	test("names outside the catalog", () => {
		expect(
			unknownVariables(pdns, ["PDNS_API_KEY", "TRAEFIK_API", "PATH"]),
		).toEqual(["TRAEFIK_API", "PATH"]);
	});
});

describe("credentialProblem", () => {
	test("a required variable", () => {
		expect(credentialProblem(cloudflare, {})).toContain("CF_DNS_API_TOKEN");
		expect(credentialProblem(cloudflare, { CF_DNS_API_TOKEN: "t" })).toBeNull();
		expect(
			credentialProblem(cloudflare, { CF_DNS_API_TOKEN: "  " }),
		).not.toBeNull();
	});
	test("one complete authentication method", () => {
		expect(credentialProblem(ovh, { OVH_ENDPOINT: "ovh-eu" })).toContain(
			"OVH_APPLICATION_KEY + OVH_APPLICATION_SECRET + OVH_CONSUMER_KEY or OVH_CLIENT_ID + OVH_CLIENT_SECRET or OVH_ACCESS_TOKEN",
		);
		expect(
			credentialProblem(ovh, {
				OVH_APPLICATION_KEY: "a",
				OVH_APPLICATION_SECRET: "b",
			}),
		).not.toBeNull();
		expect(
			credentialProblem(ovh, { OVH_CLIENT_ID: "i", OVH_CLIENT_SECRET: "s" }),
		).toBeNull();
		expect(credentialProblem(ovh, { OVH_ACCESS_TOKEN: "t" })).toBeNull();
	});
	test("a generic provider needs one variable", () => {
		expect(credentialProblem(pdns, {})).toContain("at least one");
		expect(credentialProblem(pdns, { PDNS_API_KEY: "k" })).toBeNull();
	});
});

describe("isSecretVariable", () => {
	test("the catalog decides; unknown names are hidden", () => {
		expect(isSecretVariable(pdns, "PDNS_API_KEY")).toBe(true);
		expect(isSecretVariable(pdns, "PDNS_API_URL")).toBe(false);
		expect(isSecretVariable(pdns, "SOMETHING_ELSE")).toBe(true);
	});
});
