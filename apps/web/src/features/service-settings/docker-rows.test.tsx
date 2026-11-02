import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { DockerIssue } from "./docker-options";
import {
	AddRowButton,
	issuesFor,
	patchRow,
	RowShell,
	StringList,
} from "./docker-rows";

describe("issuesFor", () => {
	const issues: DockerIssue[] = [
		{ field: "dns", message: "At most 3 DNS servers." },
		{ field: "dns", message: "1.1.1.1 is listed twice.", row: 2 },
		{ field: "dns", message: "Bad address.", row: 0 },
		{ field: "labels", message: "A label problem.", row: 0 },
		{ field: "labels", message: "Too many labels." },
	];

	test("separates the list-wide problem from the per-row ones of one field", () => {
		const dns = issuesFor(issues, "dns");
		expect(dns.listWide).toBe("At most 3 DNS servers.");
		expect(dns.row(0)).toBe("Bad address.");
		expect(dns.row(2)).toBe("1.1.1.1 is listed twice.");
		expect(dns.row(1)).toBeUndefined();
	});

	test("does not leak the problems of other fields", () => {
		const labels = issuesFor(issues, "labels");
		expect(labels.listWide).toBe("Too many labels.");
		expect(labels.row(0)).toBe("A label problem.");
		expect(issuesFor(issues, "tmpfs").listWide).toBeUndefined();
		expect(issuesFor(issues, "tmpfs").row(0)).toBeUndefined();
	});

	test("a field with only row problems has no list-wide one", () => {
		expect(
			issuesFor([{ field: "dns", message: "x", row: 1 }], "dns").listWide,
		).toBeUndefined();
	});
});

describe("patchRow", () => {
	test("changes only the named row and keeps the others and the input", () => {
		const rows = [
			{ a: "1", b: "x" },
			{ a: "2", b: "y" },
		];
		const next = patchRow(rows, 1, { b: "z" });
		expect(next).toEqual([
			{ a: "1", b: "x" },
			{ a: "2", b: "z" },
		]);
		expect(next[0]).toBe(rows[0]);
		expect(rows[1]).toEqual({ a: "2", b: "y" });
	});

	test("an index past the end changes nothing", () => {
		expect(patchRow([{ a: "1" }], 5, { a: "2" })).toEqual([{ a: "1" }]);
	});
});

describe("RowShell", () => {
	test("shows the error under the row and removes through the labelled key", async () => {
		let removed = 0;
		render(
			<ul>
				<RowShell
					label="device"
					index={1}
					error="Not under /dev."
					onRemove={() => {
						removed += 1;
					}}
				>
					<span>row body</span>
				</RowShell>
			</ul>,
		);
		expect(screen.getByText("row body")).toBeInTheDocument();
		expect(screen.getByText("Not under /dev.")).toBeInTheDocument();
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Remove device 2" }));
		expect(removed).toBe(1);
	});

	test("a disabled row cannot be removed", async () => {
		let removed = 0;
		render(
			<ul>
				<RowShell
					label="device"
					index={0}
					error={undefined}
					disabled
					onRemove={() => {
						removed += 1;
					}}
				>
					<span>row body</span>
				</RowShell>
			</ul>,
		);
		const remove = screen.getByRole("button", { name: "Remove device 1" });
		expect(remove).toBeDisabled();
		await userEvent.setup().click(remove);
		expect(removed).toBe(0);
	});
});

describe("AddRowButton", () => {
	test("calls back on click unless disabled", async () => {
		let added = 0;
		const user = userEvent.setup();
		const { rerender } = render(
			<AddRowButton
				onClick={() => {
					added += 1;
				}}
			>
				Add thing
			</AddRowButton>,
		);
		await user.click(screen.getByRole("button", { name: "Add thing" }));
		expect(added).toBe(1);
		rerender(
			<AddRowButton
				disabled
				onClick={() => {
					added += 1;
				}}
			>
				Add thing
			</AddRowButton>,
		);
		const button = screen.getByRole("button", { name: "Add thing" });
		expect(button).toBeDisabled();
		await user.click(button);
		expect(added).toBe(1);
	});
});

let latest: string[] = [];

function Harness({
	initial,
	issues = { listWide: undefined, row: () => undefined },
	max = 3,
	inputMode,
}: {
	initial: string[];
	issues?: Parameters<typeof StringList>[0]["issues"];
	max?: number;
	inputMode?: "numeric";
}) {
	const [values, setValues] = useState(initial);
	latest = values;
	return (
		<StringList
			label="DNS servers"
			rowLabel="DNS server"
			values={values}
			issues={issues}
			max={max}
			placeholder="1.1.1.1"
			addLabel="Add DNS server"
			{...(inputMode ? { inputMode } : {})}
			onChange={setValues}
		/>
	);
}

describe("StringList", () => {
	test("an empty list renders no rows, only the add key", () => {
		render(<Harness initial={[]} />);
		expect(screen.queryByRole("list")).not.toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Add DNS server" }),
		).toBeEnabled();
	});

	test("adds a blank row, edits it and removes another", async () => {
		const user = userEvent.setup();
		render(<Harness initial={["1.1.1.1", "9.9.9.9"]} />);
		const list = screen.getByRole("list", { name: "DNS servers" });
		expect(list.querySelectorAll("li")).toHaveLength(2);

		await user.click(screen.getByRole("button", { name: "Add DNS server" }));
		expect(latest).toEqual(["1.1.1.1", "9.9.9.9", ""]);

		await user.type(screen.getByLabelText("DNS server 3"), "8.8.8.8");
		expect(latest).toEqual(["1.1.1.1", "9.9.9.9", "8.8.8.8"]);

		await user.click(
			screen.getByRole("button", { name: "Remove DNS server 1" }),
		);
		expect(latest).toEqual(["9.9.9.9", "8.8.8.8"]);
		expect(screen.getByLabelText("DNS server 1")).toHaveValue("9.9.9.9");
	});

	test("editing one row leaves the others alone", async () => {
		const user = userEvent.setup();
		render(<Harness initial={["a", "b"]} />);
		await user.type(screen.getByLabelText("DNS server 2"), "!");
		expect(latest).toEqual(["a", "b!"]);
	});

	test("the add key is disabled at the maximum", () => {
		render(<Harness initial={["a", "b", "c"]} />);
		expect(
			screen.getByRole("button", { name: "Add DNS server" }),
		).toBeDisabled();
	});

	test("shows the list-wide problem and flags only the row with a problem", () => {
		render(
			<Harness
				initial={["1.1.1.1", "bad"]}
				issues={{
					listWide: "At most 3 DNS servers.",
					row: (index) =>
						index === 1 ? "bad is not an IP address." : undefined,
				}}
			/>,
		);
		expect(screen.getByText("At most 3 DNS servers.")).toBeInTheDocument();
		expect(screen.getByText("bad is not an IP address.")).toBeInTheDocument();
		expect(screen.getByLabelText("DNS server 1")).toHaveAttribute(
			"aria-invalid",
			"false",
		);
		expect(screen.getByLabelText("DNS server 2")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
	});

	test("a numeric list asks for the numeric keyboard", () => {
		render(<Harness initial={["109"]} inputMode="numeric" />);
		expect(screen.getByLabelText("DNS server 1")).toHaveAttribute(
			"inputmode",
			"numeric",
		);
	});
});
