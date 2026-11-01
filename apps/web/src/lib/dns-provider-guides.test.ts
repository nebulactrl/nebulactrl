import { describe, expect, test } from "bun:test";
import { dns01Guide } from "./credential-links";
import {
	curatedDnsGuide,
	DNS_GUIDE_CODES,
	legoDocsUrl,
	legoGenericGuide,
} from "./dns-provider-guides";

// The curated codes of pkg/agentprotocol (dns01.go, dns01_providers.go). Keep
// in step: TestCuratedEnvNamesMatchLego pins the Go side.
const CURATED = [
	"cloudflare",
	"route53",
	"hetzner",
	"digitalocean",
	"gandiv5",
	"ovh",
	"porkbun",
	"namecheap",
	"gcloud",
	"azuredns",
	"desec",
	"linode",
	"vultr",
	"scaleway",
	"bunny",
	"dnsimple",
	"ionos",
	"godaddy",
	"netcup",
] as const;

describe("DNS-01 provider guides", () => {
	test("every curated provider has a guide", () => {
		for (const code of CURATED) {
			const guide = dns01Guide(code);
			expect(guide.permissions.length).toBeGreaterThan(0);
			expect(guide.paste).not.toBe("");
			expect(guide.docs).toStartWith("https://");
		}
		// The ones past the first four come from this file.
		for (const code of CURATED.slice(4)) {
			expect(DNS_GUIDE_CODES as readonly string[]).toContain(code);
			expect(curatedDnsGuide(code)).not.toBeNull();
		}
		expect(DNS_GUIDE_CODES).toHaveLength(CURATED.length - 4);
	});

	test("a button has a link, or says why it has none", () => {
		for (const code of DNS_GUIDE_CODES) {
			const guide = dns01Guide(code);
			if (guide.url !== null) {
				const url = new URL(guide.url);
				expect(url.protocol).toBe("https:");
				expect(guide.cta).not.toBeNull();
			} else {
				expect(guide.whyNoLink).toBeTruthy();
			}
		}
	});

	test("no guide links a placeholder or a plain-http page", () => {
		for (const code of DNS_GUIDE_CODES) {
			const guide = dns01Guide(code);
			for (const u of [guide.url, guide.docs]) {
				if (u === null) continue;
				expect(u).not.toContain("example.com");
				expect(u).not.toContain("{");
				expect(u).not.toStartWith("http://");
			}
		}
	});

	test("OVHcloud prefills the two rights lego documents", () => {
		const guide = dns01Guide("ovh");
		expect(guide.prefilled).toBe(true);
		expect(guide.url).toBe(
			"https://api.eu.ovhcloud.com/createToken/?POST=/domain/zone/*&DELETE=/domain/zone/*",
		);
		expect(guide.permissions.map((p) => p.label)).toEqual([
			"POST /domain/zone/*",
			"DELETE /domain/zone/*",
		]);
	});

	test("provider specifics", () => {
		expect(dns01Guide("scaleway").permissions.map((p) => p.label)).toContain(
			"Permission set DomainsDNSFullAccess",
		);
		expect(dns01Guide("gcloud").permissions[0]?.label).toContain(
			"roles/dns.admin",
		);
		expect(dns01Guide("azuredns").permissions.map((p) => p.label)).toContain(
			"Role DNS Zone Contributor on the DNS zone",
		);
		expect(dns01Guide("linode").permissions[0]?.label).toBe(
			"Domains: Read/Write",
		);
		expect(dns01Guide("namecheap").permissions.map((p) => p.label)).toContain(
			"The server's public IPv4 under Whitelisted IPs",
		);
		// GoDaddy says its classic key is going away.
		expect(dns01Guide("godaddy").note).toContain("retirement");
		// DNSimple's token page sits under the account number: no link to open.
		expect(dns01Guide("dnsimple").url).toBeNull();
		expect(dns01Guide("netcup").permissions[0]?.label).toContain("Legacy API");
	});

	test("any other lego code gets the generic guide with the lego page", () => {
		const guide = dns01Guide("pdns", {}, "PowerDNS");
		expect(guide.provider).toBe("PowerDNS");
		expect(guide.url).toBeNull();
		expect(guide.docs).toBe("https://go-acme.github.io/lego/dns/pdns/");
		expect(guide).toEqual(legoGenericGuide("pdns", "PowerDNS"));
		expect(legoDocsUrl("acmedns")).toBe(
			"https://go-acme.github.io/lego/dns/acmedns/",
		);
		expect(legoDocsUrl("../x?y#z")).toBe(
			"https://go-acme.github.io/lego/dns/..%2Fx%3Fy%23z/",
		);
	});
});
