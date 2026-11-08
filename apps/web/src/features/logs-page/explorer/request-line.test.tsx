import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { parseLine } from "#/features/logs/search/line";
import {
	formatRequestDuration,
	RequestLine,
	requestFields,
	statusTone,
} from "./request-line";

const record = (fields: Record<string, unknown>) =>
	parseLine({ line: JSON.stringify(fields), stream: "requests" });

describe("formatRequestDuration", () => {
	test.each([
		[0.4, "<1 ms"],
		[12, "12 ms"],
		[999, "999 ms"],
		[1234, "1.2 s"],
		[59_000, "59.0 s"],
		[125_000, "2m 05s"],
	])("%p is %p", (ms, text) => {
		expect(formatRequestDuration(ms)).toBe(text);
	});
});

describe("statusTone", () => {
	test("2xx ok, 3xx muted, 4xx warning, 5xx failed", () => {
		expect([200, 204, 301, 304, 404, 429, 500, 503].map(statusTone)).toEqual([
			"ok",
			"ok",
			"off",
			"off",
			"warn",
			"warn",
			"bad",
			"bad",
		]);
	});
});

describe("requestFields", () => {
	test("reads what the stored record states", () => {
		expect(
			requestFields(
				record({
					method: "GET",
					host: "a.example.com",
					path: "/x",
					query: "q=1",
					status: 200,
					duration_ms: 7,
					process: "web",
				}),
			),
		).toEqual({
			method: "GET",
			host: "a.example.com",
			path: "/x?q=1",
			status: 200,
			durationMs: 7,
			process: "web",
		});
	});

	test("reads the kind and the bytes sent", () => {
		expect(
			requestFields(
				record({ method: "GET", path: "/a.js", kind: "asset", bytes: 2048 }),
			),
		).toEqual({
			method: "GET",
			host: "",
			path: "/a.js",
			kind: "asset",
			bytes: 2048,
		});
	});

	test("leaves out what the record does not state", () => {
		expect(requestFields(record({ method: "GET", path: "/x" }))).toEqual({
			method: "GET",
			host: "",
			path: "/x",
		});
	});

	test("a record without a method or a path is not a request", () => {
		expect(requestFields(record({ method: "GET" }))).toBeNull();
		expect(requestFields(record({ path: "/x" }))).toBeNull();
		expect(requestFields(parseLine({ line: "plain text" }))).toBeNull();
	});
});

describe("RequestLine", () => {
	test("puts the path whole in the tooltip and marks search terms in it", () => {
		const { container } = render(
			<RequestLine
				fields={{
					method: "DELETE",
					host: "a.example.com",
					path: "/api/orders/42",
					status: 500,
					durationMs: 1500,
				}}
				terms={[{ parts: ["orders"] }]}
			/>,
		);
		expect(screen.getByText("DELETE")).toBeInTheDocument();
		expect(screen.getByText("500")).toHaveAttribute("data-tone", "bad");
		expect(screen.getByText("1.5 s")).toBeInTheDocument();
		expect(
			screen.getByTitle("a.example.com/api/orders/42"),
		).toBeInTheDocument();
		expect(
			[...container.querySelectorAll("mark")].map((m) => m.textContent),
		).toEqual(["orders"]);
	});

	test("hides the host when asked and still has it in the tooltip", () => {
		render(
			<RequestLine
				fields={{ method: "GET", host: "a.example.com", path: "/x" }}
				terms={[]}
				showHost={false}
			/>,
		);
		expect(screen.queryByText("a.example.com")).toBeNull();
		expect(screen.getByTitle("a.example.com/x")).toBeInTheDocument();
	});

	test("tags asset and other requests, not page or api ones", () => {
		const row = (kind: string) =>
			render(
				<RequestLine
					fields={{ method: "GET", host: "", path: `/${kind}`, kind }}
					terms={[]}
				/>,
			);
		for (const kind of ["page", "api"]) {
			const { unmount } = row(kind);
			expect(screen.queryByText(kind)).toBeNull();
			unmount();
		}
		const asset = row("asset");
		expect(screen.getByText("asset")).toBeInTheDocument();
		asset.unmount();
		row("other");
		expect(screen.getByText("other")).toBeInTheDocument();
	});

	test("colours the duration by speed and always prints the number", () => {
		const duration = (durationMs: number) => {
			const { unmount } = render(
				<RequestLine
					fields={{ method: "GET", host: "", path: "/x", durationMs }}
					terms={[]}
				/>,
			);
			const text = formatRequestDuration(durationMs);
			const cell = screen.getByText(text);
			const tone = [...cell.classList].find((c) =>
				/^text-(ink|status)/.test(c),
			);
			unmount();
			return tone;
		};
		expect(duration(999)).toBe("text-ink-3");
		expect(duration(1000)).toBe("text-status-staged-on");
		expect(duration(4999)).toBe("text-status-staged-on");
		expect(duration(5000)).toBe("text-status-failed-on");
	});
});
