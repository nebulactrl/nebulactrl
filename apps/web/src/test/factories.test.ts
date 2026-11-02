import { describe, expect, test } from "bun:test";
import { make, type SchemaName } from "./factories";
import { DEFAULTS, IDS } from "./factory-defaults";
import { specDocument } from "./json-schema";

const tableNames = Object.keys(DEFAULTS) as SchemaName[];
const allNames = Object.keys(specDocument().components.schemas) as SchemaName[];

describe("the default table", () => {
	test("names real schemas, and there are about two dozen of them", () => {
		expect(tableNames.length).toBeGreaterThanOrEqual(25);
		for (const name of tableNames) {
			expect(allNames).toContain(name);
		}
	});

	test("every default in the table is valid against its schema", () => {
		// make() checks its result; it throws with the JSON path when it is not valid.
		for (const name of tableNames) {
			expect(() => make(name)).not.toThrow();
		}
	});

	test("the scene's ids agree: a service's server, project and environment are the defaults", () => {
		const service = make("ServiceView");
		expect(service.projectId).toBe(make("Project").id);
		expect(service.environmentId).toBe(make("Environment").id);
		expect(service.serverId).toBe(make("Server").id);
		expect(service.server?.id).toBe(IDS.server);
		expect(service.latestDeployment?.id).toBe(
			service.currentDeploymentId ?? undefined,
		);
	});

	test("a default is a running service named web", () => {
		const service = make("ServiceView");
		expect(service.name).toBe("web");
		expect(service.desiredState).toBe("running");
		expect(service.observed?.state).toBe("running");
		expect(service.config.image).toBe("ghcr.io/acme/web:1.0.0");
	});
});

describe("the structural defaults", () => {
	test("every schema of the API yields a valid default", () => {
		for (const name of allNames) {
			expect(() => make(name)).not.toThrow();
		}
	});

	test("hold required members only, the smallest legal value", () => {
		// PageServer has no table entry; `nextCursor` is optional, so it is absent.
		expect(make("PageServer")).toEqual({ items: [] });
		// DNSField has none either: "" / 0 / false / first enum value for what it requires.
		for (const value of Object.values(make("DNSField"))) {
			expect(["", 0, false]).toContain(value as never);
		}
	});

	test("a nullable member is null and a nullable list is empty", () => {
		const server = make("Server");
		expect(server.facts).toBeNull();
		expect(server.latest).toBeNull();
		const service = make("ServiceView");
		expect(service.domains).toEqual([]);
		expect(service.git).toBeNull();
	});
});

describe("overrides", () => {
	test("merge deep into objects and leave the rest of the default alone", () => {
		const service = make("ServiceView", {
			name: "api",
			config: { port: 3000 },
		});
		expect(service.name).toBe("api");
		expect(service.config.port).toBe(3000);
		expect(service.config.image).toBe(make("ServiceView").config.image);
		expect(service.config.healthcheck).toEqual(
			make("ServiceView").config.healthcheck,
		);
	});

	test("replace an array wholesale", () => {
		const service = make("ServiceView", {
			domains: [make("DomainView", { hostname: "a.example.com" })],
		});
		expect(service.domains?.map((d) => d.hostname)).toEqual(["a.example.com"]);
		const again = make("ServiceView", { domains: [] });
		expect(again.domains).toEqual([]);
	});

	test("fill a null default from a partial object", () => {
		const server = make("Server", { latest: { cpuPercent: 12 } });
		expect(server.latest?.cpuPercent).toBe(12);
		expect(server.latest?.memTotal).toBe(0);
		const service = make("ServiceView", { git: { repo: "acme/shop" } });
		expect(service.git?.repo).toBe("acme/shop");
		expect(service.git?.provider).toBe("github");
	});

	test("set null where the schema allows it", () => {
		expect(make("ServiceView", { observed: null }).observed).toBeNull();
		expect(
			make("ServiceView", { currentDeploymentId: null }).currentDeploymentId,
		).toBeNull();
	});

	test("remove an optional member with undefined", () => {
		const service = make("ServiceView", { config: { port: undefined } });
		expect("port" in service.config).toBe(false);
	});

	test("never share state between results", () => {
		const a = make("ServiceView");
		a.config.healthcheck.type = "tcp";
		expect(make("ServiceView").config.healthcheck.type).toBe("http");
	});

	test("that break the schema throw, naming the schema and the JSON path", () => {
		expect(() =>
			// @ts-expect-error an enum member the type does not have
			make("ServiceView", { kind: "toaster" }),
		).toThrow(
			/make\("ServiceView"\) breaks the schema:\s+\$\.kind: "toaster" is not one of/,
		);
		expect(() =>
			// @ts-expect-error a member the schema does not have
			make("Project", { colour: "red" }),
		).toThrow('$: unexpected property "colour"');
		expect(() => make("Project", { createdAt: "yesterday" })).toThrow(
			'$.createdAt: "yesterday" is not a valid date-time',
		);
		expect(() =>
			// @ts-expect-error a required member cannot be removed
			make("Project", { slug: undefined }),
		).toThrow('$: missing required property "slug"');
	});

	test("are typed by the schema", () => {
		// @ts-expect-error `system` is a boolean
		expect(() => make("Project", { system: "yes" })).toThrow();
		// @ts-expect-error not a schema of the API
		expect(() => make("Nope")).toThrow('has no schema "Nope"');
	});
});
