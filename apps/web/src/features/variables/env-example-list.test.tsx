import { describe, expect, it } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { SuggestedVariable } from "#/api/gen/types.gen";
import { type ExampleChoices, initialChoices } from "./env-example";
import { EnvExampleList } from "./env-example-list";

const OPEN = "$" + "{{";

function item(over: Partial<SuggestedVariable> = {}): SuggestedVariable {
	return {
		key: "LOG_LEVEL",
		value: "info",
		needsInput: false,
		proposedSecret: false,
		reason: "from .env.example",
		source: "env-example",
		warnings: null,
		...over,
	};
}

const ITEMS: SuggestedVariable[] = [
	item({ key: "LOG_LEVEL", value: "info", comment: "debug, info or warn" }),
	item({ key: "API_TOKEN", value: "", needsInput: true, proposedSecret: true }),
	item({
		key: "SESSION_SECRET",
		value: `${OPEN}secret(32)}}`,
		proposedSecret: true,
	}),
	item({ key: "DATABASE_URL", value: `${OPEN}postgres.DATABASE_URL}}` }),
	item({ key: "SITE_NAME", value: "", needsInput: true }),
	item({ key: "WEIRD", value: "x", warnings: ["looks like a placeholder"] }),
];

/** The list wired to real state, reporting the latest choices. */
function Harness({
	items = ITEMS,
	autoFocus,
	onChange,
}: {
	items?: SuggestedVariable[];
	autoFocus?: boolean;
	onChange?: (choices: ExampleChoices) => void;
}) {
	const [choices, setChoices] = useState(() => initialChoices(items));
	return (
		<EnvExampleList
			idPrefix="t"
			items={items}
			choices={choices}
			autoFocus={autoFocus ?? false}
			onChoices={(next) => {
				setChoices(next);
				onChange?.(next);
			}}
		/>
	);
}

describe("EnvExampleList", () => {
	it("puts the keys that need a value first, in their original order otherwise", () => {
		render(<Harness />);
		const rows = within(
			screen.getByRole("list", { name: "Keys from .env.example" }),
		).getAllByRole("listitem");
		expect(
			rows.map((row) => row.querySelector("label span")?.textContent),
		).toEqual([
			"API_TOKEN",
			"SITE_NAME",
			"LOG_LEVEL",
			"SESSION_SECRET",
			"DATABASE_URL",
			"WEIRD",
		]);
	});

	it("starts every key checked and shows the hint, badges and warnings", () => {
		render(<Harness />);
		for (const entry of ITEMS) {
			expect(
				screen.getByRole("checkbox", { name: `Add ${entry.key}` }),
			).toBeChecked();
		}
		expect(screen.getByText("debug, info or warn")).toBeInTheDocument();
		expect(screen.getAllByText("Needs a value")).toHaveLength(2);
		// API_TOKEN and SESSION_SECRET are proposed as secrets.
		expect(screen.getAllByText("secret")).toHaveLength(2);
		expect(screen.getByText("looks like a placeholder")).toBeInTheDocument();
	});

	it("describes the proposed value: literal, generated or a reference", () => {
		render(<Harness />);
		expect(screen.getByText("info")).toHaveAttribute("title", "info");
		expect(
			screen.getByText(/A random value is generated when you add it/),
		).toBeInTheDocument();
		const reference = screen.getByText("postgres.DATABASE_URL");
		expect(reference.closest("p")).toHaveTextContent(
			"Points at postgres.DATABASE_URL.",
		);
	});

	it("shows an input only for keys that need a value; secrets are masked", () => {
		render(<Harness />);
		const token = screen.getByLabelText("Value for API_TOKEN");
		expect(token).toHaveAttribute("type", "password");
		expect(token).toHaveAttribute("autocomplete", "new-password");
		const site = screen.getByLabelText("Value for SITE_NAME");
		expect(site).toHaveAttribute("type", "text");
		expect(screen.queryByLabelText("Value for LOG_LEVEL")).toBeNull();
	});

	it("typing a value records it and keeps the key checked", async () => {
		const user = userEvent.setup();
		let latest: ExampleChoices = {};
		render(<Harness onChange={(next) => (latest = next)} />);
		await user.type(screen.getByLabelText("Value for SITE_NAME"), "Shop");
		expect(latest.SITE_NAME).toEqual({ checked: true, value: "Shop" });
		expect(latest.LOG_LEVEL).toEqual({ checked: true, value: "info" });
		expect(screen.getByLabelText("Value for SITE_NAME")).toHaveValue("Shop");
	});

	it("unchecking a key records it and disables that key's input only", async () => {
		const user = userEvent.setup();
		let latest: ExampleChoices = {};
		render(<Harness onChange={(next) => (latest = next)} />);
		await user.click(screen.getByRole("checkbox", { name: "Add SITE_NAME" }));
		expect(latest.SITE_NAME?.checked).toBe(false);
		expect(screen.getByLabelText("Value for SITE_NAME")).toBeDisabled();
		expect(screen.getByLabelText("Value for API_TOKEN")).toBeEnabled();
		await user.click(screen.getByRole("checkbox", { name: "Add SITE_NAME" }));
		expect(latest.SITE_NAME?.checked).toBe(true);
		expect(screen.getByLabelText("Value for SITE_NAME")).toBeEnabled();
	});

	it("clicking a plain key's name toggles its checkbox; a needing key's name focuses its input", async () => {
		const user = userEvent.setup();
		render(<Harness />);
		await user.click(screen.getByText("LOG_LEVEL"));
		expect(
			screen.getByRole("checkbox", { name: "Add LOG_LEVEL" }),
		).not.toBeChecked();
		await user.click(screen.getByText("SITE_NAME"));
		expect(screen.getByLabelText("Value for SITE_NAME")).toHaveFocus();
	});

	it("focuses the first key that needs a value when asked", () => {
		render(<Harness autoFocus />);
		expect(screen.getByLabelText("Value for API_TOKEN")).toHaveFocus();
	});

	it("does not move focus by default", () => {
		render(<Harness />);
		expect(screen.getByLabelText("Value for API_TOKEN")).not.toHaveFocus();
	});

	it("an empty list renders no rows", () => {
		render(<Harness items={[]} />);
		expect(
			within(
				screen.getByRole("list", { name: "Keys from .env.example" }),
			).queryAllByRole("listitem"),
		).toEqual([]);
	});
});
