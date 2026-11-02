import { describe, expect, mock, test } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { BarList } from "./bar-list";

const items = [
	{ k: "/small", n: 5 },
	{ k: "/big", n: 90, s5xx: 3, avgMs: 120 },
	{ k: "/zero", n: 0 },
	{ k: "", n: 5 },
];

function rowTexts() {
	return screen.getAllByRole("listitem").map((li) => li.textContent ?? "");
}

describe("BarList", () => {
	test("ranks rows by count, drops empty ones and shows count and share", () => {
		render(<BarList items={items} />);
		const rows = rowTexts();
		expect(rows).toHaveLength(3);
		expect(rows[0]).toContain("/big");
		expect(rows[0]).toContain("90");
		expect(rows[0]).toContain("90%");
		// 5 of 100 is under 10%, so it keeps one decimal.
		expect(rows[1]).toContain("5.0%");
		expect(screen.queryByText("/zero")).not.toBeInTheDocument();
	});

	test("labels the row with an empty key by blankLabel", () => {
		const { rerender } = render(<BarList items={items} />);
		expect(screen.getByText("(none)")).toBeInTheDocument();
		rerender(<BarList items={items} blankLabel="Direct / none" />);
		expect(screen.getByText("Direct / none")).toBeInTheDocument();
		expect(screen.queryByText("(none)")).not.toBeInTheDocument();
	});

	test("limit caps the rows shown", () => {
		render(<BarList items={items} limit={1} />);
		expect(rowTexts()).toHaveLength(1);
		expect(screen.getByText("/big")).toBeInTheDocument();
		expect(screen.queryByText("/small")).not.toBeInTheDocument();
	});

	test("an empty or missing list shows the default or the given message", () => {
		const { rerender } = render(<BarList items={null} />);
		expect(screen.getByText("Nothing in this range.")).toBeInTheDocument();
		rerender(<BarList items={[{ k: "/x", n: 0 }]} empty="No 404s here." />);
		expect(screen.getByText("No 404s here.")).toBeInTheDocument();
		expect(screen.queryByRole("list")).not.toBeInTheDocument();
	});

	test("detail adds the 5xx count and average latency to rows that have them", () => {
		const { rerender } = render(<BarList items={items} />);
		expect(screen.queryByText("3 5xx")).not.toBeInTheDocument();
		expect(screen.queryByText("avg 120 ms")).not.toBeInTheDocument();
		rerender(<BarList items={items} detail />);
		const big = screen.getByText("/big").closest("li");
		expect(big).not.toBeNull();
		expect(within(big as HTMLElement).getByText("3 5xx")).toBeInTheDocument();
		expect(
			within(big as HTMLElement).getByText("avg 120 ms"),
		).toBeInTheDocument();
		const small = screen.getByText("/small").closest("li") as HTMLElement;
		expect(within(small).queryByText(/5xx/)).not.toBeInTheDocument();
		expect(within(small).queryByText(/avg/)).not.toBeInTheDocument();
	});

	test("rows are plain text without onSelect, buttons that report the raw key with it", async () => {
		const { rerender } = render(<BarList items={items} />);
		expect(screen.queryByRole("button")).not.toBeInTheDocument();
		const onSelect = mock((_key: string) => {});
		rerender(<BarList items={items} onSelect={onSelect} />);
		const user = userEvent.setup();
		await user.click(screen.getByRole("button", { name: /\/big/ }));
		// The blank row reports "" (the real key), not its display label.
		await user.click(screen.getByRole("button", { name: /\(none\)/ }));
		expect(onSelect.mock.calls.map((c) => c[0])).toEqual(["/big", ""]);
	});
});
