import { describe, expect, it } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { FormErrors, HeaderRow } from "#/features/proxy/lib/options";
import { renderWithProviders } from "#/test/render";
import { HeaderRowsEditor } from "./lists";

/** Holds the rows like the real group does and mirrors them into `seen`. */
function Harness({
	initial,
	errors = {},
	errorKey = "requestHeaders",
	label = "Request header",
	seen,
}: {
	initial: HeaderRow[];
	errors?: FormErrors;
	errorKey?: "requestHeaders" | "responseHeaders";
	label?: string;
	seen: { rows: HeaderRow[] };
}) {
	const [rows, setRows] = useState(initial);
	seen.rows = rows;
	return (
		<HeaderRowsEditor
			label={label}
			rows={rows}
			errors={errors}
			errorKey={errorKey}
			onChange={setRows}
		/>
	);
}

describe("HeaderRowsEditor", () => {
	it("renders one named name/value pair per row and nothing when empty", async () => {
		const seen = { rows: [] as HeaderRow[] };
		await renderWithProviders(
			<Harness
				seen={seen}
				initial={[
					{ name: "X-Env", value: "prod" },
					{ name: "X-Team", value: "core" },
				]}
			/>,
		);
		const list = screen.getByRole("list", { name: "Request header" });
		expect(within(list).getAllByRole("listitem")).toHaveLength(2);
		expect(screen.getByLabelText("Request header name 1")).toHaveValue("X-Env");
		expect(screen.getByLabelText("Request header value 1")).toHaveValue("prod");
		expect(screen.getByLabelText("Request header name 2")).toHaveValue(
			"X-Team",
		);
		expect(screen.getByLabelText("Request header value 2")).toHaveValue("core");
	});

	it("shows only the add button for an empty list", async () => {
		const seen = { rows: [] as HeaderRow[] };
		await renderWithProviders(<Harness seen={seen} initial={[]} />);
		expect(screen.queryAllByRole("listitem")).toHaveLength(0);
		expect(screen.getByRole("button", { name: "Add header" })).toBeEnabled();
	});

	it("adds a blank row, appended after the existing ones", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = { rows: [] as HeaderRow[] };
		await renderWithProviders(
			<Harness seen={seen} initial={[{ name: "X-A", value: "1" }]} />,
		);
		await user.click(screen.getByRole("button", { name: "Add header" }));
		expect(seen.rows).toEqual([
			{ name: "X-A", value: "1" },
			{ name: "", value: "" },
		]);
		expect(screen.getByLabelText("Request header name 2")).toHaveValue("");
	});

	it("edits the name and the value of just the row being typed in", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = { rows: [] as HeaderRow[] };
		await renderWithProviders(
			<Harness
				seen={seen}
				initial={[
					{ name: "X-A", value: "1" },
					{ name: "X-B", value: "2" },
				]}
			/>,
		);
		await user.type(screen.getByLabelText("Request header name 2"), "x");
		await user.clear(screen.getByLabelText("Request header value 1"));
		await user.type(screen.getByLabelText("Request header value 1"), "one");
		expect(seen.rows).toEqual([
			{ name: "X-A", value: "one" },
			{ name: "X-Bx", value: "2" },
		]);
	});

	it("removes only the chosen row and renumbers the labels", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = { rows: [] as HeaderRow[] };
		await renderWithProviders(
			<Harness
				seen={seen}
				initial={[
					{ name: "X-A", value: "1" },
					{ name: "X-B", value: "2" },
					{ name: "X-C", value: "3" },
				]}
			/>,
		);
		await user.click(
			screen.getByRole("button", { name: "Remove Request header 2" }),
		);
		expect(seen.rows.map((r) => r.name)).toEqual(["X-A", "X-C"]);
		expect(screen.getByLabelText("Request header name 2")).toHaveValue("X-C");
		expect(screen.queryByLabelText("Request header name 3")).toBeNull();
	});

	it("marks the invalid field and shows the name error before the value error", async () => {
		const seen = { rows: [] as HeaderRow[] };
		await renderWithProviders(
			<Harness
				seen={seen}
				initial={[
					{ name: "bad name", value: "ok" },
					{ name: "X-Fine", value: "bad\nvalue" },
					{ name: "X-Both", value: "x" },
				]}
				errors={{
					"requestHeaders.0.name": "Not a valid header name.",
					"requestHeaders.1.value": "Too long, or contains a line break.",
					"requestHeaders.2.name": "name problem",
					"requestHeaders.2.value": "value problem",
				}}
			/>,
		);
		expect(screen.getByLabelText("Request header name 1")).toBeInvalid();
		expect(screen.getByLabelText("Request header value 1")).toBeValid();
		expect(screen.getByText("Not a valid header name.")).toBeInTheDocument();
		expect(screen.getByLabelText("Request header name 2")).toBeValid();
		expect(screen.getByLabelText("Request header value 2")).toBeInvalid();
		expect(
			screen.getByText("Too long, or contains a line break."),
		).toBeInTheDocument();
		expect(screen.getByText("name problem")).toBeInTheDocument();
		expect(screen.queryByText("value problem")).toBeNull();
	});

	it("shows the list-level error under the rows", async () => {
		const seen = { rows: [] as HeaderRow[] };
		await renderWithProviders(
			<Harness
				seen={seen}
				initial={[{ name: "X-A", value: "1" }]}
				errorKey="responseHeaders"
				label="Response header"
				errors={{ responseHeaders: "At most 30 headers." }}
			/>,
		);
		expect(screen.getByText("At most 30 headers.")).toBeInTheDocument();
	});

	it("reads only errors of its own key", async () => {
		const seen = { rows: [] as HeaderRow[] };
		await renderWithProviders(
			<Harness
				seen={seen}
				initial={[{ name: "X-A", value: "1" }]}
				errors={{
					"responseHeaders.0.name": "response only",
					responseHeaders: "response list",
				}}
			/>,
		);
		expect(screen.queryByText("response only")).toBeNull();
		expect(screen.queryByText("response list")).toBeNull();
		expect(screen.getByLabelText("Request header name 1")).toBeValid();
	});
});
