import { describe, expect, test } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import { summarizeChecks } from "./lib";
import { Notice, PlanChecks, SecretsNotice } from "./plan-checks";

describe("PlanChecks", () => {
	test("lists failures first, then warnings, then passes, each with its status word", () => {
		const summary = summarizeChecks([
			{ id: "a", status: "ok", message: "Port is free" },
			{ id: "b", status: "fail", message: "Not enough memory" },
			{ id: "c", status: "warn", message: "Image is large" },
		]);
		render(<PlanChecks summary={summary} checking={false} waiting={null} />);
		const rows = within(screen.getByRole("region", { name: "Preflight" }))
			.getAllByRole("listitem")
			.map((row) => row.textContent);
		expect(rows).toEqual([
			"Failed: Not enough memory",
			"Warning: Image is large",
			"Passed: Port is free",
		]);
		expect(screen.getByText("1 failed · 1 to review")).toBeInTheDocument();
	});

	test("says there are no failures when everything passed", () => {
		const summary = summarizeChecks([
			{ id: "a", status: "ok", message: "Fine" },
		]);
		render(<PlanChecks summary={summary} checking={false} waiting={null} />);
		expect(screen.getByText("no failures")).toBeInTheDocument();
	});

	test("an unknown status is shown as a warning", () => {
		render(
			<PlanChecks
				summary={{
					fail: 0,
					warn: 0,
					ok: 0,
					rows: [{ id: "x", status: "weird" as "ok", message: "Odd" }],
				}}
				checking={false}
				waiting={null}
			/>,
		);
		expect(screen.getByText("Warning:")).toBeInTheDocument();
	});

	test("shows a Checking status instead of counts while a preflight runs", () => {
		const summary = summarizeChecks([
			{ id: "a", status: "fail", message: "No" },
		]);
		render(<PlanChecks summary={summary} checking waiting={null} />);
		expect(screen.getByText("Checking")).toBeInTheDocument();
		expect(screen.queryByText("1 failed")).not.toBeInTheDocument();
	});

	test("explains why there are no checks yet", () => {
		render(
			<PlanChecks
				summary={undefined}
				checking={false}
				waiting="Fill in Domain to run the checks."
			/>,
		);
		expect(
			screen.getByText("Fill in Domain to run the checks."),
		).toBeInTheDocument();
		expect(screen.queryByRole("list")).not.toBeInTheDocument();
	});

	test("an empty summary with a waiting reason shows the reason, not an empty list", () => {
		render(
			<PlanChecks
				summary={summarizeChecks([])}
				checking={false}
				waiting="Choose a server."
			/>,
		);
		expect(screen.getByText("Choose a server.")).toBeInTheDocument();
		expect(screen.queryByRole("list")).not.toBeInTheDocument();
	});
});

describe("SecretsNotice", () => {
	test("renders nothing for no secrets", () => {
		const { container } = render(<SecretsNotice secrets={[]} />);
		expect(container).toBeEmptyDOMElement();
	});

	test("counts the secrets", () => {
		render(<SecretsNotice secrets={["A", "B"]} />);
		expect(
			screen.getByText(/2 secrets will be generated or stored encrypted/),
		).toBeInTheDocument();
	});

	test("uses the singular for one secret", () => {
		render(<SecretsNotice secrets={["A"]} />);
		expect(screen.getByText(/^1 secret will be/)).toBeInTheDocument();
	});
});

describe("Notice", () => {
	test("is a note with a title and its body", () => {
		render(<Notice title="Opens public ports">Proxy restarts.</Notice>);
		const note = screen.getByRole("note");
		expect(within(note).getByText("Opens public ports")).toBeInTheDocument();
		expect(within(note).getByText("Proxy restarts.")).toBeInTheDocument();
	});
});
