import { describe, expect, test } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import { ComposeDiff } from "./compose-diff";
import { diffLines } from "./diff";

describe("ComposeDiff", () => {
	test("labels the diff with the number of added and removed lines", () => {
		render(<ComposeDiff diff={diffLines("a\nb\nc\n", "a\nB\nc\nd\n")} />);
		expect(
			screen.getByRole("region", {
				name: "Compose file changes: 2 added, 1 removed",
			}),
		).toBeInTheDocument();
	});

	test("says added, removed and unchanged in words for every line", () => {
		const { container } = render(
			<ComposeDiff diff={diffLines("keep\nold\n", "keep\nnew\n")} />,
		);
		const rows = [...container.querySelectorAll("[data-kind]")].map((row) => [
			row.getAttribute("data-kind"),
			row.textContent,
		]);
		expect(rows).toEqual([
			["unchanged", " unchangedkeep"],
			["removed", "−removedold"],
			["added", "+addednew"],
		]);
	});

	test("folds long unchanged runs into a count, keeping two lines of context", () => {
		const body = Array.from({ length: 12 }, (_, i) => `line${i}`);
		const before = [...body, "tail-old"].join("\n");
		const after = [...body, "tail-new"].join("\n");
		render(<ComposeDiff diff={diffLines(before, after)} />);
		const region = screen.getByRole("region");
		expect(within(region).getByText("10 unchanged lines")).toBeInTheDocument();
		expect(within(region).getByText("line10")).toBeInTheDocument();
		expect(within(region).getByText("line11")).toBeInTheDocument();
		expect(within(region).queryByText("line9")).not.toBeInTheDocument();
		expect(within(region).getByText("tail-old")).toBeInTheDocument();
		expect(within(region).getByText("tail-new")).toBeInTheDocument();
	});

	test("uses the singular for a folded gap of one line", () => {
		const before = ["a", "b", "c", "d", "e", "f", "g"].join("\n");
		const after = ["A", "b", "c", "d", "e", "f", "G"].join("\n");
		render(<ComposeDiff diff={diffLines(before, after)} />);
		// 5 unchanged lines in the middle, two of context on each side leave 1 hidden.
		expect(screen.getByText("1 unchanged line")).toBeInTheDocument();
	});

	test("an identical file has nothing added or removed", () => {
		render(<ComposeDiff diff={diffLines("a\n", "a\n")} />);
		expect(
			screen.getByRole("region", {
				name: "Compose file changes: 0 added, 0 removed",
			}),
		).toBeInTheDocument();
	});

	test("an added blank line still takes up a visible row", () => {
		const { container } = render(
			<ComposeDiff diff={diffLines("a\nb\n", "a\n\nb\n")} />,
		);
		const added = container.querySelector('[data-kind="added"]');
		expect(added).not.toBeNull();
		expect(added?.textContent).toBe("+added ");
	});
});
