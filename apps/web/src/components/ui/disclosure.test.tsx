import { afterEach, beforeEach, describe, expect, it, jest } from "bun:test";
import { act, render, screen } from "@testing-library/react";
import { DisclosureChevron, DisclosurePanel } from "#/components/ui/disclosure";
import { installLayoutEnv, type LayoutEnv } from "./layout-env.testkit";

describe("DisclosurePanel", () => {
	let env: LayoutEnv;
	beforeEach(() => {
		env = installLayoutEnv();
	});
	afterEach(() => {
		jest.useRealTimers();
		document.documentElement.style.removeProperty("--acc-collapse");
		env.restore();
	});

	const panel = () =>
		document.querySelector<HTMLElement>(".t-acc-panel") as HTMLElement;

	it("open on first render: body present, shown at once, exposed to assistive tech", () => {
		render(
			<DisclosurePanel open>
				<p>Body</p>
			</DisclosurePanel>,
		);
		expect(screen.getByText("Body")).toBeInTheDocument();
		expect(panel()).toHaveAttribute("data-open", "true");
		expect(panel()).not.toHaveAttribute("aria-hidden");
		expect(panel()).not.toHaveAttribute("inert");
	});

	it("closed on first render: no body, hidden and inert", () => {
		render(
			<DisclosurePanel open={false}>
				<p>Body</p>
			</DisclosurePanel>,
		);
		expect(screen.queryByText("Body")).not.toBeInTheDocument();
		expect(panel()).toHaveAttribute("data-open", "false");
		expect(panel()).toHaveAttribute("aria-hidden", "true");
		expect(panel()).toHaveAttribute("inert");
	});

	it("opening mounts the body at once but flips data-open only after two frames", async () => {
		const { rerender } = render(
			<DisclosurePanel open={false}>
				<p>Body</p>
			</DisclosurePanel>,
		);
		rerender(
			<DisclosurePanel open>
				<p>Body</p>
			</DisclosurePanel>,
		);
		expect(screen.getByText("Body")).toBeInTheDocument();
		expect(panel()).toHaveAttribute("data-open", "false");
		expect(panel()).not.toHaveAttribute("aria-hidden");
		await env.flush();
		expect(panel()).toHaveAttribute("data-open", "true");
	});

	it("closing starts the collapse at once and unmounts the body after the collapse clock", () => {
		jest.useFakeTimers();
		const { rerender } = render(
			<DisclosurePanel open>
				<p>Body</p>
			</DisclosurePanel>,
		);
		rerender(
			<DisclosurePanel open={false}>
				<p>Body</p>
			</DisclosurePanel>,
		);
		expect(panel()).toHaveAttribute("data-open", "false");
		expect(panel()).toHaveAttribute("aria-hidden", "true");
		// Still mounted so the collapse can play: fast (250ms) + 40ms.
		act(() => jest.advanceTimersByTime(289));
		expect(screen.getByText("Body")).toBeInTheDocument();
		act(() => jest.advanceTimersByTime(2));
		expect(screen.queryByText("Body")).not.toBeInTheDocument();
	});

	it("follows the --acc-collapse clock from the CSS, in ms or s", () => {
		jest.useFakeTimers();
		document.documentElement.style.setProperty("--acc-collapse", "100ms");
		const { rerender, unmount } = render(
			<DisclosurePanel open>
				<p>Body</p>
			</DisclosurePanel>,
		);
		rerender(
			<DisclosurePanel open={false}>
				<p>Body</p>
			</DisclosurePanel>,
		);
		act(() => jest.advanceTimersByTime(141));
		expect(screen.queryByText("Body")).not.toBeInTheDocument();
		unmount();

		document.documentElement.style.setProperty("--acc-collapse", "0.5s");
		const second = render(
			<DisclosurePanel open>
				<p>Body</p>
			</DisclosurePanel>,
		);
		second.rerender(
			<DisclosurePanel open={false}>
				<p>Body</p>
			</DisclosurePanel>,
		);
		act(() => jest.advanceTimersByTime(500));
		expect(screen.getByText("Body")).toBeInTheDocument();
		act(() => jest.advanceTimersByTime(41));
		expect(screen.queryByText("Body")).not.toBeInTheDocument();
	});

	it("reopening before the collapse ends keeps the body", () => {
		jest.useFakeTimers();
		const { rerender } = render(
			<DisclosurePanel open>
				<p>Body</p>
			</DisclosurePanel>,
		);
		rerender(
			<DisclosurePanel open={false}>
				<p>Body</p>
			</DisclosurePanel>,
		);
		act(() => jest.advanceTimersByTime(100));
		rerender(
			<DisclosurePanel open>
				<p>Body</p>
			</DisclosurePanel>,
		);
		act(() => jest.advanceTimersByTime(1000));
		expect(screen.getByText("Body")).toBeInTheDocument();
		expect(panel()).not.toHaveAttribute("aria-hidden");
	});

	it("keepMounted holds the body after closing", () => {
		jest.useFakeTimers();
		const { rerender } = render(
			<DisclosurePanel open keepMounted>
				<p>Body</p>
			</DisclosurePanel>,
		);
		rerender(
			<DisclosurePanel open={false} keepMounted>
				<p>Body</p>
			</DisclosurePanel>,
		);
		act(() => jest.advanceTimersByTime(5000));
		expect(screen.getByText("Body")).toBeInTheDocument();
		expect(panel()).toHaveAttribute("aria-hidden", "true");
		expect(panel()).toHaveAttribute("inert");
	});

	it("keepMounted renders the body even when it starts closed", () => {
		render(
			<DisclosurePanel open={false} keepMounted>
				<p>Body</p>
			</DisclosurePanel>,
		);
		expect(screen.getByText("Body")).toBeInTheDocument();
	});

	it("becomes a labelled region only when labelledBy is given", () => {
		const { rerender } = render(
			<>
				<button id="hdr" type="button">
					Details
				</button>
				<DisclosurePanel open id="panel">
					<p>Body</p>
				</DisclosurePanel>
			</>,
		);
		expect(panel()).toHaveAttribute("id", "panel");
		expect(screen.queryByRole("region")).not.toBeInTheDocument();
		rerender(
			<>
				<button id="hdr" type="button">
					Details
				</button>
				<DisclosurePanel open id="panel" labelledBy="hdr">
					<p>Body</p>
				</DisclosurePanel>
			</>,
		);
		expect(screen.getByRole("region", { name: "Details" })).toBe(panel());
	});

	it("a closed region is hidden from the accessibility tree", () => {
		render(
			<>
				<span id="hdr">Details</span>
				<DisclosurePanel open={false} labelledBy="hdr">
					<p>Body</p>
				</DisclosurePanel>
			</>,
		);
		expect(screen.queryByRole("region")).not.toBeInTheDocument();
	});

	it("applies className to the panel and innerClassName to the clipping element", () => {
		render(
			<DisclosurePanel open className="outer-x" innerClassName="inner-x">
				<p>Body</p>
			</DisclosurePanel>,
		);
		expect(panel()).toHaveClass("outer-x");
		expect(panel().firstElementChild).toHaveClass(
			"t-acc-panel-inner",
			"inner-x",
		);
	});

	it("cancels the pending open frames on unmount", async () => {
		const { rerender, unmount } = render(
			<DisclosurePanel open={false}>
				<p>Body</p>
			</DisclosurePanel>,
		);
		rerender(
			<DisclosurePanel open>
				<p>Body</p>
			</DisclosurePanel>,
		);
		expect(env.pendingFrames()).toBeGreaterThan(0);
		unmount();
		expect(env.pendingFrames()).toBe(0);
	});
});

describe("DisclosureChevron", () => {
	it("is decorative and reports its open state", () => {
		const { rerender, container } = render(
			<DisclosureChevron open={false} className="size-3.5" />,
		);
		const chevron = container.querySelector(".t-acc-chevron") as HTMLElement;
		expect(chevron).toHaveAttribute("aria-hidden", "true");
		expect(chevron).toHaveAttribute("data-open", "false");
		expect(chevron).toHaveClass("size-3.5");
		expect(chevron.querySelector("svg")).not.toBeNull();
		rerender(<DisclosureChevron open />);
		expect(container.querySelector(".t-acc-chevron")).toHaveAttribute(
			"data-open",
			"true",
		);
	});
});
