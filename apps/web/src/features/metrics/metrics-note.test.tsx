import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { MetricsNote, metaOf } from "./metrics-note";

describe("metaOf", () => {
	test("copies the step and retention the response reports", () => {
		expect(metaOf({ stepSeconds: 60, retentionDays: 7 })).toEqual({
			stepSeconds: 60,
			retentionDays: 7,
		});
	});

	test("zero means not reported and is left out", () => {
		expect(metaOf({ stepSeconds: 0, retentionDays: 0 })).toEqual({});
		expect(metaOf({ stepSeconds: 10, retentionDays: 0 })).toEqual({
			stepSeconds: 10,
		});
		expect(metaOf(undefined)).toEqual({});
	});
});

describe("MetricsNote", () => {
	test("renders nothing when the response reported neither value", () => {
		const { container } = render(<MetricsNote meta={{}} range="1h" />);
		expect(container).toBeEmptyDOMElement();
	});

	test("says how long history is kept and the sampling step", () => {
		render(
			<MetricsNote meta={{ retentionDays: 7, stepSeconds: 300 }} range="1h" />,
		);
		expect(screen.getByText("History kept for 7 days.")).toBeInTheDocument();
		expect(screen.getByText("One point per 5 min.")).toBeInTheDocument();
		expect(screen.queryByText(/reaches further back/)).not.toBeInTheDocument();
	});

	test("uses the singular for one day", () => {
		render(<MetricsNote meta={{ retentionDays: 1 }} range="1h" />);
		expect(screen.getByText("History kept for 1 day.")).toBeInTheDocument();
	});

	test.each([
		[10, "10 s"],
		[59, "59 s"],
		[60, "1 min"],
		[900, "15 min"],
		[3_600, "1 h"],
		[7_200, "2 h"],
		[86_400, "1 d"],
	])("a %i s step reads %s", (seconds, text) => {
		render(<MetricsNote meta={{ stepSeconds: seconds }} range="1h" />);
		expect(screen.getByText(`One point per ${text}.`)).toBeInTheDocument();
		expect(screen.queryByText(/History kept/)).not.toBeInTheDocument();
	});

	test("a range longer than the kept history is called out in words", () => {
		render(<MetricsNote meta={{ retentionDays: 7 }} range="30d" />);
		expect(
			screen.getByText(
				"30d reaches further back than the history kept, so only the last 7 days show.",
			),
		).toBeInTheDocument();
	});

	test("the warning says '1 day' for a one-day retention", () => {
		render(<MetricsNote meta={{ retentionDays: 1 }} range="7d" />);
		expect(
			screen.getByText(/so only the last 1 day show\./),
		).toBeInTheDocument();
	});

	test("a range exactly as long as the history is not a warning", () => {
		render(<MetricsNote meta={{ retentionDays: 7 }} range="7d" />);
		expect(screen.queryByText(/reaches further back/)).not.toBeInTheDocument();
	});

	test("no warning without a reported retention", () => {
		render(<MetricsNote meta={{ stepSeconds: 60 }} range="30d" />);
		expect(screen.queryByText(/reaches further back/)).not.toBeInTheDocument();
	});

	test("merges the caller's class name", () => {
		render(
			<MetricsNote meta={{ retentionDays: 3 }} range="1h" className="mt-9" />,
		);
		expect(
			screen.getByText("History kept for 3 days.").parentElement,
		).toHaveClass("mt-9");
	});
});
