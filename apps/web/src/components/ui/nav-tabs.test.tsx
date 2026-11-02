import { afterEach, beforeEach, describe, expect, it, mock } from "bun:test";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { NavTab, NavTabs } from "#/components/ui/nav-tabs";
import { installLayoutEnv, type LayoutEnv } from "./layout-env.testkit";

function Bar({ active = "overview" }: { active?: string }) {
	return (
		<NavTabs aria-label="Server sections">
			<NavTab active={active === "overview"} href="/overview">
				Overview
			</NavTab>
			<NavTab active={active === "traffic"} href="/traffic">
				Traffic
			</NavTab>
		</NavTabs>
	);
}

function Harness() {
	const [active, setActive] = useState("overview");
	return (
		<NavTabs aria-label="x">
			{["overview", "traffic"].map((name) => (
				<NavTab
					key={name}
					active={active === name}
					href={`/${name}`}
					onClick={(e) => {
						e.preventDefault();
						setActive(name);
					}}
				>
					{name}
				</NavTab>
			))}
		</NavTabs>
	);
}

describe("NavTabs", () => {
	let env: LayoutEnv;
	beforeEach(() => {
		env = installLayoutEnv();
	});
	afterEach(() => env.restore());

	it("is a named navigation landmark of links, not an ARIA tablist", () => {
		render(<Bar />);
		const nav = screen.getByRole("navigation", { name: "Server sections" });
		expect(nav).toHaveAttribute("data-slot", "nav-tabs");
		expect(screen.queryByRole("tablist")).not.toBeInTheDocument();
		expect(screen.queryByRole("tab")).not.toBeInTheDocument();
		expect(screen.getByRole("link", { name: "Overview" })).toHaveAttribute(
			"href",
			"/overview",
		);
		expect(screen.getByRole("link", { name: "Traffic" })).toHaveAttribute(
			"href",
			"/traffic",
		);
	});

	it("marks only the active tab with aria-current=page", () => {
		render(<Bar active="traffic" />);
		expect(screen.getByRole("link", { name: "Traffic" })).toHaveAttribute(
			"aria-current",
			"page",
		);
		expect(screen.getByRole("link", { name: "Overview" })).not.toHaveAttribute(
			"aria-current",
		);
	});

	it("moves aria-current when the route changes", () => {
		const { rerender } = render(<Bar active="overview" />);
		rerender(<Bar active="traffic" />);
		expect(screen.getByRole("link", { name: "Traffic" })).toHaveAttribute(
			"aria-current",
			"page",
		);
		expect(screen.getByRole("link", { name: "Overview" })).not.toHaveAttribute(
			"aria-current",
		);
	});

	it("tags each tab with its slot and forwards click handlers and caller classes", async () => {
		const onClick = mock((e: React.MouseEvent) => e.preventDefault());
		render(
			<NavTabs aria-label="x" className="nav-x">
				<NavTab active={false} href="/a" className="tab-x" onClick={onClick}>
					A
				</NavTab>
			</NavTabs>,
		);
		const link = screen.getByRole("link", { name: "A" });
		expect(link).toHaveAttribute("data-slot", "nav-tab");
		expect(link).toHaveClass("tab-x", "h-10");
		expect(screen.getByRole("navigation")).toHaveClass("nav-x", "relative");
		await userEvent.setup().click(link);
		expect(onClick).toHaveBeenCalledTimes(1);
	});

	it("is walked by Tab, one link at a time (no roving)", async () => {
		const user = userEvent.setup();
		render(<Bar />);
		await user.tab();
		expect(screen.getByRole("link", { name: "Overview" })).toHaveFocus();
		await user.tab();
		expect(screen.getByRole("link", { name: "Traffic" })).toHaveFocus();
	});

	it("renders through a custom element with the render prop", () => {
		render(
			<NavTabs aria-label="x">
				<NavTab active render={<a data-custom="1" href="/c" />}>
					C
				</NavTab>
			</NavTabs>,
		);
		const link = screen.getByRole("link", { name: "C" });
		expect(link).toHaveAttribute("data-custom", "1");
		expect(link).toHaveAttribute("aria-current", "page");
	});

	it("draws a hidden, decorative glide bar that sits under the current tab", async () => {
		env.layout = (el) =>
			el.textContent === "Overview"
				? { offsetLeft: 4, offsetWidth: 70 }
				: el.textContent === "Traffic"
					? { offsetLeft: 96, offsetWidth: 50 }
					: undefined;
		const { rerender } = render(<Bar active="overview" />);
		const nav = screen.getByRole("navigation");
		expect(nav).toHaveAttribute("data-glide", "u");
		const ind = nav.querySelector("[data-ind]") as HTMLElement;
		expect(ind).toHaveAttribute("aria-hidden", "true");
		await env.flush();
		expect(ind.style.width).toBe("70px");
		expect(ind.style.height).toBe("2px");
		expect(ind.style.transform).toBe("translateX(4px)");
		expect(ind.style.opacity).toBe("1");
		rerender(<Bar active="traffic" />);
		await env.flush();
		expect(ind.style.width).toBe("50px");
		expect(ind.style.transform).toBe("translateX(96px)");
	});

	it("hides the bar when no tab is current", async () => {
		render(<Bar active="none" />);
		await env.flush();
		expect(
			(
				screen
					.getByRole("navigation")
					.querySelector("[data-ind]") as HTMLElement
			).style.opacity,
		).toBe("0");
	});

	it("keeps the current tab in view by scrolling the strip", async () => {
		env.layout = (el) => {
			if (el.tagName === "NAV")
				return { rectLeft: 0, rectWidth: 100, clientWidth: 100 };
			if (el.textContent === "Traffic")
				return {
					offsetLeft: 400,
					offsetWidth: 50,
					rectLeft: 400,
					rectWidth: 50,
				};
			return undefined;
		};
		render(<Bar active="traffic" />);
		await env.flush();
		const calls = env.scrollCalls.filter((c) => c.el.tagName === "NAV");
		expect(calls).toHaveLength(1);
		expect(calls[0]?.options).toEqual({ left: 382 });
	});

	it("fades the continuing end of an overflowing strip and merges a caller style", async () => {
		env.layout = (el) =>
			el.tagName === "NAV" ? { clientWidth: 100, scrollWidth: 500 } : undefined;
		render(
			<NavTabs aria-label="x" style={{ color: "red" }}>
				<NavTab active href="/a">
					A
				</NavTab>
			</NavTabs>,
		);
		const nav = screen.getByRole("navigation");
		await act(async () => {
			nav.dispatchEvent(new Event("scroll"));
		});
		expect(nav.style.maskImage).toContain("linear-gradient");
		expect(nav.style.color).toBe("red");
	});

	it("applies no mask when everything fits and still keeps the caller style", () => {
		render(
			<NavTabs aria-label="x" style={{ color: "blue" }}>
				<NavTab active href="/a">
					A
				</NavTab>
			</NavTabs>,
		);
		const nav = screen.getByRole("navigation");
		expect(nav.style.maskImage).toBeFalsy();
		expect(nav.style.color).toBe("blue");
	});

	it("follows state held by a parent", async () => {
		render(<Harness />);
		await userEvent
			.setup()
			.click(screen.getByRole("link", { name: "traffic" }));
		expect(screen.getByRole("link", { name: "traffic" })).toHaveAttribute(
			"aria-current",
			"page",
		);
		expect(screen.getByRole("link", { name: "overview" })).not.toHaveAttribute(
			"aria-current",
		);
	});
});
