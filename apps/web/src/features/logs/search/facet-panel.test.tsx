import { describe, expect, test } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { renderWithProviders } from "#/test/render";
import { FacetPanel, FacetsPopover } from "./facet-panel";
import { FacetStats } from "./facets";
import { parseLine } from "./line";
import type { FacetKey } from "./query";

function statsOf(lines: { line: string; serviceName?: string }[]) {
	const stats = new FacetStats();
	for (const input of lines) stats.add(parseLine(input));
	return stats;
}

function Panel({
	stats,
	keys = ["level", "service"],
	initial = "",
}: {
	stats: FacetStats;
	keys?: FacetKey[];
	initial?: string;
}) {
	const [query, setQuery] = useState(initial);
	return (
		<div>
			<output aria-label="query">{query}</output>
			<FacetPanel
				stats={stats}
				statsVersion={1}
				keys={keys}
				query={query}
				onQueryChange={setQuery}
			/>
		</div>
	);
}

const query = () => screen.getByLabelText("query").textContent;

describe("FacetPanel", () => {
	test("explains the empty state when no line carries a facet", () => {
		render(<Panel stats={statsOf([{ line: "plain words" }])} />);
		expect(screen.getByText(/No facets yet/)).toBeInTheDocument();
		expect(screen.queryByRole("region")).not.toBeInTheDocument();
	});

	test("shows only the requested facets that have values, with counts", () => {
		const stats = statsOf([
			{ line: "ERROR a", serviceName: "api" },
			{ line: "ERROR b", serviceName: "api" },
			{ line: "INFO c", serviceName: "web" },
		]);
		render(<Panel stats={stats} keys={["level", "service", "status"]} />);
		const level = screen.getByRole("region", { name: "Level" });
		expect(within(level).getByText("error")).toBeInTheDocument();
		const errorRow = within(level).getByText("error").closest("li");
		expect(errorRow).toHaveTextContent("error2");
		const infoRow = within(level).getByText("info").closest("li");
		expect(infoRow).toHaveTextContent("info1");
		const service = screen.getByRole("region", { name: "Service" });
		expect(within(service).getByText("api")).toBeInTheDocument();
		expect(
			screen.queryByRole("region", { name: "Status" }),
		).not.toBeInTheDocument();
	});

	test("levels read worst first whatever the counts", () => {
		const stats = statsOf([
			{ line: "DEBUG a" },
			{ line: "DEBUG b" },
			{ line: "DEBUG c" },
			{ line: "INFO d" },
			{ line: "INFO e" },
			{ line: "WARN f" },
			{ line: "ERROR g" },
		]);
		render(<Panel stats={stats} keys={["level"]} />);
		const labels = within(screen.getByRole("region", { name: "Level" }))
			.getAllByRole("button", { name: /^Include / })
			.map((b) => b.getAttribute("aria-label"));
		expect(labels).toEqual([
			"Include level:error",
			"Include level:warn",
			"Include level:info",
			"Include level:debug",
		]);
	});

	test("Include adds the term to the query and a second click removes it", async () => {
		const user = userEvent.setup();
		render(<Panel stats={statsOf([{ line: "ERROR a" }])} keys={["level"]} />);
		const include = screen.getByRole("button", { name: "Include level:error" });
		expect(include).toHaveAttribute("aria-pressed", "false");
		await user.click(include);
		expect(query()).toBe("level:error");
		expect(include).toHaveAttribute("aria-pressed", "true");
		await user.click(include);
		expect(query()).toBe("");
		expect(include).toHaveAttribute("aria-pressed", "false");
	});

	test("Exclude negates the term and swaps an earlier include", async () => {
		const user = userEvent.setup();
		render(
			<Panel
				stats={statsOf([{ line: "ERROR a" }])}
				keys={["level"]}
				initial="timeout level:error"
			/>,
		);
		await user.click(
			screen.getByRole("button", { name: "Exclude level:error" }),
		);
		expect(query()).toBe("timeout -level:error");
		expect(
			screen.getByRole("button", { name: "Exclude level:error" }),
		).toHaveAttribute("aria-pressed", "true");
		expect(
			screen.getByRole("button", { name: "Include level:error" }),
		).toHaveAttribute("aria-pressed", "false");
	});

	test("a section collapses and expands", async () => {
		const user = userEvent.setup();
		render(<Panel stats={statsOf([{ line: "ERROR a" }])} keys={["level"]} />);
		const toggle = screen.getByRole("button", { name: /Level/ });
		expect(toggle).toHaveAttribute("aria-expanded", "true");
		await user.click(toggle);
		expect(toggle).toHaveAttribute("aria-expanded", "false");
		expect(
			screen.queryByRole("button", { name: "Include level:error" }),
		).not.toBeInTheDocument();
		await user.click(toggle);
		expect(
			screen.getByRole("button", { name: "Include level:error" }),
		).toBeInTheDocument();
	});

	test("long lists show eight values and offer the rest", async () => {
		const user = userEvent.setup();
		const lines = Array.from({ length: 11 }, (_, i) => ({
			line: `msg ${i}`,
			serviceName: `svc-${String(i).padStart(2, "0")}`,
		}));
		render(<Panel stats={statsOf(lines)} keys={["service"]} />);
		const includes = () =>
			screen.getAllByRole("button", { name: /^Include service:/ });
		expect(includes()).toHaveLength(8);
		await user.click(screen.getByRole("button", { name: "Show 3 more" }));
		expect(includes()).toHaveLength(11);
		await user.click(screen.getByRole("button", { name: "Show fewer" }));
		expect(includes()).toHaveLength(8);
	});

	test("values with spaces are quoted in the query", async () => {
		const user = userEvent.setup();
		render(
			<Panel
				stats={statsOf([{ line: "hello", serviceName: "my service" }])}
				keys={["service"]}
			/>,
		);
		await user.click(
			screen.getByRole("button", { name: "Include service:my service" }),
		);
		expect(query()).toBe('service:"my service"');
	});
});

describe("FacetsPopover", () => {
	test("opens from a Facets button and edits the query", async () => {
		const user = userEvent.setup();
		const changes: string[] = [];
		await renderWithProviders(
			<FacetsPopover
				stats={statsOf([{ line: "ERROR a" }])}
				statsVersion={1}
				keys={["level"]}
				query=""
				onQueryChange={(next) => changes.push(next)}
			/>,
		);
		expect(
			screen.queryByRole("button", { name: "Include level:error" }),
		).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Facets" }));
		await user.click(
			await screen.findByRole("button", { name: "Include level:error" }),
		);
		expect(changes).toEqual(["level:error"]);
	});
});

function requestStats() {
	const stats = new FacetStats();
	const request = (extra: Record<string, unknown>) =>
		stats.add(
			parseLine({
				line: JSON.stringify({
					method: "GET",
					path: "/api/orders",
					duration_ms: 50,
					kind: "api",
					...extra,
				}),
			}),
		);
	for (const status of [200, 200, 204, 301, 404, 500, 503]) request({ status });
	request({ status: 200, method: "POST", path: "/login", kind: "page" });
	return stats;
}

function RequestPanel({ initial = "" }: { initial?: string }) {
	const [query, setQuery] = useState(initial);
	return (
		<div>
			<output aria-label="query">{query}</output>
			<FacetPanel
				stats={requestStats()}
				statsVersion={1}
				keys={["status"]}
				query={query}
				onQueryChange={setQuery}
				statusClasses
				requests
			/>
		</div>
	);
}

describe("FacetPanel status classes", () => {
	test("sums the status codes by class, with the class as text", () => {
		render(<RequestPanel />);
		const status = within(screen.getByRole("region", { name: "Status" }));
		const row = (label: string) => status.getByText(label).closest("li");
		expect(row("2xx")).toHaveTextContent("4");
		expect(row("3xx")).toHaveTextContent("1");
		expect(row("4xx")).toHaveTextContent("1");
		expect(row("5xx")).toHaveTextContent("2");
		// Exact codes wait behind the disclosure.
		expect(status.queryByText("204")).toBeNull();
	});

	test("a class adds status:2* and its exclusion, and a second click removes it", async () => {
		const user = userEvent.setup();
		render(<RequestPanel />);
		await user.click(screen.getByRole("button", { name: "Include status:2*" }));
		expect(query()).toBe("status:2*");
		await user.click(screen.getByRole("button", { name: "Exclude status:2*" }));
		expect(query()).toBe("-status:2*");
		await user.click(screen.getByRole("button", { name: "Exclude status:2*" }));
		expect(query()).toBe("");
		await user.click(screen.getByRole("button", { name: "Include status:5*" }));
		expect(query()).toBe("status:5*");
	});

	test("the disclosure lists the exact codes, each with its own buttons", async () => {
		const user = userEvent.setup();
		render(<RequestPanel />);
		const disclosure = screen.getByRole("button", { name: "Show 5xx codes" });
		expect(disclosure).toHaveAttribute("aria-expanded", "false");
		await user.click(disclosure);
		expect(
			screen.getByRole("button", { name: "Hide 5xx codes" }),
		).toHaveAttribute("aria-expanded", "true");
		const status = within(screen.getByRole("region", { name: "Status" }));
		expect(status.getByText("500")).toBeInTheDocument();
		expect(status.getByText("503")).toBeInTheDocument();
		await user.click(
			screen.getByRole("button", { name: "Include status:503" }),
		);
		expect(query()).toBe("status:503");
	});

	test("without statusClasses the status facet lists its values", () => {
		render(<Panel stats={requestStats()} keys={["status"]} />);
		const status = within(screen.getByRole("region", { name: "Status" }));
		expect(status.getByText("204")).toBeInTheDocument();
		expect(status.queryByText("2xx")).toBeNull();
	});
});

describe("FacetPanel requests group", () => {
	test("lists kind, method and path values with counts", () => {
		render(<RequestPanel />);
		const requests = within(screen.getByRole("region", { name: "Requests" }));
		expect(
			within(requests.getByRole("region", { name: "Kind" })).getByText("api"),
		).toBeInTheDocument();
		expect(
			within(requests.getByRole("region", { name: "Method" })).getByText(
				"POST",
			),
		).toBeInTheDocument();
		const path = requests.getByRole("region", { name: "Path" });
		expect(within(path).getByText("/api/orders")).toHaveAttribute(
			"title",
			"/api/orders",
		);
	});

	test("a method value edits the query with its attribute key", async () => {
		const user = userEvent.setup();
		render(<RequestPanel />);
		await user.click(
			screen.getByRole("button", { name: "Include @method:POST" }),
		);
		expect(query()).toBe("@method:POST");
	});

	test("the duration ranges write the grammar's comparisons and replace each other", async () => {
		const user = userEvent.setup();
		render(<RequestPanel />);
		const fast = screen.getByRole("button", { name: "< 100 ms" });
		const mid = screen.getByRole("button", { name: "100 ms – 1 s" });
		const slow = screen.getByRole("button", { name: "> 1 s" });
		await user.click(fast);
		expect(query()).toBe("@duration_ms:<100");
		expect(fast).toHaveAttribute("aria-pressed", "true");
		await user.click(mid);
		expect(query()).toBe("@duration_ms:[100 TO 1000]");
		expect(fast).toHaveAttribute("aria-pressed", "false");
		await user.click(slow);
		expect(query()).toBe("@duration_ms:>1000");
		await user.click(slow);
		expect(query()).toBe("");
	});

	test("is left out unless requests are asked for", () => {
		render(<Panel stats={requestStats()} keys={["status"]} />);
		expect(screen.queryByRole("region", { name: "Requests" })).toBeNull();
	});

	test("is left out when the counts have no request attribute", () => {
		render(
			<FacetPanel
				stats={statsOf([{ line: "ERROR a" }])}
				statsVersion={1}
				keys={["level"]}
				query=""
				onQueryChange={() => {}}
				requests
			/>,
		);
		expect(screen.queryByRole("region", { name: "Requests" })).toBeNull();
	});
});
