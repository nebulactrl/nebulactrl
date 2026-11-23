import { describe, expect, test } from "bun:test";
import {
	BLOCK_RANGE_PHRASE,
	BLOCK_RANGES,
	blockedCounts,
	blockedCountText,
	blockedLogsQuery,
	blockedRuleLabel,
	decide,
	type FirewallRule,
	MAX_RULES,
	parseCountry,
	RECIPE_GROUPS,
	RECIPES,
	recipeFits,
	removedBlocks,
	ruleSentence,
	summary,
	type TestRequest,
	toBody,
	validateRules,
	withField,
	withOp,
} from "./model";

const req = (over: Partial<TestRequest> = {}): TestRequest => ({
	method: "GET",
	target: "/",
	ip: "",
	country: "",
	userAgent: "",
	headers: "",
	...over,
});

const rule = (over: Partial<FirewallRule>): FirewallRule => ({
	id: over.id ?? "r1",
	name: "Rule",
	action: "block",
	conditions: [],
	...over,
});

describe("decide", () => {
	const office = rule({
		id: "office",
		name: "Office",
		action: "allow",
		conditions: [{ field: "ip", op: "in", values: ["203.0.113.0/24"] }],
	});
	const admin = rule({
		id: "admin",
		name: "Admin",
		conditions: [{ field: "path", op: "prefix", values: ["/admin"] }],
	});

	test("the first matching rule decides, and allow skips the rules below it", () => {
		expect(
			decide([office, admin], req({ target: "/admin", ip: "203.0.113.9" })),
		).toMatchObject({
			kind: "allow",
			index: 0,
		});
		expect(
			decide(
				[office, admin],
				req({ target: "/admin/users", ip: "198.51.100.1" }),
			),
		).toMatchObject({
			kind: "block",
			index: 1,
		});
		expect(decide([office, admin], req({ target: "/shop" }))).toEqual({
			kind: "pass",
		});
	});

	test("a disabled rule is skipped", () => {
		expect(
			decide([{ ...admin, disabled: true }], req({ target: "/admin" })),
		).toEqual({ kind: "pass" });
	});

	test("conditions all must match; values of one condition are alternatives; not inverts", () => {
		const r = rule({
			conditions: [
				{ field: "method", op: "in", values: ["POST", "PUT"] },
				{ field: "path", op: "prefix", values: ["/api"], not: true },
			],
		});
		expect(decide([r], req({ method: "put", target: "/login" })).kind).toBe(
			"block",
		);
		expect(decide([r], req({ method: "POST", target: "/api/x" })).kind).toBe(
			"pass",
		);
		expect(decide([r], req({ method: "GET", target: "/login" })).kind).toBe(
			"pass",
		);
	});

	test("contains ignores case; regex is unanchored; query and headers by name", () => {
		const ua = rule({
			conditions: [{ field: "userAgent", op: "contains", values: ["SQLMap"] }],
		});
		expect(decide([ua], req({ userAgent: "sqlmap/1.7" })).kind).toBe("block");
		const re = rule({
			conditions: [{ field: "path", op: "regex", values: ["\\.php$"] }],
		});
		expect(decide([re], req({ target: "/x/index.php?a=1" })).kind).toBe(
			"block",
		);
		const q = rule({
			conditions: [
				{ field: "query", op: "equals", name: "debug", values: ["1"] },
			],
		});
		expect(decide([q], req({ target: "/?debug=1" })).kind).toBe("block");
		expect(decide([q], req({ target: "/?debug=0" })).kind).toBe("pass");
		const missing = rule({
			conditions: [
				{ field: "header", op: "present", name: "X-Api-Key", not: true },
			],
		});
		expect(decide([missing], req()).kind).toBe("block");
		expect(decide([missing], req({ headers: "x-api-key: abc" })).kind).toBe(
			"pass",
		);
	});

	test("an IPv4-mapped IPv6 visitor matches an IPv4 network; an unknown country never matches", () => {
		expect(decide([office], req({ ip: "::ffff:203.0.113.7" })).kind).toBe(
			"allow",
		);
		const notDE = rule({
			conditions: [{ field: "country", op: "in", values: ["DE"], not: true }],
		});
		expect(decide([notDE], req({ country: "FR" })).kind).toBe("block");
		expect(decide([notDE], req({ country: "DE" })).kind).toBe("pass");
	});
});

describe("validateRules", () => {
	test("names the rule and condition at fault, like the control plane", () => {
		const errors = validateRules([
			rule({ id: "a", name: " ", conditions: [] }),
			rule({
				id: "b",
				conditions: [
					{ field: "path", op: "prefix", values: ["admin"] },
					{ field: "ip", op: "in", values: ["10.0.0.0/33"] },
					{ field: "header", op: "equals", name: "Bad Name", values: ["x"] },
					{ field: "path", op: "regex", values: ["(?=x)"] },
					{ field: "country", op: "in", values: [] },
				],
			}),
		]);
		expect(Object.keys(errors).sort()).toEqual([
			"a.conditions",
			"a.name",
			"b.0.values",
			"b.1.values",
			"b.2.name",
			"b.3.values",
			"b.4.values",
		]);
		expect(errors["b.0.values"]).toBe("A path starts with /.");
	});

	test("a complete rule passes", () => {
		expect(
			validateRules([
				rule({
					conditions: [
						{ field: "path", op: "contains", values: ["/.env"] },
						{ field: "header", op: "present", name: "X-Debug" },
					],
				}),
			]),
		).toEqual({});
	});

	test("a Go inline flag like (?i) is accepted and matches as in the proxy", () => {
		const ci = rule({
			conditions: [{ field: "path", op: "regex", values: ["(?i)/\\.env$"] }],
		});
		expect(validateRules([ci])).toEqual({});
		expect(decide([ci], req({ target: "/.ENV" })).kind).toBe("block");
	});
});

describe("sentences and editing", () => {
	test("a rule reads as a sentence, countries by name", () => {
		expect(
			ruleSentence(
				rule({
					conditions: [
						{ field: "path", op: "prefix", values: ["/admin"] },
						{ field: "country", op: "in", not: true, values: ["DE", "FR"] },
					],
				}),
			),
		).toBe(
			"Block requests when the path starts with /admin and the country is not Germany or France",
		);
	});

	test("switching the field keeps a negation it supports and drops values that no longer fit", () => {
		const c = withField(
			{ field: "path", op: "prefix", not: true, values: ["/a"] },
			"header",
		);
		expect(c).toEqual({ field: "header", op: "equals", values: [], name: "" });
		expect(
			withOp(
				{ field: "header", op: "equals", name: "X", values: ["a"] },
				"present!",
			),
		).toEqual({
			field: "header",
			op: "present",
			not: true,
			name: "X",
			values: [],
		});
	});

	test("countries parse from a code, a name or a list pick", () => {
		expect(parseCountry("de")).toBe("DE");
		expect(parseCountry("Germany")).toBe("DE");
		expect(parseCountry("FR · France")).toBe("FR");
		expect(parseCountry("Atlantis")).toBeNull();
	});

	test("the body drops what the API does not take and the summary counts what is on", () => {
		const rules = [
			rule({
				id: "a",
				name: " Admin ",
				conditions: [
					{ field: "header", op: "present", name: "X", values: ["stale"] },
				],
			}),
			rule({
				id: "b",
				disabled: true,
				conditions: [{ field: "path", op: "prefix", values: ["/x"] }],
			}),
		];
		expect(toBody(rules)).toEqual({
			rules: [
				{
					id: "a",
					name: "Admin",
					action: "block",
					conditions: [{ field: "header", op: "present", name: "X" }],
				},
				{
					id: "b",
					name: "Rule",
					action: "block",
					disabled: true,
					conditions: [{ field: "path", op: "prefix", values: ["/x"] }],
				},
			],
		});
		expect(summary(rules)).toBe("1 of 2 rules on + standard");
		expect(summary([])).toBe("Standard protection");
	});
});

describe("recipes", () => {
	const recipe = (id: string) => {
		const found = RECIPES.find((r) => r.id === id);
		if (!found) throw new Error(`no recipe ${id}`);
		return found;
	};
	const blocks = (id: string, over: Partial<TestRequest>) =>
		decide(recipe(id).rules(), req(over)).kind === "block";
	const path = (p: string) => ({ target: p });
	const ua = (userAgent: string) => ({ userAgent });
	/** Recipes with values a person fills in (an address list, countries). */
	const FILL_IN = ["admin", "countries"];

	test("ids are unique, titles and lines are short, every group has recipes", () => {
		expect(new Set(RECIPES.map((r) => r.id)).size).toBe(RECIPES.length);
		for (const r of RECIPES) {
			expect(r.id).toMatch(/^[a-z]+(-[a-z]+)*$/);
			expect(r.description.length).toBeLessThanOrEqual(90);
			expect((r.caution ?? "").length).toBeLessThanOrEqual(70);
			expect(RECIPE_GROUPS.map((g) => g.id)).toContain(r.group);
		}
		for (const g of RECIPE_GROUPS) {
			expect(RECIPES.some((r) => r.group === g.id)).toBe(true);
		}
		// Recipes that are not for everyone say so.
		for (const id of [
			"php-files",
			"java",
			"cms",
			"exchange",
			"empty-user-agent",
			"scripts",
		]) {
			expect(recipe(id).caution).toBeTruthy();
		}
	});

	test("every recipe's rules pass the checks made before the PUT", () => {
		for (const r of RECIPES) {
			const rules = r.rules();
			expect(rules.length).toBeGreaterThan(0);
			expect(new Set(rules.map((x) => x.id)).size).toBe(rules.length);
			const errors = validateRules(rules);
			if (FILL_IN.includes(r.id)) {
				// Only the values a person still has to enter are missing.
				expect(Object.keys(errors).length).toBeGreaterThan(0);
				for (const key of Object.keys(errors)) {
					expect(key).toEndWith(".values");
				}
			} else {
				expect(errors).toEqual({});
			}
			for (const rule of rules) {
				expect(rule.action).toBe("block");
				expect(rule.name.length).toBeLessThanOrEqual(80);
				expect((rule.conditions ?? []).length).toBeLessThanOrEqual(10);
				for (const c of rule.conditions ?? []) {
					expect((c.values ?? []).length).toBeLessThanOrEqual(50);
					for (const v of c.values ?? []) {
						expect(v.length).toBeLessThanOrEqual(512);
					}
				}
			}
		}
	});

	test("all recipes together fit the rule limit, with fresh ids each time", () => {
		const all = RECIPES.filter((r) => !FILL_IN.includes(r.id)).flatMap((r) =>
			r.rules(),
		);
		expect(all.length).toBeLessThanOrEqual(MAX_RULES);
		expect(validateRules(all)).toEqual({});
		expect(recipe("tools").rules()[0]?.id).not.toBe(
			recipe("tools").rules()[0]?.id,
		);
	});

	test("a recipe is offered only while its rules fit", () => {
		const size = recipe("signatures").rules().length;
		expect(size).toBeGreaterThan(1);
		expect(recipeFits(recipe("signatures"), MAX_RULES - size)).toBe(true);
		expect(recipeFits(recipe("signatures"), MAX_RULES - size + 1)).toBe(false);
		expect(recipeFits(recipe("countries"), MAX_RULES - 1)).toBe(true);
		expect(recipeFits(recipe("countries"), MAX_RULES)).toBe(false);
	});

	test("PHP file probes spare code hosts, security.txt and extension-less paths", () => {
		for (const p of [
			"/index.php",
			"/alfa.php",
			"/1.PHP",
			"/index.php/login",
			"/.well-known/acme-challenge/about.php",
			"/wp-content/plugins/x/up.php",
		]) {
			expect(blocks("php-files", path(p))).toBe(true);
		}
		for (const p of [
			"/",
			"/.well-known/security.txt",
			"/robots.txt",
			"/org/repo/src/branch/main/index.php",
			"/blog/php-tips",
			"/phpinfo",
		]) {
			expect(blocks("php-files", path(p))).toBe(false);
		}
	});

	test("PHP framework and debug probes, including the ThinkPHP query", () => {
		for (const p of [
			"/vendor/phpunit/phpunit/src/Util/PHP/eval-stdin.php",
			"/_ignition/execute-solution",
			"/_profiler/phpinfo",
			"/app_dev.php/_profiler/phpinfo",
			"/debug/default/view",
			"/?s=/index/\\think\\app/invokefunction",
		]) {
			expect(blocks("php-framework", path(p))).toBe(true);
		}
		for (const p of [
			"/vendor/jquery/jquery.js",
			"/assets/vendor/bootstrap.css",
			"/vendors/x",
			"/?s=shoes",
		]) {
			expect(blocks("php-framework", path(p))).toBe(false);
		}
	});

	test("WordPress probes include the typo-squats and spare lookalikes", () => {
		for (const p of [
			"/wp-login.php",
			"/blog/wp-includes/wlwmanifest.xml",
			"/xmrlpc.php",
			"/wp-admin/wp-conflg.php",
			"/wp-json/oembed/1.0/embed",
			"/wp-cron.php",
			"/phpMyAdmin/index.php",
		]) {
			expect(blocks("wordpress", path(p))).toBe(true);
		}
		for (const p of ["/wordpress/", "/wp/", "/wp-sitemap.xml"]) {
			expect(blocks("wordpress", path(p))).toBe(false);
		}
	});

	test("exposed-service probes", () => {
		for (const p of [
			"/server-status",
			"/_all_dbs",
			"/containers/json",
			"/@vite/env",
			"/@fs/etc/passwd",
			"/phpinfo",
			"/phpinfo.php",
			"/info.php",
			"/test.php",
			"/_cat/indices",
		]) {
			expect(blocks("exposed-services", path(p))).toBe(true);
		}
		for (const p of [
			"/info",
			"/test",
			"/@vite/client",
			"/@id/x",
			"/server-statuses",
			"/catalog",
			"/v2/_catalog",
		]) {
			expect(blocks("exposed-services", path(p))).toBe(false);
		}
	});

	test("Java and Spring probes", () => {
		for (const p of [
			"/actuator/env",
			"/api/actuator/env",
			"/manager/html",
			"/login.action",
			"/WEB-INF/web.xml",
			"/index.jsp",
			"/geoserver/web/",
		]) {
			expect(blocks("java", path(p))).toBe(true);
		}
		for (const p of ["/solrs", "/actuators", "/console", "/actions"]) {
			expect(blocks("java", path(p))).toBe(false);
		}
	});

	test("CMS and admin-panel probes", () => {
		for (const p of [
			"/administrator/",
			"/phpmyadmin4.9/",
			"/sites/default/files",
			"/rest/V1/store/storeViews",
			"/typo3/",
			"/adminer.php",
		]) {
			expect(blocks("cms", path(p))).toBe(true);
		}
		for (const p of [
			"/administrators",
			"/components",
			"/blog/typo3",
			"/sites",
			"/downloaders",
		]) {
			expect(blocks("cms", path(p))).toBe(false);
		}
	});

	test("backups and dumps, only at the root or one folder deep", () => {
		for (const p of [
			"/backup.sql",
			"/db.sql.gz",
			"/wwwroot.zip",
			"/site.tar.gz",
			"/index.php.bak",
			"/wp-config.php.bak",
			"/backup/db.sql",
		]) {
			expect(blocks("backups", path(p))).toBe(true);
		}
		for (const p of [
			"/downloads/app-1.2.zip",
			"/archive/main.zip",
			"/org/repo/raw/branch/main/schema.sql",
			"/data.json",
		]) {
			expect(blocks("backups", path(p))).toBe(false);
		}
	});

	test("router, camera and VPN probes", () => {
		for (const p of [
			"/HNAP1",
			"/cgi-bin/luci/;stok=/locale",
			"/boaform/admin/formLogin",
			"/SDK/webLanguage",
			"/cgi-bin/.%2e/.%2e/bin/sh",
		]) {
			expect(blocks("routers", path(p))).toBe(true);
		}
		for (const p of ["/cgi", "/shell", "/remote/login"]) {
			expect(blocks("routers", path(p))).toBe(false);
		}
	});

	test("Exchange and Outlook probes", () => {
		for (const p of [
			"/autodiscover/autodiscover.xml",
			"/en/AutoDiscover/autodiscover.xml",
			"/owa/auth/logon.aspx",
			"/ecp/Current/exporttool/",
			"/EWS/Exchange.asmx",
		]) {
			expect(blocks("exchange", path(p))).toBe(true);
		}
		for (const p of ["/owasp", "/about", "/ewsx", "/rpcs"]) {
			expect(blocks("exchange", path(p))).toBe(false);
		}
	});

	test("attack signatures are found in each field they are checked in", () => {
		const lookups = [
			// biome-ignore lint/suspicious/noTemplateCurlyInString: literal Log4Shell payloads, not templates
			"${jndi:ldap://x/a}",
			// biome-ignore lint/suspicious/noTemplateCurlyInString: literal Log4Shell payloads, not templates
			"${${lower:j}ndi:ldap://x}",
			// biome-ignore lint/suspicious/noTemplateCurlyInString: literal Log4Shell payloads, not templates
			"${${::-j}${::-n}di:ldap://x}",
			// biome-ignore lint/suspicious/noTemplateCurlyInString: literal Log4Shell payloads, not templates
			"${${env:BARFOO:-j}ndi:ldap://x}",
		];
		for (const l of lookups) {
			expect(blocks("signatures", ua(`Mozilla/5.0 ${l}`))).toBe(true);
			expect(blocks("signatures", { headers: `Referer: ${l}` })).toBe(true);
			expect(blocks("signatures", { headers: `X-Forwarded-For: ${l}` })).toBe(
				true,
			);
			expect(blocks("signatures", path(`/${l}`))).toBe(true);
		}
		for (const harmless of [
			// biome-ignore lint/suspicious/noTemplateCurlyInString: literal Log4Shell payloads, not templates
			"price ${amount}",
			// biome-ignore lint/suspicious/noTemplateCurlyInString: literal Log4Shell payloads, not templates
			"${HOME}",
			"Mozilla/5.0 (X11; Linux x86_64)",
		]) {
			expect(blocks("signatures", ua(harmless))).toBe(false);
			expect(blocks("signatures", { headers: `Referer: ${harmless}` })).toBe(
				false,
			);
		}
		const shellshock = "() { :;}; /bin/bash -c id";
		expect(blocks("signatures", ua(shellshock))).toBe(true);
		expect(blocks("signatures", { headers: `Referer: ${shellshock}` })).toBe(
			true,
		);
		for (const p of [
			"/etc/passwd",
			"/proc/self/environ",
			"/bin/sh",
			"/windows/win.ini",
			"/var/log/auth.log",
		]) {
			expect(blocks("signatures", path(p))).toBe(true);
		}
		for (const p of ["/etc", "/blog/etc/passwd", "/bin", "/processes"]) {
			expect(blocks("signatures", path(p))).toBe(false);
		}
	});

	test("attack tools and fake browsers, not real ones", () => {
		for (const agent of [
			"sqlmap/1.7.2#stable",
			"Fuzz Faster U Fool v2.1.0",
			"Mozilla/4.0 (Hydra)",
			"Mozlila/5.0",
			"Mozilla/6.0 x",
			"User-Agent: foo",
			"Nmap Scripting Engine",
			"WPScan v3.8.25",
			"Nuclei - Open-source project",
		]) {
			expect(blocks("tools", ua(agent))).toBe(true);
		}
		for (const agent of [
			"Mozilla/5.0 (X11; Linux x86_64)",
			"Mozilla/4.0 (compatible; MSIE 6.0)",
			"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
			"Googlebot/2.1 (+http://www.google.com/bot.html)",
		]) {
			expect(blocks("tools", ua(agent))).toBe(false);
		}
	});

	test("AI crawlers and answer bots are told apart, link previews stay open", () => {
		expect(
			blocks(
				"ai-training",
				ua(
					"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko); compatible; GPTBot/1.4; +https://openai.com/gptbot",
				),
			),
		).toBe(true);
		expect(blocks("ai-training", ua("ClaudeBot/1.0"))).toBe(true);
		expect(blocks("ai-training", ua("Bytespider"))).toBe(true);
		expect(blocks("ai-training", ua("Claude-User/1.0"))).toBe(false);
		expect(blocks("ai-answers", ua("ChatGPT-User/1.0"))).toBe(true);
		expect(blocks("ai-answers", ua("PerplexityBot/1.0"))).toBe(true);
		expect(blocks("ai-answers", ua("ClaudeBot/1.0"))).toBe(false);
		for (const agent of [
			"facebookexternalhit/1.1",
			"Twitterbot/1.0",
			"Googlebot/2.1",
			"bingbot/2.0",
			"Slackbot-LinkExpanding 1.0",
			"Discordbot/2.0",
		]) {
			expect(blocks("ai-training", ua(agent))).toBe(false);
			expect(blocks("ai-answers", ua(agent))).toBe(false);
		}
	});

	test("SEO crawlers, not the site-audit tokens", () => {
		for (const agent of [
			"Mozilla/5.0 (compatible; AhrefsBot/7.0; +http://ahrefs.com/robot/)",
			"SemrushBot-BA",
			"MJ12bot/v1.4.8",
		]) {
			expect(blocks("seo", ua(agent))).toBe(true);
		}
		for (const agent of ["SiteAuditBot", "AhrefsSiteAudit", "Googlebot/2.1"]) {
			expect(blocks("seo", ua(agent))).toBe(false);
		}
	});

	test("script-library user agents", () => {
		for (const agent of [
			"python-requests/2.31.0",
			"Go-http-client/1.1",
			"curl/8.4.0",
			"Wget/1.21",
		]) {
			expect(blocks("scripts", ua(agent))).toBe(true);
		}
		for (const agent of ["Mozilla/5.0 (X11; Linux x86_64)", "Googlebot/2.1"]) {
			expect(blocks("scripts", ua(agent))).toBe(false);
		}
	});

	test("an empty user agent is a missing, empty or blank one", () => {
		expect(blocks("empty-user-agent", ua(""))).toBe(true);
		expect(blocks("empty-user-agent", ua("   "))).toBe(true);
		expect(blocks("empty-user-agent", ua("curl/8.4.0"))).toBe(false);
	});

	test("TRACE and CONNECT are refused, other methods are not", () => {
		expect(blocks("trace-connect", { method: "TRACE" })).toBe(true);
		expect(blocks("trace-connect", { method: "CONNECT" })).toBe(true);
		expect(blocks("trace-connect", { method: "GET" })).toBe(false);
		expect(blocks("trace-connect", { method: "OPTIONS" })).toBe(false);
	});
});

describe("blocked requests", () => {
	const blocks = {
		range: "24h" as const,
		from: "2026-11-22T12:00:00Z",
		to: "2026-11-23T12:00:00Z",
		trafficAnalytics: true,
		total: 18,
		logsQuery: "stream:requests @blocked_by:*",
		byRule: [
			{
				id: "stdsecrets",
				name: "Secret files",
				kind: "standard" as const,
				requests: 12,
			},
			{ id: "r1", name: "Office only", kind: "rule" as const, requests: 5 },
			{ id: "gone", name: "gone", kind: "removed" as const, requests: 1 },
			{ id: "idle", name: "Idle", kind: "rule" as const, requests: 0 },
		],
	};

	test("counts are by rule id, and a rule with none is absent", () => {
		const counts = blockedCounts(blocks);
		expect(counts.get("stdsecrets")).toBe(12);
		expect(counts.get("r1")).toBe(5);
		expect(counts.has("idle")).toBe(false);
		expect(blockedCounts(undefined).size).toBe(0);
		expect(blockedCounts({ ...blocks, byRule: null }).size).toBe(0);
	});

	test("removed rules are the ones gone that still blocked something", () => {
		expect(removedBlocks(blocks).map((r) => r.id)).toEqual(["gone"]);
		expect(removedBlocks(undefined)).toEqual([]);
		expect(
			removedBlocks({
				...blocks,
				byRule: [{ id: "x", name: "x", kind: "removed", requests: 0 }],
			}),
		).toEqual([]);
	});

	test("a removed rule reads as its id, any other by its name", () => {
		expect(
			blockedRuleLabel({
				id: "gone",
				name: "gone",
				kind: "removed",
				requests: 1,
			}),
		).toBe("Removed rule gone");
		expect(
			blockedRuleLabel({
				id: "r1",
				name: "Office only",
				kind: "rule",
				requests: 1,
			}),
		).toBe("Office only");
	});

	test("none prints a dash, a count its digits grouped", () => {
		expect(blockedCountText(undefined)).toBe("—");
		expect(blockedCountText(0)).toBe("—");
		expect(blockedCountText(7)).toBe("7");
		expect(blockedCountText(1204)).toBe("1,204");
	});

	test("the Logs query is the service's blocked request lines, of every rule or one", () => {
		expect(blockedLogsQuery("shop")).toBe(
			"stream:requests service:shop @blocked_by:*",
		);
		expect(blockedLogsQuery("shop", "stdsecrets")).toBe(
			"stream:requests service:shop @blocked_by:stdsecrets",
		);
		// A name with a space is quoted so it stays one term.
		expect(blockedLogsQuery("my shop")).toBe(
			'stream:requests service:"my shop" @blocked_by:*',
		);
	});

	test("every range has a phrase", () => {
		for (const range of BLOCK_RANGES) {
			expect(BLOCK_RANGE_PHRASE[range]).toMatch(/^the last /);
		}
	});
});
