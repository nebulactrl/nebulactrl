import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { OpenTarget } from "#/lib/service-urls";
import { renderWithProviders } from "#/test/render";
import { OpenDomainLink, OpenSiteButton } from "./open-site";

const site: OpenTarget = {
	host: "api.acme.dev",
	url: "https://api.acme.dev/",
	tailnetOnly: false,
};
const docs: OpenTarget = {
	host: "docs.acme.dev",
	url: "https://docs.acme.dev/",
	tailnetOnly: false,
};
const tailnet: OpenTarget = {
	host: "api.tail1234.ts.net",
	url: "https://api.tail1234.ts.net/",
	tailnetOnly: true,
};

describe("OpenDomainLink", () => {
	test("is a link to the site that opens in a new tab with no handle back", async () => {
		await renderWithProviders(<OpenDomainLink target={site} />);
		const link = screen.getByRole("link", {
			name: "Open api.acme.dev in a new tab",
		});
		expect(link).toHaveAttribute("href", "https://api.acme.dev/");
		expect(link).toHaveAttribute("target", "_blank");
		expect(link).toHaveAttribute("rel", "noopener noreferrer");
		expect(link).toHaveTextContent("api.acme.dev");
	});
});

describe("OpenSiteButton", () => {
	test("renders nothing when there is nothing to open", async () => {
		const view = await renderWithProviders(<OpenSiteButton targets={[]} />);
		expect(view.container).toBeEmptyDOMElement();
	});

	test("one public address is a single Open link in a new tab", async () => {
		await renderWithProviders(<OpenSiteButton targets={[site]} />);
		const link = screen.getByRole("button", {
			name: "Open api.acme.dev in a new tab",
		});
		expect(link).toHaveTextContent("Open");
		expect(link).toHaveAttribute("href", "https://api.acme.dev/");
		expect(link).toHaveAttribute("target", "_blank");
		expect(link).toHaveAttribute("rel", "noopener noreferrer");
		expect(
			screen.queryByRole("button", { name: "Choose an address to open" }),
		).toBeNull();
	});

	test("a lone tailnet address explains itself on hover", async () => {
		await renderWithProviders(<OpenSiteButton targets={[tailnet]} />);
		const link = screen.getByRole("button", {
			name: "Open api.tail1234.ts.net in a new tab",
		});
		expect(link).toHaveAttribute("href", "https://api.tail1234.ts.net/");
		await userEvent.setup().hover(link);
		expect(
			await screen.findByText("Reachable on your tailnet only"),
		).toBeInTheDocument();
	});

	test("a public address has no tailnet tooltip", async () => {
		await renderWithProviders(<OpenSiteButton targets={[site]} />);
		await userEvent
			.setup()
			.hover(screen.getByRole("button", { name: /^Open api\.acme\.dev/ }));
		expect(screen.queryByText("Reachable on your tailnet only")).toBeNull();
	});

	test("several addresses: the first opens directly and a chevron lists them all", async () => {
		await renderWithProviders(
			<OpenSiteButton targets={[site, docs, tailnet]} />,
		);
		const group = screen.getByRole("group", { name: "Open the service" });
		expect(
			within(group).getByRole("button", {
				name: "Open api.acme.dev in a new tab",
			}),
		).toHaveAttribute("href", "https://api.acme.dev/");
		await userEvent.setup().click(
			within(group).getByRole("button", {
				name: "Choose an address to open",
			}),
		);
		const menu = await screen.findByRole("menu");
		const items = within(menu).getAllByRole("menuitem");
		expect(items.map((item) => item.getAttribute("aria-label"))).toEqual([
			"Open api.acme.dev in a new tab",
			"Open docs.acme.dev in a new tab",
			"Open api.tail1234.ts.net in a new tab. Reachable on your tailnet only",
		]);
		expect(items.map((item) => item.getAttribute("href"))).toEqual([
			"https://api.acme.dev/",
			"https://docs.acme.dev/",
			"https://api.tail1234.ts.net/",
		]);
		for (const item of items) {
			expect(item).toHaveAttribute("target", "_blank");
			expect(item).toHaveAttribute("rel", "noopener noreferrer");
		}
		// Tailnet-only entries are marked, and set apart by a separator.
		expect(items[2]).toHaveTextContent("Tailnet only");
		expect(items[0]).not.toHaveTextContent("Tailnet only");
		expect(within(menu).getAllByRole("separator")).toHaveLength(1);
	});

	test("with only public addresses the menu has no separator or tailnet mark", async () => {
		await renderWithProviders(<OpenSiteButton targets={[site, docs]} />);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Choose an address to open" }));
		const menu = await screen.findByRole("menu");
		expect(within(menu).queryAllByRole("separator")).toHaveLength(0);
		expect(within(menu).queryByText("Tailnet only")).toBeNull();
	});

	test("when the first address is tailnet-only, no separator precedes it", async () => {
		await renderWithProviders(<OpenSiteButton targets={[tailnet, docs]} />);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Choose an address to open" }));
		const menu = await screen.findByRole("menu");
		expect(within(menu).queryAllByRole("separator")).toHaveLength(0);
	});
});
