import { describe, expect, test } from "bun:test";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { testQueryClient } from "#/test/render";
import {
	catalogQuery,
	instancesQuery,
	planQuery,
	templateQuery,
} from "./queries";
import { instance, summary, template } from "./test-fixtures";

describe("catalogQuery", () => {
	test("exposes the items and categories of the catalog", async () => {
		const items = [summary({ id: "umami" })];
		const categories = [{ id: "analytics", label: "Analytics", count: 1 }];
		const api = stubApi({
			"GET /catalog/templates": { body: { items, categories } },
		});
		const data = await testQueryClient().fetchQuery(catalogQuery());
		expect(data).toEqual({ items, categories });
		expect(api.calls.map((c) => `${c.method} ${c.path}`)).toEqual([
			"GET /catalog/templates",
		]);
	});

	test("a catalog with null lists reads as empty ones", () => {
		// `select` only runs in an observer; fetchQuery returns the raw body.
		const select = catalogQuery().select;
		expect(select?.({ items: null, categories: null })).toEqual({
			items: [],
			categories: [],
		});
	});

	test("is cached for ten minutes: a second fetch does not call the server", async () => {
		const api = stubApi({
			"GET /catalog/templates": { body: { items: [], categories: [] } },
		});
		const client = testQueryClient();
		await client.fetchQuery(catalogQuery());
		await client.fetchQuery(catalogQuery());
		expect(api.calls).toHaveLength(1);
	});
});

describe("templateQuery", () => {
	test("asks for the one template by id", async () => {
		const umami = template({ id: "umami" });
		const api = stubApi({
			"GET /catalog/templates/:templateId": { body: umami },
		});
		expect(await testQueryClient().fetchQuery(templateQuery("umami"))).toEqual(
			umami,
		);
		expect(api.calls[0]?.path).toBe("/catalog/templates/umami");
	});

	test("does not retry a failure", async () => {
		const api = stubApi({
			"GET /catalog/templates/:templateId": {
				status: 404,
				body: {
					status: 404,
					title: "x",
					detail: "no such template",
					code: "not_found",
				},
			},
		});
		await expect(
			testQueryClient().fetchQuery(templateQuery("nope")),
		).rejects.toBeDefined();
		expect(api.calls).toHaveLength(1);
	});
});

describe("instancesQuery", () => {
	test("lists the stacks of the environment", async () => {
		const stacks = [instance({ id: "i1" })];
		const api = stubApi({
			"GET /environments/:environmentId/template-instances": { body: stacks },
		});
		const client = testQueryClient();
		await client.fetchQuery(instancesQuery("env9"));
		expect(api.calls[0]?.path).toBe("/environments/env9/template-instances");
		expect(client.getQueryData(instancesQuery("env9").queryKey)).toEqual(
			stacks,
		);
	});

	test("select turns a null list into an empty one, always the same array", () => {
		const select = instancesQuery("env1").select;
		const first = select?.(null as never);
		expect(first).toEqual([]);
		expect(select?.(null as never)).toBe(first);
	});
});

describe("planQuery", () => {
	const args = {
		environmentId: "env1",
		templateId: "umami",
		serverId: "s1",
		inputs: { DOMAIN: "a.com" },
	};

	test("posts the server and inputs to the template's plan and returns the plan", async () => {
		const plan = {
			checks: [],
			inputs: [],
			installable: true,
			proxyRestart: false,
			secrets: [],
			services: [],
			template: summary({ id: "umami" }),
			warnings: [],
		};
		const api = stubApi({
			"POST /environments/:environmentId/templates/:templateId/plan": {
				body: plan,
			},
		});
		expect(await testQueryClient().fetchQuery(planQuery(args))).toEqual(plan);
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]?.method).toBe("POST");
		expect(api.calls[0]?.path).toBe("/environments/env1/templates/umami/plan");
		expect(api.calls[0]?.body).toEqual({
			serverId: "s1",
			inputs: { DOMAIN: "a.com" },
		});
	});

	test("without a chosen server the body leaves serverId out", async () => {
		const api = stubApi({
			"POST /environments/:environmentId/templates/:templateId/plan": {
				body: make("TemplatePlan", { checks: [], installable: false }),
			},
		});
		await testQueryClient().fetchQuery(planQuery({ ...args, serverId: "" }));
		expect(api.calls[0]?.body).toEqual({ inputs: { DOMAIN: "a.com" } });
	});

	test("a different server or input is a different cache entry", () => {
		const base = planQuery(args).queryKey;
		expect(planQuery({ ...args, serverId: "s2" }).queryKey).not.toEqual(base);
		expect(
			planQuery({ ...args, inputs: { DOMAIN: "b.com" } }).queryKey,
		).not.toEqual(base);
		expect(planQuery({ ...args }).queryKey).toEqual(base);
	});

	test("a failed preflight is not retried", async () => {
		const api = stubApi({
			"POST /environments/:environmentId/templates/:templateId/plan": problem(
				500,
				{ status: 500, title: "x", detail: "boom", code: "internal" },
			),
		});
		await expect(
			testQueryClient().fetchQuery(planQuery(args)),
		).rejects.toBeDefined();
		expect(api.calls).toHaveLength(1);
	});
});
