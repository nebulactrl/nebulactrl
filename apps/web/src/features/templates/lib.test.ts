import { describe, expect, test } from "bun:test";
import type {
	TemplateInput,
	TemplateInstance,
	TemplatePlanCheck,
	TemplateSummary,
} from "#/api/gen/types.gen";
import type { Server } from "#/api/models";
import {
	categoryChips,
	databaseLabel,
	defaultServerId,
	filterTemplates,
	inputHint,
	inputValue,
	installedTitle,
	installFailure,
	installInputs,
	instanceOfGroup,
	instanceOfService,
	isGenerated,
	LICENSE_FILTERS,
	lacksUnrestrictedServer,
	licenseChips,
	licenseExplanation,
	licenseTone,
	matchesLicense,
	memoryHint,
	missingInputs,
	needsUnrestricted,
	publishedPortsText,
	RESTRICTED_SERVER_REASON,
	searchTemplates,
	secretsNotice,
	serverRestrictions,
	serviceCards,
	summarizeChecks,
	templateInitial,
	UNRESTRICTED_DOCS,
	updateGroups,
	updateInputFields,
	updateSummary,
	versionStep,
} from "./lib";

function summary(
	over: Partial<TemplateSummary> & { id: string },
): TemplateSummary {
	return {
		name: over.id,
		summary: "",
		category: "web",
		tags: [],
		license: "MIT",
		licenseClass: "permissive",
		services: 1,
		databases: [],
		memoryMb: 256,
		publishesPorts: false,
		revision: 1,
		...over,
	};
}

const catalog: TemplateSummary[] = [
	summary({
		id: "umami",
		name: "Umami",
		summary: "Simple, privacy-friendly web analytics.",
		tags: ["analytics", "privacy"],
		databases: ["postgres"],
		services: 2,
	}),
	summary({
		id: "ghost",
		name: "Ghost",
		summary: "Publishing platform for blogs and newsletters.",
		tags: ["blog", "cms"],
		databases: ["mysql"],
		services: 2,
	}),
	summary({
		id: "minecraft",
		name: "Minecraft",
		summary: "Java edition game server.",
		category: "games",
		tags: ["game", "analytics-free"],
		publishesPorts: true,
	}),
	summary({
		id: "n8n",
		name: "n8n",
		summary: "Workflow automation.",
		category: "automation",
		license: "Sustainable Use License",
		licenseClass: "source-available",
		tags: ["workflows"],
	}),
];

const categories = [
	{ id: "homelab", label: "Homelab" },
	{ id: "games", label: "Games" },
	{ id: "web", label: "Web" },
	{ id: "automation", label: "Automation" },
];

describe("searchTemplates", () => {
	test("an empty query keeps the catalog order", () => {
		expect(searchTemplates(catalog, "  ").map((t) => t.id)).toEqual([
			"umami",
			"ghost",
			"minecraft",
			"n8n",
		]);
	});

	test("every word must match somewhere", () => {
		expect(
			searchTemplates(catalog, "privacy analytics").map((t) => t.id),
		).toEqual(["umami"]);
		expect(searchTemplates(catalog, "blog nothing")).toEqual([]);
	});

	test("matches engines and licences", () => {
		expect(searchTemplates(catalog, "postgres").map((t) => t.id)).toEqual([
			"umami",
		]);
		expect(searchTemplates(catalog, "sustainable").map((t) => t.id)).toEqual([
			"n8n",
		]);
	});

	test("a name match outranks a tag match", () => {
		const items = [
			summary({ id: "a", name: "Zeta", tags: ["ghost"] }),
			summary({ id: "b", name: "Ghost" }),
		];
		expect(searchTemplates(items, "ghost").map((t) => t.id)).toEqual([
			"b",
			"a",
		]);
	});

	test("is case-insensitive", () => {
		expect(searchTemplates(catalog, "MINECRAFT")).toHaveLength(1);
	});
});

describe("filterTemplates and categoryChips", () => {
	test("filters by category after the search", () => {
		expect(filterTemplates(catalog, "", "games").map((t) => t.id)).toEqual([
			"minecraft",
		]);
		expect(
			filterTemplates(catalog, "analytics", "games").map((t) => t.id),
		).toEqual(["minecraft"]);
		expect(filterTemplates(catalog, "ghost", "games")).toEqual([]);
	});

	test("counts follow the search, with All first", () => {
		const chips = categoryChips(categories, catalog, "");
		expect(chips[0]).toEqual({ id: null, label: "All", count: 4 });
		expect(chips.map((chip) => [chip.id, chip.count])).toEqual([
			[null, 4],
			["homelab", 0],
			["games", 1],
			["web", 2],
			["automation", 1],
		]);
		const searched = categoryChips(categories, catalog, "analytics");
		expect(searched.map((chip) => chip.count)).toEqual([2, 0, 1, 1, 0]);
	});
});

describe("licence filter", () => {
	test("the OSI-approved filter is permissive plus copyleft", () => {
		const items = [
			summary({ id: "a", licenseClass: "permissive" }),
			summary({ id: "b", licenseClass: "copyleft" }),
			summary({ id: "c", licenseClass: "source-available" }),
			summary({ id: "d", licenseClass: "proprietary" }),
		];
		expect(filterTemplates(items, "", null, "open").map((t) => t.id)).toEqual([
			"a",
			"b",
		]);
		expect(
			filterTemplates(items, "", null, "source-available").map((t) => t.id),
		).toEqual(["c"]);
		expect(
			filterTemplates(items, "", null, "proprietary").map((t) => t.id),
		).toEqual(["d"]);
		expect(filterTemplates(items, "", null, null)).toHaveLength(4);
		expect(matchesLicense(items[2] as TemplateSummary, "open")).toBe(false);
	});

	test("combines with the category and the search", () => {
		expect(
			filterTemplates(catalog, "", "automation", "source-available").map(
				(t) => t.id,
			),
		).toEqual(["n8n"]);
		expect(filterTemplates(catalog, "ghost", null, "source-available")).toEqual(
			[],
		);
	});

	test("chip counts follow the search and the category", () => {
		const chips = licenseChips(catalog, "", null);
		expect(chips.map((c) => [c.id, c.count])).toEqual([
			["open", 3],
			["source-available", 1],
			["proprietary", 0],
		]);
		expect(licenseChips(catalog, "", "automation").map((c) => c.count)).toEqual(
			[0, 1, 0],
		);
	});

	test("the filter reads OSI-approved and says what it covers", () => {
		expect(LICENSE_FILTERS.map((f) => f.label)).toEqual([
			"OSI-approved",
			"Source-available",
			"Proprietary",
		]);
		expect(LICENSE_FILTERS.find((f) => f.id === "open")?.hint).toBe(
			"OSI-approved licence (permissive or copyleft)",
		);
		expect(
			licenseExplanation({
				license: "Sustainable Use License",
				licenseClass: "source-available",
			}),
		).toContain("this is not an OSI-approved licence");
		for (const text of LICENSE_FILTERS.flatMap((f) => [f.label, f.hint])) {
			expect(text.toLowerCase()).not.toContain("open source");
			expect(text.toLowerCase()).not.toContain("open-source");
		}
	});

	test("category counts follow the licence filter", () => {
		const chips = categoryChips(categories, catalog, "", "open");
		expect(chips[0]?.count).toBe(3);
		expect(chips.find((c) => c.id === "automation")?.count).toBe(0);
	});
});

describe("template updates", () => {
	const change = (
		status: "apply" | "applied" | "kept" | "manual",
		field: string,
	) => ({
		service: "app",
		field,
		from: "a",
		to: "b",
		secret: false,
		status,
	});

	test("updateGroups splits by status and keeps order", () => {
		const groups = updateGroups([
			change("apply", "image"),
			change("kept", "env.A"),
			change("apply", "port"),
			change("manual", "ports"),
			change("applied", "env.B"),
		]);
		expect(groups.apply.map((c) => c.field)).toEqual(["image", "port"]);
		expect(groups.kept).toHaveLength(1);
		expect(groups.manual).toHaveLength(1);
		expect(groups.applied).toHaveLength(1);
	});

	test("versionStep shows the version only when it moves", () => {
		const base = {
			installedRevision: 1,
			currentRevision: 2,
			installedVersion: "3.4.0",
			currentVersion: "3.4.1",
		};
		expect(versionStep(base)).toBe("Revision 1 → 2 · 3.4.0 → 3.4.1");
		expect(versionStep({ ...base, currentVersion: "3.4.0" })).toBe(
			"Revision 1 → 2",
		);
		expect(versionStep({ ...base, installedVersion: "" })).toBe(
			"Revision 1 → 2 · 3.4.1",
		);
		expect(versionStep({ ...base, currentVersion: "" })).toBe("Revision 1 → 2");
	});

	test("updateSummary", () => {
		expect(updateSummary({ apply: 1, kept: 0, manual: 0 })).toBe(
			"1 change will be staged. Nothing is deployed until you press Deploy.",
		);
		expect(updateSummary({ apply: 3, kept: 2, manual: 1 })).toBe(
			"3 changes will be staged, 2 kept as you changed them, 1 left for you. Nothing is deployed until you press Deploy.",
		);
	});

	test("updateInputFields turns plan inputs into form inputs", () => {
		const fields = updateInputFields([
			{
				key: "SMTP_HOST",
				label: "SMTP host",
				required: true,
				secret: false,
				generated: false,
				value: "mail.example.org",
			},
			{
				key: "TOKEN",
				label: "Token",
				required: false,
				secret: true,
				generated: true,
				description: "Made for you",
			},
		]);
		expect(fields[0]?.default).toBe("mail.example.org");
		expect(fields[0]?.required).toBe(true);
		expect(isGenerated(fields[1] as TemplateInput)).toBe(true);
		expect(inputValue(fields[1] as TemplateInput, {})).toBe("");
		expect(fields[1]?.description).toBe("Made for you");
		expect(missingInputs(fields, {})).toEqual([]);
		expect(
			missingInputs([{ ...(fields[0] as TemplateInput), default: "" }], {}),
		).toEqual(["SMTP_HOST"]);
	});
});

describe("card facts", () => {
	test("memoryHint", () => {
		expect(memoryHint(512)).toBe("512 MB");
		expect(memoryHint(2048)).toBe("2 GB");
		expect(memoryHint(1536)).toBe("1.5 GB");
	});

	test("only source-available and proprietary are flagged", () => {
		expect(licenseTone("permissive")).toBe("open");
		expect(licenseTone("copyleft")).toBe("open");
		expect(licenseTone("source-available")).toBe("flagged");
		expect(licenseTone("proprietary")).toBe("flagged");
	});

	test("initial and database labels", () => {
		expect(templateInitial("  umami")).toBe("U");
		expect(templateInitial("")).toBe("?");
		expect(databaseLabel("postgres")).toBe("PostgreSQL");
		expect(databaseLabel("postgres 17")).toBe("PostgreSQL 17");
		expect(databaseLabel("surreal 2")).toBe("surreal 2");
	});
});

function input(over: Partial<TemplateInput> & { key: string }): TemplateInput {
	return {
		label: over.key,
		default: "",
		required: false,
		secret: false,
		...over,
	};
}

// A template's generator syntax, assembled so it is not read as a JS template.
const generator = (call: string) => `$${"{{"}${call}}}`;

describe("install inputs", () => {
	const inputs = [
		input({ key: "EMAIL", required: true }),
		input({ key: "PASSWORD", secret: true, default: generator("secret(20)") }),
		input({ key: "TITLE", default: "My site" }),
		input({ key: "TZ", options: ["UTC", "CET"], default: "UTC" }),
		input({ key: "NOTE" }),
	];

	test("recognises generator defaults", () => {
		expect(isGenerated(inputs[1] as TemplateInput)).toBe(true);
		expect(isGenerated(inputs[2] as TemplateInput)).toBe(false);
		expect(isGenerated({ default: `$${"{"}input:X}` })).toBe(false);
	});

	test("a field shows what was typed, else the literal default, never a generator", () => {
		expect(inputValue(inputs[2] as TemplateInput, {})).toBe("My site");
		expect(inputValue(inputs[2] as TemplateInput, { TITLE: "Mine" })).toBe(
			"Mine",
		);
		expect(inputValue(inputs[2] as TemplateInput, { TITLE: "" })).toBe("");
		expect(inputValue(inputs[1] as TemplateInput, {})).toBe("");
	});

	test("the body carries only what has a value", () => {
		expect(
			installInputs(inputs, { EMAIL: " a@b.c ", NOTE: "  ", TITLE: "Mine" }),
		).toEqual({ EMAIL: "a@b.c", TITLE: "Mine" });
		expect(installInputs(inputs, {})).toEqual({});
	});

	test("a required input without value or generator is missing", () => {
		expect(missingInputs(inputs, {})).toEqual(["EMAIL"]);
		expect(missingInputs(inputs, { EMAIL: "a@b.c" })).toEqual([]);
		expect(missingInputs(inputs, { EMAIL: "   " })).toEqual(["EMAIL"]);
		const generatedRequired = [
			input({
				key: "KEY",
				required: true,
				secret: true,
				default: generator("secret(8)"),
			}),
		];
		expect(missingInputs(generatedRequired, {})).toEqual([]);
	});

	test("hints say why a field may stay empty", () => {
		expect(inputHint(inputs[1] as TemplateInput)).toMatch(
			/Generated and stored encrypted/,
		);
		expect(inputHint(inputs[2] as TemplateInput)).toBeNull();
		expect(inputHint(input({ key: "K", secret: true }))).toBe(
			"Optional. Stored encrypted.",
		);
		expect(inputHint(input({ key: "K", secret: true, required: true }))).toBe(
			"Stored encrypted.",
		);
	});
});

describe("summarizeChecks", () => {
	const checks: TemplatePlanCheck[] = [
		{ id: "server", status: "ok", message: "online" },
		{ id: "license", status: "warn", message: "source-available" },
		{ id: "memory", status: "warn", message: "tight" },
		{ id: "port-25565", status: "fail", message: "taken" },
		{ id: "secrets", status: "ok", message: "3 secrets" },
		{ id: "proxy-restart", status: "warn", message: "restart" },
	];

	test("failures first, then warnings, then passes", () => {
		const summary = summarizeChecks(checks);
		expect(summary.rows.map((row) => row.id)).toEqual([
			"port-25565",
			"memory",
			"server",
		]);
		expect(summary).toMatchObject({ fail: 1, warn: 1, ok: 1 });
	});

	test("notices with a place of their own are not listed twice", () => {
		const ids = summarizeChecks(checks).rows.map((row) => row.id);
		expect(ids).not.toContain("license");
		expect(ids).not.toContain("secrets");
		expect(ids).not.toContain("proxy-restart");
	});

	test("nothing to list for no checks", () => {
		expect(summarizeChecks([])).toEqual({ fail: 0, warn: 0, ok: 0, rows: [] });
	});

	test("secrets notice counts", () => {
		expect(secretsNotice(0)).toBeNull();
		expect(secretsNotice(1)).toMatch(/^1 secret will/);
		expect(secretsNotice(4)).toMatch(/^4 secrets will/);
	});
});

describe("serviceCards", () => {
	const cards = serviceCards({
		services: [
			{
				key: "app",
				name: "App",
				kind: "app",
				image: "ghcr.io/x/app:1@sha256:abc",
				port: 3000,
				volumes: [{ name: "data", path: "/data" }],
				ports: [
					{ name: "game", port: 25565, protocol: "tcp", publish: true },
					{ name: "query", port: 25566, protocol: "udp", publish: false },
				],
				resources: { memoryMb: 512 },
			},
			{
				key: "db",
				name: "DB",
				kind: "database",
				database: { engine: "postgres", version: "17" },
			},
			{ key: "migrate", name: "Migrate", kind: "worker", oneshot: true },
		],
	});

	test("maps what a card shows", () => {
		expect(cards[0]).toMatchObject({
			kind: "app",
			image: "ghcr.io/x/app:1@sha256:abc",
			httpPort: 3000,
			volumes: 1,
			oneshot: false,
			memoryMb: 512,
		});
		expect(cards[1]).toMatchObject({
			kind: "database",
			database: "postgres 17",
			image: null,
			httpPort: null,
			volumes: 0,
		});
		expect(cards[2]).toMatchObject({ kind: "worker", oneshot: true });
	});

	test("lists only published ports", () => {
		expect(publishedPortsText(cards[0] as (typeof cards)[number])).toBe(
			"tcp/25565",
		);
		expect(publishedPortsText(cards[1] as (typeof cards)[number])).toBe("");
	});
});

describe("stacks", () => {
	const instances: TemplateInstance[] = [
		{
			id: "i1",
			environmentId: "e",
			templateId: "umami",
			templateRevision: 1,
			templateVersion: "3.4.1",
			currentRevision: 1,
			currentVersion: "3.4.1",
			updateAvailable: false,
			name: "Umami",
			groupId: "g1",
			inputs: {},
			createdAt: "2026-01-01T00:00:00Z",
			services: [
				{ id: "s1", key: "app", slug: "umami", name: "Umami" },
				{ id: "s2", key: "db", slug: "umami-db", name: "DB" },
			],
		},
	];

	test("finds a stack by group and by service", () => {
		expect(instanceOfGroup(instances, "g1")?.id).toBe("i1");
		expect(instanceOfGroup(instances, "other")).toBeUndefined();
		expect(instanceOfService(instances, "s2")?.id).toBe("i1");
		expect(instanceOfService(instances, "s9")).toBeUndefined();
	});
});

describe("install outcomes", () => {
	test("titles", () => {
		expect(installedTitle(1, false)).toBe("1 service added, staged");
		expect(installedTitle(3, false)).toBe("3 services added, staged");
		expect(installedTitle(2, true)).toBe("Deploying 2 services");
	});

	test("a conflict asks for a new look at the plan", () => {
		expect(
			installFailure({ status: 409, message: "port taken" }, "Umami"),
		).toMatchObject({
			replan: true,
		});
		expect(
			installFailure({ status: 422, message: "bad" }, "Umami").replan,
		).toBe(true);
	});

	test("a missing scope does not offer a retry", () => {
		const failure = installFailure(
			{ status: 403, message: "needs projects:write" },
			"Umami",
		);
		expect(failure.replan).toBe(false);
		expect(failure.body).toBe("needs projects:write");
	});

	test("anything else names the template", () => {
		const failure = installFailure({ status: 500, message: "boom" }, "Umami");
		expect(failure.title).toBe("Could not add Umami");
	});
});

function srv(over: Partial<Server> & { id: string }): Server {
	return {
		name: over.id,
		status: "online",
		drainState: "active",
		acceptWorkloads: true,
		unrestricted: false,
		...over,
	} as Server;
}

describe("templates that need an unrestricted server", () => {
	const open = srv({ id: "open", unrestricted: true });
	const closed = srv({ id: "closed" });

	test("only an explicit flag counts", () => {
		expect(needsUnrestricted({ requiresUnrestricted: true })).toBe(true);
		expect(needsUnrestricted({ requiresUnrestricted: false })).toBe(false);
		expect(needsUnrestricted({})).toBe(false);
	});

	test("restrictions list the restricted servers with the reason", () => {
		const map = serverRestrictions([closed, open], true);
		expect([...map.keys()]).toEqual(["closed"]);
		expect(map.get("closed")).toBe(RESTRICTED_SERVER_REASON);
		expect(serverRestrictions([closed, open], false).size).toBe(0);
	});

	test("none is unrestricted only when the template needs the tier", () => {
		expect(lacksUnrestrictedServer([closed], true)).toBe(true);
		expect(lacksUnrestrictedServer([closed, open], true)).toBe(false);
		expect(lacksUnrestrictedServer([closed], false)).toBe(false);
		expect(lacksUnrestrictedServer([], true)).toBe(true);
	});

	test("the default server never lands on a restricted one", () => {
		expect(defaultServerId([closed, open])).toBe("closed");
		expect(defaultServerId([closed, open], true)).toBe("open");
		expect(defaultServerId([closed], true)).toBe("");
		expect(
			defaultServerId(
				[closed, srv({ id: "off", unrestricted: true, status: "offline" })],
				true,
			),
		).toBe("off");
	});

	test("the docs anchor exists in the docker options page", () => {
		expect(UNRESTRICTED_DOCS).toBe(
			"operations/docker-options#the-unrestricted-switch-per-server",
		);
	});
});
