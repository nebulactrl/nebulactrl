import { describe, expect, it, mock } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { PreviewAction } from "./preview-action";

describe("PreviewAction", () => {
	it("opens a published page on its hostname in a new tab", () => {
		const onExplain = mock(() => {});
		render(
			<PreviewAction
				page={{ hostname: "status.example.com", published: true }}
				explaining={false}
				onExplain={onExplain}
			/>,
		);
		// Button renders the anchor with role="button", so it is found by that role.
		const link = screen.getByRole("button", { name: /Open page/ });
		expect(link.tagName).toBe("A");
		expect(link).toHaveAttribute("href", "https://status.example.com/");
		expect(link).toHaveAttribute("target", "_blank");
		expect(link).toHaveAttribute("rel", "noopener noreferrer");
		expect(link).toHaveTextContent("(opens in a new tab)");
		expect(
			screen.queryByRole("button", { name: "Preview" }),
		).not.toBeInTheDocument();
	});

	it.each([
		[
			"a draft with a hostname",
			{ hostname: "status.example.com", published: false },
		],
		["a draft without a hostname", { hostname: null, published: false }],
		[
			"a published page whose hostname was cleared",
			{ hostname: null, published: true },
		],
	])("asks for the reason instead of opening %s", async (_name, page) => {
		const onExplain = mock(() => {});
		render(
			<PreviewAction page={page} explaining={false} onExplain={onExplain} />,
		);
		expect(
			screen.queryByRole("button", { name: /Open page/ }),
		).not.toBeInTheDocument();
		expect(document.querySelector("a")).toBeNull();
		const button = screen.getByRole("button", { name: "Preview" });
		expect(button).toHaveAttribute("aria-expanded", "false");
		await userEvent.setup().click(button);
		expect(onExplain).toHaveBeenCalledTimes(1);
	});

	it("reports the reason as expanded while it is shown", () => {
		render(
			<PreviewAction
				page={{ hostname: null, published: false }}
				explaining
				onExplain={() => {}}
			/>,
		);
		expect(screen.getByRole("button", { name: "Preview" })).toHaveAttribute(
			"aria-expanded",
			"true",
		);
	});
});
