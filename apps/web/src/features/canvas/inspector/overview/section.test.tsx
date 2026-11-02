import { describe, expect, mock, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { OverviewSection } from "./section";

describe("OverviewSection", () => {
	test("is a labelled region with the title as its heading and the content under it", () => {
		render(
			<OverviewSection id="overview-health" title="Health">
				<p>all good</p>
			</OverviewSection>,
		);
		const region = screen.getByRole("region", { name: "Health" });
		expect(region).toHaveAttribute("id", "overview-health");
		expect(screen.getByRole("heading", { name: "Health" })).toBeInTheDocument();
		expect(region).toContainElement(screen.getByText("all good"));
	});

	test("has no action link without an action", () => {
		render(
			<OverviewSection title="Scaling">
				<p>x</p>
			</OverviewSection>,
		);
		expect(screen.queryByRole("button")).not.toBeInTheDocument();
	});

	test("its action is a button named by the label that calls back on click", async () => {
		const onClick = mock(() => {});
		render(
			<OverviewSection
				title="Latest deployments"
				action={{ label: "View all", onClick }}
			>
				<p>x</p>
			</OverviewSection>,
		);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "View all" }));
		expect(onClick).toHaveBeenCalledTimes(1);
	});
});
