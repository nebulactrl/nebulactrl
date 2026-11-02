import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { act, waitFor } from "@testing-library/react";
import { renderWithProviders } from "#/test/render";
import { useRevealHash } from "./use-reveal-hash";

interface Reveal {
	id: string;
	options: ScrollIntoViewOptions | undefined;
	marginTop: string;
}

let revealed: Reveal[];
let flashes: string[];
const realScroll = Element.prototype.scrollIntoView;
const realAnimate = Element.prototype.animate;
const realMatchMedia = window.matchMedia;

beforeEach(() => {
	revealed = [];
	flashes = [];
	Element.prototype.scrollIntoView = function (
		this: HTMLElement,
		options?: boolean | ScrollIntoViewOptions,
	) {
		// The router scrolls to a hash by itself, without options; only the hook's calls count.
		if (typeof options !== "object") return;
		revealed.push({
			id: this.id,
			options,
			marginTop: this.style.scrollMarginTop,
		});
	};
	Element.prototype.animate = function (this: Element) {
		flashes.push(this.id);
		return { finished: Promise.resolve(), cancel() {} } as unknown as Animation;
	};
});

afterEach(() => {
	Element.prototype.scrollIntoView = realScroll;
	Element.prototype.animate = realAnimate;
	window.matchMedia = realMatchMedia;
});

/** A screen that owns hashes of the form `row-<id>`. */
function findRow(hash: string) {
	if (!hash.startsWith("row-")) return undefined;
	return () => document.getElementById(hash);
}

function Screen({ late = false }: { late?: boolean }) {
	useRevealHash(findRow);
	return (
		<div>
			<div id="row-1">one</div>
			{late ? null : <div id="row-2">two</div>}
		</div>
	);
}

describe("useRevealHash", () => {
	test("scrolls the element the hash names to the top, with room above it", async () => {
		await renderWithProviders(<Screen />, { path: "/x#row-2" });
		expect(revealed).toEqual([
			{
				id: "row-2",
				options: { block: "start", behavior: "auto" },
				marginTop: "16px",
			},
		]);
	});

	test("does nothing for a hash the screen does not own", async () => {
		await renderWithProviders(<Screen />, { path: "/x#other" });
		expect(revealed).toEqual([]);
	});

	test("does nothing without a hash", async () => {
		await renderWithProviders(<Screen />, { path: "/x" });
		expect(revealed).toEqual([]);
	});

	test("reduced motion scrolls at once and does not flash the row", async () => {
		await renderWithProviders(<Screen />, { path: "/x#row-1" });
		expect(revealed[0]?.options?.behavior).toBe("auto");
		expect(flashes).toEqual([]);
	});

	test("with motion allowed it scrolls smoothly and flashes the row once", async () => {
		window.matchMedia = ((query: string) => ({
			matches: false,
			media: query,
			addEventListener() {},
			removeEventListener() {},
		})) as unknown as typeof window.matchMedia;
		await renderWithProviders(<Screen />, { path: "/x#row-1" });
		expect(revealed[0]?.options).toEqual({
			block: "start",
			behavior: "smooth",
		});
		expect(flashes).toEqual(["row-1"]);
	});

	test("waits for an element that is drawn later, then reveals it", async () => {
		await renderWithProviders(<Screen late />, { path: "/x#row-2" });
		expect(revealed).toEqual([]);
		await act(async () => {
			const row = document.createElement("div");
			row.id = "row-2";
			document.body.append(row);
		});
		await waitFor(() => expect(revealed.map((r) => r.id)).toEqual(["row-2"]));
		await act(async () => {
			const another = document.createElement("div");
			document.body.append(another);
		});
		expect(revealed).toHaveLength(1);
	});

	test("stops watching the page once the screen unmounts", async () => {
		const { unmount } = await renderWithProviders(<Screen late />, {
			path: "/x#row-2",
		});
		unmount();
		const row = document.createElement("div");
		row.id = "row-2";
		document.body.append(row);
		await act(async () => {});
		expect(revealed).toEqual([]);
	});
});
