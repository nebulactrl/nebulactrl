import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { Progress } from "./progress";

const bar = () => screen.getByRole("progressbar");

describe("Progress", () => {
	test("exposes a progressbar with the current value and range", () => {
		render(<Progress value={40} aria-label="CPU" />);
		expect(bar()).toHaveAttribute("aria-valuenow", "40");
		expect(bar()).toHaveAttribute("aria-valuemin", "0");
		expect(bar()).toHaveAttribute("aria-valuemax", "100");
		expect(screen.getByRole("progressbar", { name: "CPU" })).toBe(bar());
	});

	test("the indicator width follows the value", () => {
		const { rerender } = render(<Progress value={25} />);
		const indicator = () =>
			document.querySelector("[data-slot=progress-indicator]") as HTMLElement;
		expect(indicator().style.width).toBe("25%");
		rerender(<Progress value={80} />);
		expect(indicator().style.width).toBe("80%");
	});

	test("boundary values 0 and 100", () => {
		const { rerender } = render(<Progress value={0} />);
		const indicator = () =>
			document.querySelector("[data-slot=progress-indicator]") as HTMLElement;
		expect(bar()).toHaveAttribute("aria-valuenow", "0");
		expect(indicator().style.width).toBe("0%");
		rerender(<Progress value={100} />);
		expect(bar()).toHaveAttribute("aria-valuenow", "100");
		expect(indicator().style.width).toBe("100%");
	});

	test("a custom max rescales the percentage", () => {
		render(<Progress value={5} max={20} />);
		expect(bar()).toHaveAttribute("aria-valuemax", "20");
		expect(
			(document.querySelector("[data-slot=progress-indicator]") as HTMLElement)
				.style.width,
		).toBe("25%");
	});

	test("a null value is indeterminate: no aria-valuenow, no fixed width", () => {
		render(<Progress value={null} />);
		expect(bar()).not.toHaveAttribute("aria-valuenow");
		expect(bar()).toHaveAttribute("data-indeterminate");
		const indicator = document.querySelector(
			"[data-slot=progress-indicator]",
		) as HTMLElement;
		expect(indicator).toHaveAttribute("data-indeterminate");
		expect(indicator.style.width).toBe("");
	});

	test("warnAt flags the bar at and above the threshold only", () => {
		const { rerender } = render(<Progress value={79} warnAt={80} />);
		expect(bar()).not.toHaveAttribute("data-warn");
		rerender(<Progress value={80} warnAt={80} />);
		expect(bar()).toHaveAttribute("data-warn", "true");
		rerender(<Progress value={100} warnAt={80} />);
		expect(bar()).toHaveAttribute("data-warn", "true");
	});

	test("without warnAt a full bar is not flagged", () => {
		render(<Progress value={100} />);
		expect(bar()).not.toHaveAttribute("data-warn");
	});

	test("an indeterminate bar never warns, even with warnAt 0", () => {
		render(<Progress value={null} warnAt={0} />);
		expect(bar()).not.toHaveAttribute("data-warn");
	});

	test("tone=warning flags the bar regardless of value", () => {
		render(<Progress value={1} tone="warning" />);
		expect(bar()).toHaveAttribute("data-warn", "true");
	});

	test("live sets data-live", () => {
		const { rerender } = render(<Progress value={10} />);
		expect(bar()).not.toHaveAttribute("data-live");
		rerender(<Progress value={10} live />);
		expect(bar()).toHaveAttribute("data-live", "true");
	});

	test("hairline size gives the track a different treatment than default", () => {
		const trackClass = (size: "default" | "hairline") => {
			const { unmount } = render(<Progress value={10} size={size} />);
			const cls = (
				document.querySelector("[data-slot=progress-track]") as HTMLElement
			).className;
			unmount();
			return cls;
		};
		expect(trackClass("hairline")).not.toBe(trackClass("default"));
	});

	test("renders children before the track (e.g. a label)", () => {
		render(
			<Progress value={30}>
				<span>Uploading</span>
			</Progress>,
		);
		const kids = Array.from(bar().children);
		const track = document.querySelector("[data-slot=progress-track]");
		expect(kids[0]).toHaveTextContent("Uploading");
		expect(kids.indexOf(track as Element)).toBe(1);
	});

	test("announces the value as text", () => {
		render(<Progress value={30} />);
		expect(bar()).toHaveAttribute("aria-valuetext", "30%");
	});

	test("inline renders spans for root, track and indicator", () => {
		render(<Progress value={50} inline />);
		expect(bar().tagName).toBe("SPAN");
		expect(document.querySelector("[data-slot=progress-track]")?.tagName).toBe(
			"SPAN",
		);
		expect(
			document.querySelector("[data-slot=progress-indicator]")?.tagName,
		).toBe("SPAN");
	});

	test("by default root, track and indicator are divs", () => {
		render(<Progress value={50} />);
		expect(bar().tagName).toBe("DIV");
		expect(document.querySelector("[data-slot=progress-track]")?.tagName).toBe(
			"DIV",
		);
	});

	test("className is applied to the root", () => {
		render(<Progress value={50} className="w-40" />);
		expect(bar()).toHaveClass("w-40");
	});
});
