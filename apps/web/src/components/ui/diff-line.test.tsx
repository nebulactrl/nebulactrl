import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DiffLine } from "./diff-line";

const line = () =>
	document.querySelector("[data-slot=diff-line]") as HTMLElement;

describe("DiffLine", () => {
	test.each([
		["+", "added"],
		["−", "removed"],
		["~", "changed"],
	] as const)("sign %s is shown visually and spoken as %s", (sign, name) => {
		render(<DiffLine sign={sign}>KEY=value</DiffLine>);
		expect(line()).toHaveAttribute("data-sign", name);
		expect(screen.getByText(sign)).toHaveAttribute("aria-hidden", "true");
		expect(screen.getByText(name)).toHaveClass("sr-only");
		expect(screen.getByText("KEY=value")).toBeInTheDocument();
	});

	test("renders a label alone as a single span", () => {
		render(<DiffLine sign="+">replicas</DiffLine>);
		const label = screen.getByText("replicas");
		expect(label.parentElement).toBe(line());
		expect(line().querySelector("[title]")).toBeNull();
	});

	test("with a value, label and value are separate and the value carries a title", () => {
		render(
			<DiffLine sign="~" value="registry.example/app:2">
				image
			</DiffLine>,
		);
		const label = screen.getByText("image");
		const value = screen.getByText("registry.example/app:2");
		expect(label).not.toBe(value);
		expect(label.parentElement).toBe(value.parentElement);
		expect(value).toHaveAttribute("title", "registry.example/app:2");
	});

	test("an empty-string value still renders the split layout", () => {
		render(
			<DiffLine sign="+" value="">
				EMPTY
			</DiffLine>,
		);
		const label = screen.getByText("EMPTY");
		expect(label.nextElementSibling).toHaveAttribute("title", "");
	});

	test("a multi-line value is shown whole with no title tooltip", () => {
		render(
			<DiffLine sign="+" value={"-----BEGIN-----\nabc\n-----END-----"}>
				CERT
			</DiffLine>,
		);
		const value = document.querySelector(
			"[data-slot=diff-line] span.whitespace-pre-wrap",
		) as HTMLElement;
		expect(value.textContent).toBe("-----BEGIN-----\nabc\n-----END-----");
		expect(value).not.toHaveAttribute("title");
		expect(line()).toHaveClass("items-start");
	});

	test("a multi-line label keeps its breaks and top-aligns the row", () => {
		render(<DiffLine sign="+">{"line one\nline two"}</DiffLine>);
		expect(screen.getByText(/line one/)).toHaveClass("whitespace-pre-wrap");
		expect(line()).toHaveClass("items-start");
	});

	test("a single-line row is centre-aligned", () => {
		render(<DiffLine sign="+">one</DiffLine>);
		expect(line()).toHaveClass("items-center");
		expect(line()).not.toHaveClass("items-start");
	});

	test("a CRLF in the value counts as multi-line", () => {
		render(
			<DiffLine sign="+" value={"a\r\nb"}>
				K
			</DiffLine>,
		);
		expect(line()).toHaveClass("items-start");
	});

	test("a ReactNode label is never treated as multi-line", () => {
		render(
			<DiffLine sign="+">
				<b>bold</b>
			</DiffLine>,
		);
		expect(line()).toHaveClass("items-center");
	});

	test("wrap lets a long label stop truncating", () => {
		const { rerender } = render(<DiffLine sign="+">some label</DiffLine>);
		expect(screen.getByText("some label")).toHaveClass("truncate");
		rerender(
			<DiffLine sign="+" wrap>
				some label
			</DiffLine>,
		);
		expect(screen.getByText("some label")).not.toHaveClass("truncate");
	});

	test("wrap stops the value from truncating too", () => {
		const { rerender } = render(
			<DiffLine sign="+" value="v">
				k
			</DiffLine>,
		);
		expect(screen.getByText("v")).toHaveClass("truncate");
		rerender(
			<DiffLine sign="+" value="v" wrap>
				k
			</DiffLine>,
		);
		expect(screen.getByText("v")).not.toHaveClass("truncate");
		expect(screen.getByText("k")).not.toHaveClass("truncate");
	});

	test("the note is shown apart from the line only when given", () => {
		const { rerender } = render(
			<DiffLine sign="+" note="set by nebula.toml">
				KEY
			</DiffLine>,
		);
		expect(screen.getByText("set by nebula.toml")).toBeInTheDocument();
		rerender(<DiffLine sign="+">KEY</DiffLine>);
		expect(screen.queryByText("set by nebula.toml")).toBeNull();
	});

	test("no undo button without onUndo", () => {
		render(<DiffLine sign="+">KEY</DiffLine>);
		expect(screen.queryByRole("button")).toBeNull();
	});

	test("the undo button calls onUndo once per click", async () => {
		const user = userEvent.setup();
		const calls: number[] = [];
		render(
			<DiffLine sign="−" onUndo={() => calls.push(1)}>
				KEY
			</DiffLine>,
		);
		const button = screen.getByRole("button", { name: "undo" });
		expect(button).toHaveAttribute("type", "button");
		await user.click(button);
		expect(calls).toHaveLength(1);
		await user.click(button);
		expect(calls).toHaveLength(2);
	});

	test("the undo button is keyboard operable", async () => {
		const user = userEvent.setup();
		let n = 0;
		render(
			<DiffLine sign="−" onUndo={() => n++}>
				KEY
			</DiffLine>,
		);
		await user.tab();
		expect(screen.getByRole("button", { name: "undo" })).toHaveFocus();
		await user.keyboard("{Enter}");
		expect(n).toBe(1);
	});

	test("tinted and plain variants style the row differently, per sign", () => {
		const cls = (variant: "plain" | "tinted", sign: "+" | "−" | "~") => {
			const { unmount } = render(
				<DiffLine sign={sign} variant={variant}>
					x
				</DiffLine>,
			);
			const c = line().className;
			unmount();
			return c;
		};
		expect(cls("plain", "+")).not.toBe(cls("tinted", "+"));
		expect(
			new Set(["+", "−", "~"].map((s) => cls("tinted", s as "+"))).size,
		).toBe(3);
	});

	test("plain colours only the sign, not the row", () => {
		render(<DiffLine sign="+">text</DiffLine>);
		const signCell = screen.getByText("+").parentElement as HTMLElement;
		expect(signCell.className).toContain("text-ok");
		expect(line().className).not.toContain("status-live");
	});

	test("tinted does not recolour the sign cell separately", () => {
		render(
			<DiffLine sign="+" variant="tinted">
				text
			</DiffLine>,
		);
		const signCell = screen.getByText("+").parentElement as HTMLElement;
		expect(signCell.className).not.toContain("text-ok");
	});

	test("className is merged onto the row", () => {
		render(
			<DiffLine sign="+" className="my-1">
				x
			</DiffLine>,
		);
		expect(line()).toHaveClass("my-1");
	});
});
