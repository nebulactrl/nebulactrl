import {
	afterEach,
	beforeEach,
	describe,
	expect,
	setDefaultTimeout,
	test,
} from "bun:test";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithProviders } from "#/test/render";
import { stubResizeObserver } from "../resize-stub";
import { makeGeo } from "../test-fixtures";
import type { TrafficGeo, TrafficReport } from "../types";
import { OriginsCard } from "./origins-card";

setDefaultTimeout(20_000);

let restoreSize = () => {};
beforeEach(() => {
	restoreSize = stubResizeObserver({ width: 600, height: 300 });
});
afterEach(() => restoreSize());

type Report = Pick<TrafficReport, "geo" | "geoReason">;

async function card(report: Report, serverId?: string) {
	const view = await renderWithProviders(
		<OriginsCard report={report} serverId={serverId} />,
	);
	await act(async () => {
		await Promise.resolve();
	});
	return view;
}

/** A list row: the map's hotspots carry the same names, but only rows can be pressed. */
function row(
	name: RegExp,
	scope: { getAllByRole: typeof screen.getAllByRole } = screen,
) {
	const match = scope
		.getAllByRole("button", { name })
		.find((b) => b.hasAttribute("aria-pressed"));
	if (!match) throw new Error(`no row ${name}`);
	return match;
}

function geoOf(over: Partial<TrafficGeo> = {}): TrafficGeo {
	return { ...makeGeo("country"), ...over };
}

describe("OriginsCard empty states", () => {
	test("geolocation off: says so and links to the server's traffic settings", async () => {
		await card({ geoReason: "geo_disabled" }, "srv-7");
		expect(
			screen.getByRole("heading", { name: "Where requests come from" }),
		).toBeInTheDocument();
		expect(screen.getByText(/^Geolocation is off\./)).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Open traffic settings" }),
		).toHaveAttribute("href", "/servers/srv-7/settings/monitoring");
		expect(screen.queryByText("Top countries")).not.toBeInTheDocument();
	});

	test("with no reason the card assumes geolocation is off", async () => {
		await card({});
		expect(screen.getByText(/^Geolocation is off\./)).toBeInTheDocument();
	});

	test("traffic analytics off gets its own explanation", async () => {
		await card({ geoReason: "traffic_disabled" }, "srv-7");
		expect(
			screen.getByText(/^Traffic analytics is off, so there are no origins\./),
		).toBeInTheDocument();
		expect(screen.queryByText(/^Geolocation is off/)).not.toBeInTheDocument();
	});

	test("without a server the settings button goes to the servers list", async () => {
		await card({ geoReason: "geo_disabled" });
		expect(
			screen.getByRole("button", { name: "Open traffic settings" }),
		).toHaveAttribute("href", "/servers");
	});

	test("geolocation on but nothing located yet: explains the GeoIP database and keeps the credit", async () => {
		await card(
			{ geo: geoOf({ locatedRequests: 0, countries: [], cities: null }) },
			"srv-7",
		);
		expect(screen.getByText(/^No located requests yet\./)).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Check the GeoIP database" }),
		).toHaveAttribute("href", "/servers/srv-7/settings/monitoring");
		expect(
			screen.getByRole("link", { name: "IP geolocation by DB-IP" }),
		).toHaveAttribute("href", "https://db-ip.com");
		// The map stays on screen as a picture.
		expect(screen.queryByRole("application")).not.toBeInTheDocument();
		expect(
			screen.getByRole("img", { name: /Where requests come from/ }),
		).toBeInTheDocument();
	});
});

describe("OriginsCard with data", () => {
	test("summarises the located requests and the unlisted ones with the level", async () => {
		await card({ geo: geoOf() });
		expect(
			screen.getByText(
				"1k requests located · 50 from unlisted or private addresses · country level",
			),
		).toBeInTheDocument();
	});

	test("leaves out the unlisted clause when every request was located", async () => {
		await card({ geo: geoOf({ unknownRequests: 0 }) });
		expect(
			screen.getByText("1k requests located · country level"),
		).toBeInTheDocument();
	});

	test("lists countries with count and share, busiest first, and no cities at the country level", async () => {
		await card({ geo: geoOf() });
		expect(screen.getByText("Top countries")).toBeInTheDocument();
		expect(screen.getByText("2 countries")).toBeInTheDocument();
		const germany = row(/Germany/);
		expect(germany).toHaveTextContent("600");
		expect(germany).toHaveTextContent("60%");
		expect(row(/United States/)).toHaveTextContent("40%");
		expect(screen.queryByText("Top cities")).not.toBeInTheDocument();
	});

	test("at the city level it lists cities too, and the map marks cities", async () => {
		await card({ geo: makeGeo("city") });
		expect(screen.getByText("Top cities")).toBeInTheDocument();
		expect(screen.getByText("2 cities")).toBeInTheDocument();
		expect(row(/Berlin, DE/)).toHaveTextContent("50%");
		expect(
			screen.getByRole("button", { name: "Berlin, DE: 500 requests, 50%" }),
		).toBeInTheDocument();
		expect(screen.getByText(/· city level$/)).toBeInTheDocument();
	});

	test("the data credit is a real link to its source", async () => {
		await card({ geo: geoOf() });
		const link = screen.getByRole("link", { name: "IP geolocation by DB-IP" });
		expect(link).toHaveAttribute("href", "https://db-ip.com");
		expect(link).toHaveAttribute("target", "_blank");
		expect(link).toHaveAttribute("rel", expect.stringContaining("noopener"));
	});

	test("joins several credits with a middle dot and shows none when there are none", async () => {
		const { unmount } = await card({
			geo: geoOf({
				attribution: [
					{ source: "dbip", text: "DB-IP", url: "https://db-ip.com" },
					{ source: "maxmind", text: "MaxMind", url: "https://maxmind.com" },
				],
			}),
		});
		expect(
			screen.getByRole("link", { name: "DB-IP" }).parentElement?.parentElement,
		).toHaveTextContent("DB-IP · MaxMind");
		unmount();
		await card({ geo: geoOf({ attribution: null }) });
		expect(screen.queryByRole("link")).not.toBeInTheDocument();
	});

	test("a row pins on click, shows pressed, and unpins on a second click", async () => {
		const user = userEvent.setup();
		await card({ geo: geoOf() });
		const germany = row(/Germany/);
		expect(germany).toHaveAttribute("aria-pressed", "false");
		await user.click(germany);
		expect(germany).toHaveAttribute("aria-pressed", "true");
		// Pinning another moves the pin.
		const us = row(/United States/);
		await user.click(us);
		expect(germany).toHaveAttribute("aria-pressed", "false");
		expect(us).toHaveAttribute("aria-pressed", "true");
		await user.click(us);
		expect(us).toHaveAttribute("aria-pressed", "false");
	});

	test("hovering or focusing a row lights its place on the map and leaving puts it out", async () => {
		const user = userEvent.setup();
		const { container } = await card({ geo: geoOf() });
		const rings = () =>
			container.querySelectorAll('span[aria-hidden][style*="clip-path"]');
		expect(rings()).toHaveLength(0);
		await user.hover(row(/Germany/));
		expect(rings()).toHaveLength(2);
		await user.unhover(row(/Germany/));
		expect(rings()).toHaveLength(0);
		row(/United States/).focus();
		await waitFor(() => expect(rings()).toHaveLength(2));
		row(/United States/).blur();
		await waitFor(() => expect(rings()).toHaveLength(0));
	});

	test("a pinned row stays lit when the pointer leaves, and a city row lights only its own place", async () => {
		const user = userEvent.setup();
		const { container } = await card({ geo: makeGeo("city") });
		const rings = () =>
			container.querySelectorAll('span[aria-hidden][style*="clip-path"]');
		await user.click(row(/Berlin, DE/));
		await user.unhover(row(/Berlin, DE/));
		expect(rings()).toHaveLength(2);
		// A country row lights every place of the country: one here.
		await user.click(row(/Berlin, DE/));
		await user.hover(row(/^Germany/));
		expect(rings()).toHaveLength(2);
	});

	test("folds the long tail into one line that expands and collapses", async () => {
		const user = userEvent.setup();
		// Codes without a centroid: these countries are in the list but not on the map.
		const tail = Array.from({ length: 9 }, (_, i) => ({
			iso2: `X${i}`,
			name: `Tiny ${i}`,
			requests: 1,
			share: 0.001,
		}));
		await card({
			geo: geoOf({
				locatedRequests: 1_000,
				countries: [
					{ iso2: "DE", name: "Germany", requests: 600, share: 0.6 },
					{ iso2: "US", name: "United States", requests: 391, share: 0.391 },
					...tail,
				],
			}),
		});
		expect(screen.getByText("11 countries")).toBeInTheDocument();
		expect(screen.queryByText("Tiny 0")).not.toBeInTheDocument();
		const other = screen.getByRole("button", { name: /Other countries/ });
		expect(other).toHaveAttribute("title", "9 countries under 1%");
		const toggle = screen.getByRole("button", {
			name: "Show all 11 countries",
		});
		expect(toggle).toHaveAttribute("aria-expanded", "false");
		await user.click(toggle);
		expect(screen.getByText("Tiny 0")).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: /Other countries/ }),
		).not.toBeInTheDocument();
		const back = screen.getByRole("button", { name: "Show fewer" });
		expect(back).toHaveAttribute("aria-expanded", "true");
		await user.click(back);
		expect(screen.queryByText("Tiny 0")).not.toBeInTheDocument();
	});

	test("a short list has no fold control", async () => {
		await card({ geo: geoOf() });
		expect(
			screen.queryByRole("button", { name: /Show all/ }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Show fewer" }),
		).not.toBeInTheDocument();
	});
});

describe("OriginsCard expanded map", () => {
	async function open(report: Report) {
		const user = userEvent.setup();
		const view = await card(report);
		await user.click(screen.getByRole("button", { name: "Expand the map" }));
		const dialog = await screen.findByRole("dialog");
		await act(async () => {
			await Promise.resolve();
		});
		return { user, view, dialog };
	}

	test("opens a dialog with the title, summary, a region picker and the lists beside the map", async () => {
		const { dialog } = await open({ geo: makeGeo("city") });
		expect(
			within(dialog).getByRole("heading", { name: "Where requests come from" }),
		).toBeInTheDocument();
		expect(within(dialog).getByText(/requests located/)).toBeInTheDocument();
		expect(within(dialog).getByText("Map")).toBeInTheDocument();
		expect(
			within(dialog).getByText("Fit to data · 2 places located"),
		).toBeInTheDocument();
		expect(row(/Germany/, within(dialog))).toBeInTheDocument();
		expect(row(/Austin, US/, within(dialog))).toBeInTheDocument();
		expect(
			within(dialog).getByRole("link", { name: "IP geolocation by DB-IP" }),
		).toBeInTheDocument();
	});

	test("a single place reads 'place' not 'places'", async () => {
		const { dialog } = await open({
			geo: geoOf({
				countries: [{ iso2: "DE", name: "Germany", requests: 1000, share: 1 }],
			}),
		});
		expect(
			within(dialog).getByText("Fit to data · 1 place located"),
		).toBeInTheDocument();
	});

	test("choosing a region names it in the subtitle and a pan makes it 'Zoomed'", async () => {
		const { user, dialog } = await open({ geo: makeGeo("city") });
		await user.click(within(dialog).getByRole("button", { name: "Europe" }));
		expect(
			within(dialog).getByText("Europe · 2 places located"),
		).toBeInTheDocument();
		await user.click(
			within(dialog).getByRole("button", { name: "Fit to data" }),
		);
		expect(
			within(dialog).getByText("Fit to data · 2 places located"),
		).toBeInTheDocument();
		await user.click(within(dialog).getByRole("button", { name: "World" }));
		expect(
			within(dialog).getByText("World · 2 places located"),
		).toBeInTheDocument();
	});

	test("the dialog's zoom buttons step the view and say 'Zoomed'; the world cannot zoom out", async () => {
		const { user, dialog } = await open({ geo: makeGeo("city") });
		await user.click(within(dialog).getByRole("button", { name: "World" }));
		// The dialog's own buttons beside the picker (the map has none: controls is off).
		const zoomOut = within(dialog).getByRole("button", { name: "Zoom out" });
		const zoomIn = within(dialog).getByRole("button", { name: "Zoom in" });
		expect(zoomOut).toBeDisabled();
		await user.click(zoomIn);
		expect(
			within(dialog).getByText("Zoomed · 2 places located"),
		).toBeInTheDocument();
		expect(zoomOut).toBeEnabled();
		await user.click(zoomOut);
		expect(
			within(dialog).getByText("Zoomed · 2 places located"),
		).toBeInTheDocument();
	});

	test("zoom in stops at the closest region", async () => {
		const { user, dialog } = await open({ geo: makeGeo("city") });
		const zoomIn = within(dialog).getByRole("button", { name: "Zoom in" });
		for (let i = 0; i < 12 && !(zoomIn as HTMLButtonElement).disabled; i += 1) {
			await user.click(zoomIn);
		}
		expect(zoomIn).toBeDisabled();
	});

	test("the map in the dialog takes keys: + zooms, 0 returns to fit", async () => {
		const { dialog } = await open({ geo: makeGeo("city") });
		const map = within(dialog).getByRole("application");
		const user = userEvent.setup();
		map.focus();
		await user.keyboard("+");
		await waitFor(() =>
			expect(
				within(dialog).getByText(/^Zoomed · 2 places located$/),
			).toBeInTheDocument(),
		);
		await user.keyboard("0");
		await waitFor(() =>
			expect(
				within(dialog).getByText(/^Fit to data · 2 places located$/),
			).toBeInTheDocument(),
		);
	});

	test("a row hovered in the dialog lights the dialog's map", async () => {
		const { user, dialog } = await open({ geo: makeGeo("city") });
		const rings = () =>
			dialog.querySelectorAll('span[aria-hidden][style*="clip-path"]');
		await user.hover(row(/^Germany/, within(dialog)));
		expect(rings().length).toBeGreaterThan(0);
	});

	test("closing the dialog returns to the card", async () => {
		const { user } = await open({ geo: geoOf() });
		await user.click(screen.getByRole("button", { name: "Close" }));
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);
		expect(
			screen.getByRole("button", { name: "Expand the map" }),
		).toBeInTheDocument();
	});
});
