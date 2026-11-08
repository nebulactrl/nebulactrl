import { describe, expect, mock, test } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { LogLine } from "#/api/sse";
import { LineDetail } from "./line-detail";
import { logsAround } from "./request-line";

const request: LogLine = {
	id: 1,
	ts: "2026-10-29T10:00:20.000Z",
	line: JSON.stringify({
		method: "POST",
		path: "/api/orders",
		status: 502,
		duration_ms: 1234,
		user: { id: "u 1" },
	}),
	stream: "requests",
	serviceName: "api",
	replica: 0,
};

function detail(
	props: Partial<Parameters<typeof LineDetail>[0]> = {},
	line: LogLine = request,
) {
	const onQueryChange = mock();
	const onClose = mock();
	render(
		<LineDetail
			line={line}
			query=""
			onQueryChange={onQueryChange}
			onClose={onClose}
			{...props}
		/>,
	);
	return { onQueryChange, onClose };
}

describe("LineDetail", () => {
	test("shows the raw line and every field", () => {
		detail();
		const pane = screen.getByRole("complementary", { name: "Log line detail" });
		expect(
			within(pane).getByText("2026-10-29T10:00:20.000Z"),
		).toBeInTheDocument();
		expect(within(pane).getByText("@method")).toBeInTheDocument();
		expect(within(pane).getByText("@duration_ms")).toBeInTheDocument();
		// A nested attribute keeps its path.
		expect(within(pane).getByText("@user.id")).toBeInTheDocument();
		// The built-ins are facet keys, and the status is listed once.
		expect(within(pane).getByText("service")).toBeInTheDocument();
		expect(within(pane).getByText("stream")).toBeInTheDocument();
		expect(within(pane).getAllByText("status")).toHaveLength(1);
		expect(within(pane).queryByText("@status")).toBeNull();
	});

	test("Filter and Exclude edit the query with the key form of the field", async () => {
		const user = userEvent.setup();
		const { onQueryChange } = detail({ query: "error" });
		await user.click(
			screen.getByRole("button", { name: "Filter @method:POST" }),
		);
		expect(onQueryChange).toHaveBeenLastCalledWith("error @method:POST");
		await user.click(
			screen.getByRole("button", { name: "Exclude stream:requests" }),
		);
		expect(onQueryChange).toHaveBeenLastCalledWith("error -stream:requests");
		await user.click(
			screen.getByRole("button", { name: "Filter @user.id:u 1" }),
		);
		expect(onQueryChange).toHaveBeenLastCalledWith('error @user.id:"u 1"');
	});

	test("a term already in the query is pressed, and pressing it again removes it", async () => {
		const user = userEvent.setup();
		const { onQueryChange } = detail({ query: "@method:POST -status:502" });
		const include = screen.getByRole("button", { name: "Filter @method:POST" });
		expect(include).toHaveAttribute("aria-pressed", "true");
		expect(
			screen.getByRole("button", { name: "Exclude status:502" }),
		).toHaveAttribute("aria-pressed", "true");
		await user.click(include);
		expect(onQueryChange).toHaveBeenLastCalledWith("-status:502");
	});

	test("the close button closes", async () => {
		const user = userEvent.setup();
		const { onClose } = detail();
		await user.click(screen.getByRole("button", { name: "Close detail" }));
		expect(onClose).toHaveBeenCalledTimes(1);
	});

	test("offers to copy the raw line", () => {
		detail();
		expect(
			screen.getByRole("button", { name: "Copy raw line" }),
		).toBeInTheDocument();
	});

	test("a plain text line has the raw text and says it states no fields", () => {
		detail({}, { id: 2, line: "disk almost full" });
		const pane = screen.getByRole("complementary", { name: "Log line detail" });
		expect(
			within(pane).getAllByText("disk almost full").length,
		).toBeGreaterThan(0);
		expect(
			within(pane).getByText(/This line states no fields/),
		).toBeInTheDocument();
	});
});

describe("LineDetail request summary", () => {
	const full: LogLine = {
		...request,
		line: JSON.stringify({
			method: "POST",
			host: "app.example.com",
			path: "/api/orders",
			status: 502,
			duration_ms: 5400,
			bytes: 2048,
		}),
	};

	test("a request line starts with METHOD status path, then duration, size and time", () => {
		detail({}, full);
		const summary = screen.getByRole("region", { name: "Request" });
		const first = within(summary).getByText("POST").closest("p");
		expect(first).toHaveTextContent("POST502/api/orders");
		expect(within(summary).getByText("502")).toHaveAttribute(
			"data-tone",
			"bad",
		);
		expect(within(summary).getByText("app.example.com")).toBeInTheDocument();
		const facts = within(summary).getByText("5.4 s").closest("p");
		const clock = new Date(full.ts as string).toLocaleTimeString("en-GB", {
			hour12: false,
		});
		expect(facts).toHaveTextContent(`5.4 s·2 KiB·${clock}`);
		// A slow request says so in more than the number's colour: the number is there.
		expect(within(summary).getByText("5.4 s")).toHaveClass(
			"text-status-failed-on",
		);
	});

	test("a runtime line has no request summary", () => {
		detail({}, { id: 2, line: "server listening", stream: "runtime" });
		expect(screen.queryByRole("region", { name: "Request" })).toBeNull();
	});

	test("Show logs around this request calls back, and is left out without a handler", async () => {
		const user = userEvent.setup();
		const onShowAround = mock();
		detail({ onShowAround }, full);
		await user.click(
			screen.getByRole("button", { name: "Show logs around this request" }),
		);
		expect(onShowAround).toHaveBeenCalledTimes(1);
	});

	test("without a handler there is no such button", () => {
		detail({}, full);
		expect(
			screen.queryByRole("button", { name: "Show logs around this request" }),
		).toBeNull();
	});
});

describe("logsAround", () => {
	const line = (extra: Partial<LogLine> = {}): LogLine => ({
		id: 1,
		ts: "2026-10-29T10:00:20.000Z",
		line: "{}",
		serviceName: "api",
		...extra,
	});

	test("is the line's time ± 30 s and its service", () => {
		expect(logsAround(line())).toEqual({
			from: "2026-10-29T09:59:50.000Z",
			to: "2026-10-29T10:00:50.000Z",
			query: "service:api",
		});
	});

	test("adds the replica when the line states one, even 0", () => {
		expect(logsAround(line({ replica: 0 }))?.query).toBe(
			"service:api replica:0",
		);
		expect(logsAround(line({ replica: 2 }))?.query).toBe(
			"service:api replica:2",
		);
	});

	test("quotes a service name with spaces", () => {
		expect(logsAround(line({ serviceName: "my api" }))?.query).toBe(
			'service:"my api"',
		);
	});

	test("is null without a readable time or a service", () => {
		expect(logsAround(line({ ts: "later" }))).toBeNull();
		const { ts: _ts, ...untimed } = line();
		expect(logsAround(untimed)).toBeNull();
		const { serviceName: _name, ...unnamed } = line();
		expect(logsAround(unnamed)).toBeNull();
	});
});
