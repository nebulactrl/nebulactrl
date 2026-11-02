import { describe, expect, mock, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { CanvasSearch } from "./search-bar";

/** A search box whose query lives in the parent, as in the canvas. */
function Host() {
	const [query, setQuery] = useState("");
	return (
		<CanvasSearch
			query={query}
			onQueryChange={setQuery}
			matchCount={2}
			total={9}
			inline
			rightInset={0}
			onSubmit={() => {}}
			onClose={() => {}}
		/>
	);
}

function setup(props: Partial<Parameters<typeof CanvasSearch>[0]> = {}) {
	const onQueryChange = mock((_q: string) => {});
	const onSubmit = mock(() => {});
	const onClose = mock(() => {});
	const view = render(
		<CanvasSearch
			query=""
			onQueryChange={onQueryChange}
			matchCount={0}
			total={5}
			inline={false}
			rightInset={0}
			onSubmit={onSubmit}
			onClose={onClose}
			{...props}
		/>,
	);
	return { onQueryChange, onSubmit, onClose, ...view };
}

describe("CanvasSearch", () => {
	test("takes focus on open and selects the current query so typing replaces it", () => {
		setup({ query: "web" });
		const input = screen.getByRole("searchbox", { name: "Search services" });
		expect(input).toHaveFocus();
		expect((input as HTMLInputElement).selectionStart).toBe(0);
		expect((input as HTMLInputElement).selectionEnd).toBe(3);
	});

	test("typing reports each new query", async () => {
		const user = userEvent.setup();
		const { onQueryChange } = setup();
		await user.keyboard("db");
		expect(onQueryChange.mock.calls.map((c) => c[0])).toEqual(["d", "b"]);
	});

	test("typing builds up the query when the parent holds the state", async () => {
		const user = userEvent.setup();
		render(<Host />);
		await user.keyboard("redis");
		expect(screen.getByRole("searchbox")).toHaveValue("redis");
		expect(screen.getByText("2 of 9")).toBeInTheDocument();
	});

	test("Enter submits and Escape closes, without leaking the key to the canvas", async () => {
		const user = userEvent.setup();
		const outer = mock(() => {});
		const { onSubmit, onClose } = setup();
		document.addEventListener("keydown", outer);
		try {
			await user.keyboard("{Enter}");
			expect(onSubmit).toHaveBeenCalledTimes(1);
			expect(onClose).not.toHaveBeenCalled();
			await user.keyboard("{Escape}");
			expect(onClose).toHaveBeenCalledTimes(1);
			expect(onSubmit).toHaveBeenCalledTimes(1);
			await user.keyboard("a");
			// A bubbling listener on the document (the canvas's shortcuts) never sees keys typed here.
			expect(outer).not.toHaveBeenCalled();
		} finally {
			document.removeEventListener("keydown", outer);
		}
	});

	test("the close button closes", async () => {
		const user = userEvent.setup();
		const { onClose } = setup();
		await user.click(screen.getByRole("button", { name: "Close search" }));
		expect(onClose).toHaveBeenCalledTimes(1);
	});

	test("an empty query shows the esc hint and no count", () => {
		setup({ query: "   ", matchCount: 3, total: 5 });
		expect(screen.getByText("esc")).toBeInTheDocument();
		expect(screen.queryByText(/of 5/)).not.toBeInTheDocument();
	});

	test("a typed query shows the match count in a live region instead of the hint", () => {
		setup({ query: "web", matchCount: 3, total: 5 });
		const count = screen.getByText("3 of 5");
		expect(count).toHaveAttribute("aria-live", "polite");
		expect(screen.queryByText("esc")).not.toBeInTheDocument();
	});

	test("floating, it centres in the canvas the inspector leaves free", () => {
		const { container } = setup({ rightInset: 400, inline: false });
		const wrapper = container.firstElementChild as HTMLElement;
		expect(wrapper.style.right).toBe("412px");
		expect(wrapper).toContainElement(screen.getByRole("searchbox"));
		expect(wrapper.firstElementChild?.tagName).toBe("SEARCH");
	});

	test("inline, it renders the field alone, without the floating wrapper", () => {
		const { container } = setup({ inline: true, rightInset: 400 });
		expect(container.firstElementChild?.tagName).toBe("SEARCH");
		expect(container.firstElementChild).toContainElement(
			screen.getByRole("searchbox"),
		);
	});
});
