import { describe, expect, mock, test } from "bun:test";
import { fireEvent, render, screen } from "@testing-library/react";
import { VolumeHistogram } from "./volume-histogram";

const start = Date.parse("2026-10-29T10:00:00.000Z");
const series = [
	{ start, count: 1_000, errors: 0 },
	{ start: start + 15_000, count: 240, errors: 12 },
	{ start: start + 30_000, count: 0, errors: 0 },
	{ start: start + 45_000, count: 1, errors: 1 },
];

function chart(onSelectSpan = mock(), points = series) {
	render(
		<VolumeHistogram
			series={points}
			rangeText="the last 15 minutes"
			onSelectSpan={onSelectSpan}
		/>,
	);
	const element = screen.getByTestId("volume-chart");
	element.getBoundingClientRect = () =>
		({
			left: 100,
			top: 0,
			width: 400,
			height: 56,
			right: 500,
			bottom: 56,
		}) as DOMRect;
	return { element, onSelectSpan };
}

describe("VolumeHistogram", () => {
	test("sums the lines and errors for a screen reader, with thousands separated", () => {
		chart();
		expect(
			screen.getByText("1,241 lines, 13 errors in the last 15 minutes"),
		).toBeInTheDocument();
	});

	test("says a quiet range is empty", () => {
		chart(mock(), [{ start, count: 0, errors: 0 }]);
		expect(
			screen.getByText("No matching lines in the last 15 minutes"),
		).toBeInTheDocument();
		expect(
			screen.getByText("0 lines, 0 errors in the last 15 minutes"),
		).toBeInTheDocument();
	});

	test("hovering a bar names its time and counts, leaving hides them", () => {
		const { element } = chart();
		fireEvent.pointerMove(element, { clientX: 100 + 150, pointerId: 1 });
		expect(screen.getByText("240 lines")).toBeInTheDocument();
		expect(screen.getByText("12 errors")).toBeInTheDocument();
		fireEvent.pointerLeave(element);
		expect(screen.queryByText("240 lines")).toBeNull();
	});

	test("a drag across buckets selects from the first bucket's start to the last one's end", () => {
		const { element, onSelectSpan } = chart();
		fireEvent.pointerDown(element, {
			clientX: 100 + 120,
			button: 0,
			pointerId: 1,
		});
		fireEvent.pointerMove(element, { clientX: 100 + 390, pointerId: 1 });
		fireEvent.pointerUp(element, { clientX: 100 + 390, pointerId: 1 });
		expect(onSelectSpan).toHaveBeenCalledWith({
			from: "2026-10-29T10:00:15.000Z",
			to: "2026-10-29T10:01:00.000Z",
		});
	});

	test("a drag the other way selects the same span", () => {
		const { element, onSelectSpan } = chart();
		fireEvent.pointerDown(element, {
			clientX: 100 + 250,
			button: 0,
			pointerId: 1,
		});
		fireEvent.pointerUp(element, { clientX: 100 + 10, pointerId: 1 });
		expect(onSelectSpan).toHaveBeenCalledWith({
			from: "2026-10-29T10:00:00.000Z",
			to: "2026-10-29T10:00:45.000Z",
		});
	});

	test("a press and release on one bucket selects nothing", () => {
		const { element, onSelectSpan } = chart();
		fireEvent.pointerDown(element, {
			clientX: 100 + 150,
			button: 0,
			pointerId: 1,
		});
		fireEvent.pointerUp(element, { clientX: 100 + 160, pointerId: 1 });
		expect(onSelectSpan).not.toHaveBeenCalled();
	});

	test("a cancelled drag selects nothing", () => {
		const { element, onSelectSpan } = chart();
		fireEvent.pointerDown(element, {
			clientX: 100 + 10,
			button: 0,
			pointerId: 1,
		});
		fireEvent.pointerCancel(element, { pointerId: 1 });
		fireEvent.pointerUp(element, { clientX: 100 + 390, pointerId: 1 });
		expect(onSelectSpan).not.toHaveBeenCalled();
	});

	test("names the start, middle and end of the range under the bars", () => {
		chart();
		const clock = (ms: number) =>
			new Date(ms).toLocaleTimeString("en-GB", { hour12: false });
		const labels = screen.getByTestId("volume-labels");
		expect([...labels.children].map((label) => label.textContent)).toEqual([
			clock(start),
			clock(start + 30_000),
			clock(start + 60_000),
		]);
	});

	test("every non-empty bucket is at least 2px tall, an empty one has none", () => {
		chart();
		const bars = [
			...screen
				.getByTestId("volume-chart")
				.querySelectorAll<HTMLElement>(".transition-\\[height\\]"),
		];
		expect(bars).toHaveLength(4);
		// 1 line against a peak of 1,000 is 0.1%: the floor keeps it visible.
		expect(bars[3]?.style.minHeight).toBe("2px");
		expect(bars[0]?.style.minHeight).toBe("2px");
		expect(bars[2]?.style.minHeight).toBe("0");
		expect(bars[2]?.style.height).toBe("0%");
	});

	test("while the first counts load it shows a skeleton, not an empty range", () => {
		render(
			<VolumeHistogram
				series={[]}
				rangeText="the last 15 minutes"
				onSelectSpan={mock()}
				status="loading"
			/>,
		);
		expect(screen.getAllByText(/Loading counts/).length).toBeGreaterThan(0);
		expect(screen.queryByText(/No matching lines/)).toBeNull();
		expect(screen.queryByTestId("volume-labels")).toBeNull();
	});

	test("when the counts failed it says so with the reason, never that nothing matched", () => {
		render(
			<VolumeHistogram
				series={[]}
				rangeText="the last 15 minutes"
				onSelectSpan={mock()}
				status="unavailable"
				reason="The agent on alpha is too old to count."
			/>,
		);
		expect(
			screen.getByText(
				"Counts unavailable · The agent on alpha is too old to count.",
			),
		).toBeInTheDocument();
		expect(screen.queryByText(/No matching lines/)).toBeNull();
		expect(screen.queryByText(/0 lines/)).toBeNull();
	});

	test("counts that failed cannot be dragged across", () => {
		const onSelectSpan = mock();
		render(
			<VolumeHistogram
				series={series}
				rangeText="the last 15 minutes"
				onSelectSpan={onSelectSpan}
				status="unavailable"
			/>,
		);
		const element = screen.getByTestId("volume-chart");
		fireEvent.pointerDown(element, { clientX: 10, button: 0, pointerId: 1 });
		fireEvent.pointerUp(element, { clientX: 300, pointerId: 1 });
		expect(onSelectSpan).not.toHaveBeenCalled();
	});
});
