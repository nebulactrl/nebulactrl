import { describe, expect, test } from "bun:test";
import type { Service } from "#/api/models";
import { domainScheme, domainUrl, openTargets } from "./service-urls";

const domain = (patch: Record<string, unknown> = {}) => ({
	hostname: "shop.localhost",
	pathPrefix: "",
	tlsMode: "auto" as const,
	...patch,
});

const service = (patch: Record<string, unknown> = {}) =>
	({ domains: [], tailnet: null, ...patch }) as unknown as Pick<
		Service,
		"domains" | "tailnet"
	>;

describe("domainUrl", () => {
	test("https for automatic and custom TLS", () => {
		expect(domainUrl(domain())).toBe("https://shop.localhost/");
		expect(domainUrl(domain({ tlsMode: "custom" }))).toBe(
			"https://shop.localhost/",
		);
	});

	test("http when TLS is off", () => {
		expect(domainScheme(domain({ tlsMode: "none" }))).toBe("http");
		expect(domainUrl(domain({ tlsMode: "none" }))).toBe(
			"http://shop.localhost/",
		);
	});

	test("http when the domain has no certificate", () => {
		expect(domainUrl(domain({ cert: { source: "none" } }))).toBe(
			"http://shop.localhost/",
		);
		expect(domainUrl(domain({ cert: { source: "acme" } }))).toBe(
			"https://shop.localhost/",
		);
		expect(domainUrl(domain({ cert: null }))).toBe("https://shop.localhost/");
	});

	test("keeps the path prefix", () => {
		expect(domainUrl(domain({ pathPrefix: "/api" }))).toBe(
			"https://shop.localhost/api",
		);
	});

	test("an IDN host becomes punycode, a punycode host stays", () => {
		expect(domainUrl(domain({ hostname: "bücher.example" }))).toBe(
			"https://xn--bcher-kva.example/",
		);
		expect(domainUrl(domain({ hostname: "xn--bcher-kva.example" }))).toBe(
			"https://xn--bcher-kva.example/",
		);
	});

	test("a host the parser rejects comes back as typed", () => {
		expect(domainUrl(domain({ hostname: "not a host" }))).toBe(
			"https://not a host",
		);
	});
});

describe("openTargets", () => {
	test("a service with no domain and no tailnet has nothing to open", () => {
		expect(openTargets(service())).toEqual([]);
	});

	test("public domains keep their order", () => {
		const targets = openTargets(
			service({
				domains: [
					domain({ hostname: "a.dev" }),
					domain({ hostname: "b.dev", tlsMode: "none" }),
				],
			}),
		);
		expect(targets).toEqual([
			{ host: "a.dev", url: "https://a.dev/", tailnetOnly: false },
			{ host: "b.dev", url: "http://b.dev/", tailnetOnly: false },
		]);
	});

	test("the host is shown as typed while the URL is punycode", () => {
		const [target] = openTargets(
			service({ domains: [domain({ hostname: "bücher.example" })] }),
		);
		expect(target?.host).toBe("bücher.example");
		expect(target?.url).toBe("https://xn--bcher-kva.example/");
	});

	test("a wildcard domain is not offered", () => {
		expect(
			openTargets(
				service({ domains: [domain({ hostname: "*.example.com" })] }),
			),
		).toEqual([]);
	});

	test("an online tailnet node comes after the public domains, marked", () => {
		const targets = openTargets(
			service({
				domains: [domain({ hostname: "a.dev" })],
				tailnet: {
					state: "online",
					url: "https://web-prod.tail1234.ts.net",
					hidePublic: false,
				},
			}),
		);
		expect(targets.map((target) => [target.host, target.tailnetOnly])).toEqual([
			["a.dev", false],
			["web-prod.tail1234.ts.net", true],
		]);
		expect(targets[1]?.url).toBe("https://web-prod.tail1234.ts.net/");
	});

	test("a Headscale node keeps its http address", () => {
		const [target] = openTargets(
			service({
				tailnet: {
					state: "online",
					url: "http://web.hs.internal:8080",
					hidePublic: false,
				},
			}),
		);
		expect(target).toEqual({
			host: "web.hs.internal:8080",
			url: "http://web.hs.internal:8080/",
			tailnetOnly: true,
		});
	});

	test("Tailnet only drops the public domains", () => {
		const targets = openTargets(
			service({
				domains: [domain({ hostname: "a.dev" })],
				tailnet: {
					state: "online",
					url: "https://web.tail1234.ts.net",
					hidePublic: true,
				},
			}),
		);
		expect(targets.map((target) => target.host)).toEqual([
			"web.tail1234.ts.net",
		]);
	});

	test("an offline node, an empty address or a non-web address is skipped", () => {
		for (const tailnet of [
			{ state: "offline", url: "https://x.ts.net", hidePublic: false },
			{ state: "online", url: "", hidePublic: false },
			{ state: "online", url: "ftp://x.ts.net", hidePublic: false },
			{ state: "online", url: "not a url", hidePublic: false },
		]) {
			expect(openTargets(service({ tailnet }))).toEqual([]);
		}
	});
});
