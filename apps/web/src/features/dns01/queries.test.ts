import { describe, expect, test } from "bun:test";
import {
	CLOUDFLARE_DNS01_STAGED_TEXT,
	certificateLine,
	type DomainCert,
	expiryLabel,
	expiryTone,
	isWildcardHost,
	resolverLabel,
} from "./queries";

describe("dns01 helpers", () => {
	test("isWildcardHost", () => {
		expect(isWildcardHost("*.example.com")).toBe(true);
		expect(isWildcardHost("*.com")).toBe(false);
		expect(isWildcardHost("app.example.com")).toBe(false);
	});
	test("expiry words and tones", () => {
		expect(expiryTone(60)).toBe("ok");
		expect(expiryTone(20)).toBe("warn");
		expect(expiryTone(3)).toBe("bad");
		expect(expiryLabel(1)).toBe("1 day left");
		expect(expiryLabel(-2)).toBe("expired 2 days ago");
	});
});

describe("the certificate line of a domain", () => {
	const cert = (overrides: Partial<DomainCert> = {}): DomainCert => ({
		source: "acme",
		status: "unknown",
		message: "",
		resolver: "",
		wildcard: "",
		issuer: "",
		notAfter: null,
		daysToExpiry: null,
		automatic: false,
		staged: false,
		fallback: "",
		issuedVia: "",
		...overrides,
	});
	const automatic = (overrides: Partial<DomainCert> = {}) =>
		cert({
			source: "dns01",
			resolver: "cloudflare",
			automatic: true,
			...overrides,
		});

	test("automatic DNS-01 says it goes through Cloudflare, with the observed status", () => {
		expect(certificateLine(automatic({ status: "active" }))).toEqual({
			badge: { tone: "live", kind: "healthy", label: "Issued" },
			source: "Certificate via Cloudflare DNS (DNS-01)",
			reason: "",
			automatic: true,
		});
		expect(certificateLine(automatic({ status: "pending" })).badge).toEqual({
			tone: "off",
			kind: "pending",
			label: "Waiting for certificate",
		});
		const failed = certificateLine(automatic({ status: "error" }));
		expect(failed.badge).toEqual({
			tone: "failed",
			kind: "failed",
			label: "Problem",
		});
		expect(failed.source).toBe("Certificate via Cloudflare DNS (DNS-01)");
	});

	test("an automatic name says DNS-01 only when DNS-01 holds or will issue the certificate", () => {
		for (const issuedVia of ["dns-01", ""] as const) {
			for (const status of ["active", "pending", "error"] as const) {
				const line = certificateLine(automatic({ status, issuedVia }));
				expect(line.source).toBe("Certificate via Cloudflare DNS (DNS-01)");
				expect(line.reason).toBe("");
			}
		}
	});

	test("an existing HTTP-01 certificate of an automatic name renews over HTTP-01", () => {
		for (const status of ["active", "pending", "error", "unknown"] as const) {
			const line = certificateLine(automatic({ status, issuedVia: "http-01" }));
			expect(line.source).toBe("Certificate via HTTP-01, renews over HTTP-01");
			expect(line.source).not.toContain("Cloudflare");
			expect(line.reason).toBe(
				"New names in this zone use DNS-01 through Cloudflare.",
			);
			expect(line.automatic).toBe(true);
		}
		// The status badge still follows the observed certificate.
		expect(
			certificateLine(automatic({ status: "active", issuedVia: "http-01" }))
				.badge.label,
		).toBe("Issued");
	});

	test("staged automatic DNS-01 keeps the current certificate until the proxy deploy", () => {
		const line = certificateLine(automatic({ staged: true }));
		expect(line.source).toBe(
			"Certificate via Cloudflare DNS (DNS-01) after the next proxy deploy. The current certificate keeps serving until then.",
		);
		expect(line.source).toBe(CLOUDFLARE_DNS01_STAGED_TEXT);
		expect(line.badge.label).toBe("Staged");
		// Staged wins over an existing HTTP-01 certificate.
		expect(
			certificateLine(automatic({ staged: true, issuedVia: "http-01" })).source,
		).toBe(CLOUDFLARE_DNS01_STAGED_TEXT);
		expect(line.automatic).toBe(true);
	});

	test("a name in a connected zone on HTTP-01 gives the reason beside the HTTP-01 text", () => {
		const line = certificateLine(
			cert({ fallback: "The token cannot edit DNS records in example.com." }),
		);
		expect(line.source).toBe(
			"Issued by Let's Encrypt (HTTP-01) on the first request",
		);
		expect(line.reason).toBe(
			"The token cannot edit DNS records in example.com.",
		);
		expect(line.automatic).toBe(false);
	});

	test("HTTP-01 and manual DNS-01 keep their text and show no reason", () => {
		expect(certificateLine(cert())).toEqual({
			badge: { tone: "off", kind: "unknown", label: "Not observed" },
			source: "Issued by Let's Encrypt (HTTP-01) on the first request",
			reason: "",
			automatic: false,
		});
		const manual = certificateLine(
			cert({ source: "dns01", resolver: "route53", status: "active" }),
		);
		expect(manual.source).toBe("Issued through DNS-01");
		expect(manual.automatic).toBe(false);
		expect(
			certificateLine(
				cert({
					source: "wildcard",
					wildcard: "*.example.com",
					status: "active",
				}),
			).source,
		).toBe("Covered by a wildcard certificate (*.example.com)");
		expect(certificateLine(cert({ source: "custom" })).source).toBe(
			"Uploaded certificate",
		);
		expect(certificateLine(cert({ source: "none" })).source).toBe(
			"No TLS: plain HTTP only",
		);
	});

	test("the automatic resolver is named after the connected account", () => {
		expect(resolverLabel({ providerName: "Cloudflare", automatic: true })).toBe(
			"Cloudflare (automatic, from the connected account)",
		);
		expect(resolverLabel({ providerName: "Route 53", automatic: false })).toBe(
			"Route 53",
		);
	});
});
