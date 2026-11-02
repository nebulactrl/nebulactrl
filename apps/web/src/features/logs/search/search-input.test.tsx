import { describe, expect, mock, test } from "bun:test";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { FacetStats } from "./facets";
import { parseLine } from "./line";
import { parseQuery } from "./query";
import { SearchInput } from "./search-input";

function makeStats() {
	const stats = new FacetStats();
	for (const [line, serviceName] of [
		["ERROR boom", "api"],
		["ERROR bang", "api"],
		["INFO ok", "web"],
	] as const) {
		stats.add(parseLine({ line, serviceName }));
	}
	return stats;
}

function Box({
	initial = "",
	recent = [],
	onSubmit,
	error,
	hotkey,
}: {
	initial?: string;
	recent?: string[];
	onSubmit?: () => void;
	error?: string;
	hotkey?: boolean;
}) {
	const [value, setValue] = useState(initial);
	const [stats] = useState(makeStats);
	return (
		<div>
			<output aria-label="value">{value}</output>
			<SearchInput
				label="Search logs"
				value={value}
				onChange={setValue}
				parsed={parseQuery(value)}
				error={error}
				stats={stats}
				statsVersion={1}
				recent={recent}
				onSubmit={onSubmit ?? (() => {})}
				{...(hotkey === undefined ? {} : { hotkey })}
			/>
		</div>
	);
}

const box = () => screen.getByRole("combobox", { name: "Search logs" });
const value = () => screen.getByLabelText("value").textContent;
const options = () =>
	screen.queryAllByRole("option").map((o) => o.textContent ?? "");

describe("SearchInput", () => {
	test("typing reports the new text", async () => {
		const user = userEvent.setup();
		render(<Box />);
		await user.type(box(), "timeout");
		expect(value()).toBe("timeout");
		expect(box()).toHaveValue("timeout");
	});

	test("focusing opens facet-key suggestions and blurring closes them and submits", async () => {
		const user = userEvent.setup();
		const onSubmit = mock();
		render(<Box onSubmit={onSubmit} />);
		expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
		await user.click(box());
		expect(box()).toHaveAttribute("aria-expanded", "true");
		expect(options().some((o) => o.includes("level:"))).toBe(true);
		await user.click(document.body);
		expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
		expect(box()).toHaveAttribute("aria-expanded", "false");
		expect(onSubmit).toHaveBeenCalledTimes(1);
	});

	test("an empty box offers the recent queries first", async () => {
		const user = userEvent.setup();
		render(<Box recent={["level:error", "timeout"]} />);
		await user.click(box());
		expect(options()[0]).toContain("level:error");
		expect(options()[1]).toContain("timeout");
	});

	test("arrow keys move the active option and wrap around", async () => {
		const user = userEvent.setup();
		render(<Box />);
		await user.click(box());
		const count = options().length;
		expect(count).toBeGreaterThan(1);
		await user.keyboard("{ArrowDown}");
		expect(screen.getAllByRole("option")[0]).toHaveAttribute(
			"aria-selected",
			"true",
		);
		expect(box()).toHaveAttribute(
			"aria-activedescendant",
			screen.getAllByRole("option")[0]?.id ?? "",
		);
		await user.keyboard("{ArrowUp}");
		expect(screen.getAllByRole("option")[count - 1]).toHaveAttribute(
			"aria-selected",
			"true",
		);
		await user.keyboard("{ArrowDown}");
		expect(screen.getAllByRole("option")[0]).toHaveAttribute(
			"aria-selected",
			"true",
		);
	});

	test("ArrowDown on a closed list opens it", async () => {
		const user = userEvent.setup();
		render(<Box />);
		await user.click(box());
		await user.keyboard("{Escape}");
		expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
		await user.keyboard("{ArrowDown}");
		expect(screen.getByRole("listbox")).toBeInTheDocument();
	});

	test("Enter accepts the active suggestion, completing the term", async () => {
		const user = userEvent.setup();
		render(<Box />);
		await user.click(box());
		await user.type(box(), "lev");
		const first = options()[0] ?? "";
		expect(first).toContain("level:");
		await user.keyboard("{ArrowDown}{Enter}");
		expect(value()).toBe("level:");
		// A key leads on to its values.
		expect(options().some((o) => o.includes("error"))).toBe(true);
	});

	test("Tab accepts the first suggestion without moving focus", async () => {
		const user = userEvent.setup();
		render(<Box />);
		await user.click(box());
		await user.type(box(), "lev");
		await user.tab();
		expect(value()).toBe("level:");
		expect(box()).toHaveFocus();
	});

	test("Shift+Tab leaves the box and does not accept a suggestion", async () => {
		const user = userEvent.setup();
		render(<Box />);
		await user.click(box());
		await user.type(box(), "lev");
		await user.tab({ shift: true });
		expect(value()).toBe("lev");
		expect(box()).not.toHaveFocus();
	});

	test("clicking a suggestion applies it", async () => {
		const user = userEvent.setup();
		render(<Box />);
		await user.click(box());
		await user.type(box(), "level:");
		const option = screen
			.getAllByRole("option")
			.find((o) => o.textContent?.includes("error"));
		expect(option).toBeDefined();
		await user.click(option as HTMLElement);
		expect(value()).toBe("level:error ");
	});

	test("choosing a recent query puts the whole search in the box and closes the list", async () => {
		const user = userEvent.setup();
		render(<Box recent={["service:api timeout"]} />);
		await user.click(box());
		await user.click(screen.getAllByRole("option")[0] as HTMLElement);
		expect(value()).toBe("service:api timeout");
		expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
	});

	test("Enter with nothing chosen closes the list and submits", async () => {
		const user = userEvent.setup();
		const onSubmit = mock();
		render(<Box initial="timeout" onSubmit={onSubmit} />);
		await user.click(box());
		await user.keyboard("{Enter}");
		expect(onSubmit).toHaveBeenCalledTimes(1);
		expect(value()).toBe("timeout");
		expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
	});

	test("Escape closes the list and stops the key reaching a surrounding panel", async () => {
		const user = userEvent.setup();
		const outer = mock();
		document.addEventListener("keydown", outer);
		try {
			render(<Box />);
			await user.click(box());
			expect(screen.getByRole("listbox")).toBeInTheDocument();
			fireEvent.keyDown(box(), { key: "Escape" });
			expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
			expect(outer).not.toHaveBeenCalled();
			// With the list already closed, Escape passes through.
			fireEvent.keyDown(box(), { key: "Escape" });
			expect(outer).toHaveBeenCalledTimes(1);
		} finally {
			document.removeEventListener("keydown", outer);
		}
	});

	test("the clear button empties the box and keeps focus there", async () => {
		const user = userEvent.setup();
		render(<Box initial="level:error" />);
		await user.click(screen.getByRole("button", { name: "Clear search" }));
		expect(value()).toBe("");
		expect(box()).toHaveFocus();
		expect(
			screen.queryByRole("button", { name: "Clear search" }),
		).not.toBeInTheDocument();
	});

	test("an empty box hints at the / shortcut until it has focus", async () => {
		const user = userEvent.setup();
		render(<Box />);
		expect(screen.getByText("/")).toBeInTheDocument();
		await user.click(box());
		expect(screen.queryByText("/")).not.toBeInTheDocument();
	});

	test("pressing / outside a field focuses the box", async () => {
		const user = userEvent.setup();
		render(<Box />);
		expect(box()).not.toHaveFocus();
		await user.keyboard("/");
		expect(box()).toHaveFocus();
		// The slash is the shortcut, not text.
		expect(value()).toBe("");
	});

	test("/ typed inside the box is text", async () => {
		const user = userEvent.setup();
		render(<Box />);
		await user.click(box());
		await user.type(box(), "a/b");
		expect(value()).toBe("a/b");
	});

	test("with hotkey off, / does nothing and no hint is drawn", async () => {
		const user = userEvent.setup();
		render(<Box hotkey={false} />);
		expect(screen.queryByText("/")).not.toBeInTheDocument();
		await user.keyboard("/");
		expect(box()).not.toHaveFocus();
	});

	test("an error is described by the box and marks it invalid", () => {
		render(<Box initial="status:>" error="Expected a number after >" />);
		const message = screen.getByText("Expected a number after >");
		expect(box()).toHaveAttribute("aria-invalid", "true");
		expect(box().getAttribute("aria-describedby")).toBe(message.id);
	});

	test("without an error the box is not marked invalid", () => {
		render(<Box />);
		expect(box()).not.toHaveAttribute("aria-invalid");
		expect(box()).not.toHaveAttribute("aria-describedby");
	});
});
