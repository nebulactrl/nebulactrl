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
