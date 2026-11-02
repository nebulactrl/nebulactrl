import { describe, expect, test } from "bun:test";
import { make } from "./factories";
import { componentPointer, validateAt } from "./json-schema";

const at = (name: string, value: unknown) =>
	validateAt(componentPointer(name), value);

describe("validateAt (ajv over openapi.json)", () => {
	test("accepts a conforming value, resolving $refs into the components", () => {
		expect(at("ServiceView", make("ServiceView"))).toEqual([]);
		expect(at("PageProject", { items: [make("Project")] })).toEqual([]);
	});

	test("names the JSON path of a wrong type, an unknown enum value and a bad item", () => {
		const project = make("Project");
		expect(at("Project", { ...project, id: 7 })).toEqual([
			"$.id: expected string, got integer",
		]);
		expect(at("Project", { ...project, buildLocation: "moon" })).toEqual([
			'$.buildLocation: "moon" is not one of "inherit", "target", "build_server"',
		]);
		expect(
			at("PageProject", { items: [project, { ...project, id: 3 }] }),
		).toEqual(["$.items[1].id: expected string, got integer"]);
	});

	test("reports a missing required property and an unexpected one", () => {
		const { slug: _slug, ...without } = make("Project");
		expect(at("Project", without)).toEqual([
			'$: missing required property "slug"',
		]);
		expect(at("Project", { ...make("Project"), extra: 1 })).toEqual([
			'$: unexpected property "extra"',
		]);
	});

	test("null is only valid where the schema allows it", () => {
		expect(at("Project", { ...make("Project"), registryId: null })).toEqual([]);
		expect(at("Project", { ...make("Project"), slug: null })).toEqual([
			"$.slug: expected string, got null",
		]);
	});

	test("a nullable object reads as null or the object, and problems inside keep their path", () => {
		const service = make("ServiceView");
		expect(at("ServiceView", { ...service, git: null })).toEqual([]);
		expect(
			at("ServiceView", {
				...service,
				observed: { ...make("ObservedView"), replicasReady: "1" },
			}),
		).toEqual(["$.observed.replicasReady: expected integer, got string"]);
		expect(at("ServiceView", { ...service, git: "x" })).toEqual([
			"$.git: expected object, got string",
		]);
	});

	test("checks formats", () => {
		expect(
			at("Project", { ...make("Project"), createdAt: "yesterday" }),
		).toEqual(['$.createdAt: "yesterday" is not a valid date-time']);
	});

	test("checks patterns", () => {
		expect(
			at("CreateCredentialInputBody", { provider: "Not Valid" }),
		).toContain('$.provider: must match pattern "^[a-z0-9][a-z0-9-]{0,39}$"');
	});

	test("stops after five problems", () => {
		const many = Object.fromEntries(
			[1, 2, 3, 4, 5, 6, 7].map((n) => [`k${n}`, 1]),
		);
		expect(at("Project", many)).toHaveLength(5);
	});

	test("a pointer that names nothing is an error, not a pass", () => {
		expect(() => validateAt("/components/schemas/Nope", {})).toThrow(
			"no schema",
		);
	});
});
