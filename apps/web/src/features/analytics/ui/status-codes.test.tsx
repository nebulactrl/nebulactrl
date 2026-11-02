import { describe, expect, test } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import { StatusCodeList } from "./status-codes";

function row(code: string) {
	return screen.getByText(code).closest("li") as HTMLElement;
}

/** The code rows, below the class bar's legend. */
function codeRows() {
	const list = screen.getByText("200").closest("ul") as HTMLElement;
	return within(list).getAllByRole("listitem");
}

describe("StatusCodeList", () => {
	test("shows an empty message when there are no responses", () => {
		const { rerender } = render(<StatusCodeList codes={null} />);
		expect(screen.getByText("No responses in this range.")).toBeInTheDocument();
		rerender(<StatusCodeList codes={[{ code: 200, n: 0 }]} />);
		expect(screen.getByText("No responses in this range.")).toBeInTheDocument();
		expect(screen.queryByRole("list")).not.toBeInTheDocument();
	});

	test("lists codes biggest first with class, reason, count and share", () => {
		render(
			<StatusCodeList
				codes={[
					{ code: 404, n: 20 },
					{ code: 200, n: 1_500 },
					{ code: 502, n: 5 },
					{ code: 299, n: 1 },
				]}
			/>,
		);
		expect(codeRows().map((li) => li.textContent)).toEqual([
			expect.stringContaining("200"),
			expect.stringContaining("404"),
			expect.stringContaining("502"),
			expect.stringContaining("299"),
		]);
		const ok = row("200");
		expect(ok).toHaveTextContent("2xx");
		expect(ok).toHaveTextContent("OK");
		expect(ok).toHaveTextContent("1.5k");
		// 1500 of 1526 is 98%.
		expect(ok).toHaveTextContent("98%");
		expect(row("404")).toHaveTextContent("4xx");
		expect(row("404")).toHaveTextContent("Not found");
		expect(row("404")).toHaveTextContent("1.3%");
		expect(row("502")).toHaveTextContent("5xx");
		expect(row("502")).toHaveTextContent("Bad gateway");
		// A code without a known reason still shows its class.
		expect(row("299")).toHaveTextContent("2xx");
	});

	test("code 0 reads 'none' and falls in the other class", () => {
		render(
			<StatusCodeList
				codes={[
					{ code: 200, n: 9 },
					{ code: 0, n: 1 },
				]}
			/>,
		);
		const none = row("none");
		expect(none).toHaveTextContent("other");
		expect(within(none).queryByText("0")).not.toBeInTheDocument();
	});

	test("limit caps the rows but the class bar still counts every response", () => {
		render(
			<StatusCodeList
				limit={1}
				codes={[
					{ code: 200, n: 80 },
					{ code: 500, n: 20 },
				]}
			/>,
		);
		expect(codeRows()).toHaveLength(1);
		expect(screen.queryByText("500")).not.toBeInTheDocument();
		const bar = screen.getByRole("progressbar", {
			name: "Responses by status class",
		});
		const legend = bar.parentElement?.textContent ?? "";
		expect(legend).toContain("2xx");
		expect(legend).toContain("5xx");
		expect(legend).not.toContain("3xx");
	});
});
