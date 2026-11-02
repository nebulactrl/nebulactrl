import { describe, expect, test } from "bun:test";
import { groupByTag } from "./openapi.ts";

const spec = {
	openapi: "3.1.0",
	tags: [
		{ name: "services", description: "Services" },
		{ name: "auth", description: "Sessions" },
	],
	security: [{ bearer: [] }, { cookie: [] }],
	paths: {
		"/services/{serviceId}": {
			get: {
				operationId: "getService",
				summary: "Get a service",
				tags: ["services"],
				parameters: [
					{
						in: "path",
						name: "serviceId",
						required: true,
						schema: { type: "string" },
					},
				],
				responses: {
					"200": {
						description: "OK",
						content: {
							"application/json": {
								schema: { $ref: "#/components/schemas/Service" },
							},
						},
					},
					"404": {
						description: "Not Found",
						content: {
							"application/problem+json": {
								schema: { $ref: "#/components/schemas/Error" },
							},
						},
					},
				},
			},
		},
		"/services/{serviceId}/deployments": {
			post: {
				operationId: "deployService",
				summary: "Deploy",
				description:
					"Returns 202. 409 `deployment_in_progress` while another is active; 422 `no_server` otherwise.",
				tags: ["services"],
				parameters: [
					{
						in: "header",
						name: "Idempotency-Key",
						schema: { type: "string", maxLength: 255 },
					},
					{
						in: "path",
						name: "serviceId",
						required: true,
						schema: { type: "string" },
					},
				],
				requestBody: {
					required: true,
					content: {
						"application/json": {
							schema: { $ref: "#/components/schemas/Deploy" },
						},
					},
				},
				responses: { "202": { description: "Accepted" } },
			},
		},
		"/auth/login": {
			post: {
				operationId: "login",
				summary: "Sign in",
				tags: ["auth"],
				security: [{}],
				responses: { "200": { description: "OK" } },
			},
		},
	},
	components: {
		schemas: {
			Error: {
				type: "object",
				properties: { code: { type: "string" } },
				required: ["code"],
			},
			Deploy: {
				type: "object",
				properties: {
					redeploy: { type: "boolean", default: false },
					parent: { $ref: "#/components/schemas/Service" },
				},
			},
			Service: {
				type: "object",
				required: ["id"],
				properties: {
					id: { type: "string" },
					tags: { type: ["array", "null"], items: { type: "string" } },
					parent: { $ref: "#/components/schemas/Service" },
					state: { enum: ["a", "b"], type: "string" },
				},
			},
		},
	},
};

describe("groupByTag", () => {
	const tags = groupByTag(spec);

	test("groups operations by tag, tags sorted by name, operations by path then method", () => {
		expect(tags.map((t) => t.name)).toEqual(["auth", "services"]);
		expect(tags[1]?.operations.map((o) => `${o.method} ${o.path}`)).toEqual([
			"GET /services/{serviceId}",
			"POST /services/{serviceId}/deployments",
		]);
		expect(tags[1]?.description).toBe("Services");
	});

	test("path parameters are required and typed", () => {
		const get = tags[1]?.operations[0];
		expect(get?.params).toEqual([
			{
				name: "serviceId",
				in: "path",
				required: true,
				type: "string",
				description: "",
			},
		]);
	});

	test("marks operations with an empty security requirement as public", () => {
		expect(tags[0]?.operations[0]?.public).toBe(true);
		expect(tags[1]?.operations[0]?.public).toBe(false);
	});

	test("converts refs into named defs and survives self-reference", () => {
		const get = tags[1]?.operations[0];
		expect(get?.responses[0]?.schema).toEqual({ ref: "Service" });
		const defs = tags[1]?.defs;
		expect(Object.keys(defs ?? {}).sort()).toEqual(["Deploy", "Service"]);
		const service = defs?.Service;
		expect(service?.props?.map((p) => p.name)).toEqual([
			"id",
			"tags",
			"parent",
			"state",
		]);
		expect(service?.props?.[0]?.required).toBe(true);
		expect(service?.props?.[1]?.schema).toMatchObject({
			type: "array",
			nullable: true,
			items: { type: "string" },
		});
		expect(service?.props?.[2]?.schema).toEqual({ ref: "Service" });
		expect(service?.props?.[3]?.schema.enum).toEqual(["a", "b"]);
	});

	test("keeps the request body, drops error schemas, and reads error codes from the description", () => {
		const get = tags[1]?.operations[0];
		expect(
			get?.responses.find((r) => r.status === "404")?.schema,
		).toBeUndefined();
		const deploy = tags[1]?.operations[1];
		expect(deploy?.body).toMatchObject({
			contentType: "application/json",
			required: true,
			schema: { ref: "Deploy" },
		});
		expect(deploy?.errorCodes).toEqual([
			{ status: "409", code: "deployment_in_progress" },
			{ status: "422", code: "no_server" },
		]);
		expect(
			deploy?.params.find((p) => p.name === "Idempotency-Key")?.required,
		).toBe(false);
	});
});

describe("groupByTag schema conversion", () => {
	const doc = {
		openapi: "3.1.0",
		paths: {
			"/things": {
				post: {
					operationId: "makeThing",
					tags: ["things"],
					requestBody: {
						content: {
							"application/json": {
								schema: { $ref: "#/components/schemas/Thing" },
							},
						},
					},
					responses: {
						"200": {
							description: "OK",
							content: {
								"application/json": {
									schema: { $ref: "#/components/schemas/Merged" },
								},
							},
						},
						"201": {
							description: "Created",
							content: {
								"application/json": {
									schema: { $ref: "#/components/schemas/Extended" },
								},
							},
						},
					},
				},
			},
			"/untagged": {
				get: {
					parameters: [
						{
							in: "query",
							name: "q",
							required: true,
							description: " Search text ",
							schema: {
								type: "array",
								items: { type: "string", format: "uuid" },
							},
						},
						{ in: "query", name: "kind", schema: { enum: ["a", "b"] } },
						{
							in: "header",
							name: "X-Id",
							schema: { $ref: "#/components/schemas/Id" },
						},
						{
							in: "query",
							name: "when",
							schema: { type: "string", format: "date-time" },
						},
					],
					responses: { "204": { description: "Done" } },
				},
			},
		},
		components: {
			schemas: {
				Base: {
					type: "object",
					properties: { id: { type: "string" } },
					required: ["id"],
				},
				Merged: {
					allOf: [
						{ $ref: "#/components/schemas/Base" },
						{ properties: { extra: { type: "integer" } } },
						{ $ref: "#/components/schemas/Missing" },
					],
				},
				Extended: {
					allOf: [{ $ref: "#/components/schemas/Base" }],
					properties: { own: { type: "boolean" } },
					required: ["own"],
				},
				Thing: {
					properties: {
						// A union member that is only null makes the field nullable.
						owner: {
							oneOf: [{ type: "null" }, { $ref: "#/components/schemas/Base" }],
						},
						either: { anyOf: [{ type: "string" }, { type: "integer" }] },
						labels: {
							type: "object",
							additionalProperties: { type: "string" },
						},
						maybe: {
							type: ["string", "null"],
							description: "  Optional  ",
							default: 3,
						},
						anything: { additionalProperties: true },
					},
				},
				Id: { type: "string" },
			},
		},
	};
	const [other, things] = groupByTag(doc);
	const thing = things?.defs.Thing;
	const prop = (name: string) =>
		thing?.props?.find((p) => p.name === name)?.schema;

	test("operations without a tag go under 'other', with a generated id", () => {
		expect(other?.name).toBe("other");
		expect(other?.operations[0]?.id).toBe("get/untagged");
		expect(other?.operations[0]?.responses).toEqual([
			{ status: "204", description: "Done" },
		]);
	});

	test("allOf merges the properties of every part, dropping a part it cannot resolve", () => {
		const merged = things?.defs.Merged;
		expect(merged?.props?.map((p) => [p.name, p.required])).toEqual([
			["id", true],
			["extra", false],
		]);
		expect(merged?.type).toBe("object");
	});

	test("a schema's own properties come after the ones it gets from allOf", () => {
		expect(
			things?.defs.Extended?.props?.map((p) => [p.name, p.required]),
		).toEqual([
			["id", true],
			["own", true],
		]);
	});

	test("a null member of a union makes the field nullable and is not listed as a variant", () => {
		expect(prop("owner")).toEqual({
			nullable: true,
			variants: [{ ref: "Base" }],
		});
	});

	test("a union of types lists each as a variant", () => {
		expect(prop("either")?.variants).toEqual([
			{ type: "string" },
			{ type: "integer" },
		]);
	});

	test("a free-form map keeps the schema of its values", () => {
		expect(prop("labels")).toEqual({
			type: "object",
			additional: { type: "string" },
		});
	});

	test("type arrays, descriptions and defaults are carried over", () => {
		expect(prop("maybe")).toEqual({
			type: "string",
			nullable: true,
			description: "Optional",
			default: "3",
		});
		expect(prop("anything")).toEqual({});
	});

	test("parameter types are labelled: arrays of formats, enums, refs and formats", () => {
		const params = other?.operations[0]?.params;
		expect(
			params?.map((p) => [p.name, p.type, p.required, p.description]),
		).toEqual([
			["q", "string (uuid)[]", true, "Search text"],
			["kind", "enum: a | b", false, ""],
			["X-Id", "Id", false, ""],
			["when", "string (date-time)", false, ""],
		]);
	});

	test("a $ref reaches every schema the operation names, once", () => {
		expect(Object.keys(things?.defs ?? {}).sort()).toEqual([
			"Base",
			"Extended",
			"Merged",
			"Thing",
		]);
	});
});
