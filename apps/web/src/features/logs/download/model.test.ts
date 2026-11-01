import { describe, expect, test } from "bun:test";
import { downloadQuery, filenameFromDisposition } from "./model";

describe("downloadQuery", () => {
	test("keeps the format and drops empty values", () => {
		expect(downloadQuery({ format: "txt", q: "  ", from: "" })).toEqual({
			format: "txt",
		});
	});
	test("carries the range, the query and the component", () => {
		expect(
			downloadQuery({
				format: "ndjson",
				from: "2026-10-28T08:00:00Z",
				to: "2026-10-28T09:00:00Z",
				q: " level:error ",
				component: "web",
			}),
		).toEqual({
			format: "ndjson",
			from: "2026-10-28T08:00:00Z",
			to: "2026-10-28T09:00:00Z",
			q: "level:error",
			component: "web",
		});
	});
	test("carries the process of a service with processes", () => {
		expect(downloadQuery({ format: "txt", process: "worker" })).toEqual({
			format: "txt",
			process: "worker",
		});
		expect(downloadQuery({ format: "txt", process: "" })).toEqual({
			format: "txt",
		});
	});
});

describe("filenameFromDisposition", () => {
	test("reads the attachment file name", () => {
		expect(
			filenameFromDisposition(
				'attachment; filename="web-logs-20261028T080507Z.txt"',
				"x.txt",
			),
		).toBe("web-logs-20261028T080507Z.txt");
	});
	test("falls back when the header is missing or has a path", () => {
		expect(filenameFromDisposition(null, "x.txt")).toBe("x.txt");
		expect(
			filenameFromDisposition('attachment; filename="../x"', "y.txt"),
		).toBe("y.txt");
	});
});
