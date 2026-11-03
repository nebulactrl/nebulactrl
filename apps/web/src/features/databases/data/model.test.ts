import { describe, expect, test } from "bun:test";
import { ApiError } from "#/api/client-config";
import { make } from "#/test/factories";
import {
	blockedMessage,
	dialectFor,
	estimateLabel,
	filterTables,
	hasNextPage,
	isConfirmationRequired,
	needsWriteConfirmation,
	type QueryResult,
	resultFooter,
	rowsLabel,
	type TableRef,
	tableKey,
	toQueryResult,
	toTables,
	totalRows,
} from "./model";

const table = (schema: string, name: string): TableRef => ({
	schema,
	name,
	kind: "table",
	rowEstimate: undefined,
});

describe("toQueryResult", () => {
	test("keeps NULL apart from the empty string and fills what the wire leaves null", () => {
		const result = toQueryResult(
			make("DataResult", {
				durationMs: 12,
				results: [
					{
						columns: [{ name: "a", type: "int4" }],
						rows: [["1"], [null], [""]],
						rowCount: 3,
					},
				],
			}),
		);
		expect(result.durationMs).toBe(12);
		expect(result.truncatedCells).toBe(false);
		expect(result.sets[0]?.rows).toEqual([["1"], [null], [""]]);
		expect(result.sets[0]?.truncated).toBe(false);
		expect(result.sets[0]?.commandTag).toBe("");
	});

	test("a statement without rows has no columns and carries its tag", () => {
		const result = toQueryResult({
			durationMs: 1,
			results: [
				{
					columns: null,
					rows: null,
					rowCount: 0,
					commandTag: "INSERT 0 1",
				},
			],
		});
		expect(result.sets[0]).toEqual({
			columns: [],
			rows: [],
			rowCount: 0,
			truncated: false,
			commandTag: "INSERT 0 1",
		});
	});

	test("a null result list is no sets", () => {
		expect(toQueryResult({ durationMs: 0, results: null }).sets).toEqual([]);
	});
});

describe("tables", () => {
	test("toTables maps the listing and tolerates a null list", () => {
		expect(toTables({ tables: null })).toEqual([]);
		expect(
			toTables({
				tables: [
					{ schema: "public", name: "users", kind: "table", rowEstimate: 7 },
				],
			}),
		).toEqual([
			{ schema: "public", name: "users", kind: "table", rowEstimate: 7 },
		]);
	});

	test("a table's key is schema.name, or the name alone without a schema", () => {
		expect(tableKey(table("public", "users"))).toBe("public.users");
		expect(tableKey(table("", "users"))).toBe("users");
	});

	test("the filter ignores case and matches the schema too", () => {
		const all = [
			table("public", "users"),
			table("public", "orders"),
			table("audit", "Users_log"),
		];
		expect(filterTables(all, "").length).toBe(3);
		expect(filterTables(all, "  ").length).toBe(3);
		expect(filterTables(all, "USER").map(tableKey)).toEqual([
			"public.users",
			"audit.Users_log",
		]);
		expect(filterTables(all, "audit.").map(tableKey)).toEqual([
			"audit.Users_log",
		]);
		expect(filterTables(all, "nope")).toEqual([]);
	});

	test("an estimate reads as an approximation, or nothing", () => {
		expect(estimateLabel(undefined)).toBe("");
		expect(estimateLabel(1)).toBe("~1 row");
		expect(estimateLabel(42)).toBe("~42 rows");
	});
});

describe("the result footer", () => {
	const result: QueryResult = {
		durationMs: 12,
		truncatedCells: false,
		sets: [
			{ columns: [], rows: [], rowCount: 2, truncated: false, commandTag: "" },
			{ columns: [], rows: [], rowCount: 1, truncated: true, commandTag: "" },
		],
	};
	test("counts the rows of every set and the time", () => {
		expect(totalRows(result)).toBe(3);
		expect(resultFooter(result)).toBe("3 rows · 12 ms");
		expect(rowsLabel(1)).toBe("1 row");
		expect(rowsLabel(1000)).toBe("1000 rows");
	});
	test("a cut page has a next page", () => {
		expect(hasNextPage(result.sets[1])).toBe(true);
		expect(hasNextPage(result.sets[0])).toBe(false);
		expect(hasNextPage(undefined)).toBe(false);
	});
});

describe("writes and dialects", () => {
	test("only a write in production asks for the slug", () => {
		expect(needsWriteConfirmation(true, true)).toBe(true);
		expect(needsWriteConfirmation(true, false)).toBe(false);
		expect(needsWriteConfirmation(false, true)).toBe(false);
	});

	test("each SQL engine has its dialect, ClickHouse the standard one", () => {
		expect(dialectFor("postgres")).toBe("postgres");
		expect(dialectFor("mysql")).toBe("mysql");
		expect(dialectFor("mariadb")).toBe("mariadb");
		expect(dialectFor("clickhouse")).toBe("standard");
		expect(dialectFor(undefined)).toBe("standard");
	});
});

describe("error codes", () => {
	const api = (code: string, status = 409) =>
		new ApiError(status, code, "from the server");

	test("the blocked states say what to do", () => {
		expect(blockedMessage(api("service_stopped"))).toBe(
			"Start the database to browse its data.",
		);
		expect(blockedMessage(api("not_deployed"))).toBe(
			"Deploy the database first.",
		);
		expect(blockedMessage(api("database_fenced"))).toBe(
			"This database is fenced.",
		);
	});

	test("a failed query is not a blocked state", () => {
		expect(blockedMessage(api("query_failed", 422))).toBeUndefined();
		expect(blockedMessage(new Error("boom"))).toBeUndefined();
	});

	test("confirmation_required is recognised", () => {
		expect(isConfirmationRequired(api("confirmation_required"))).toBe(true);
		expect(isConfirmationRequired(api("query_failed"))).toBe(false);
	});
});
