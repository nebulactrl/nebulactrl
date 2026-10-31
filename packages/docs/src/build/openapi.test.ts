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
