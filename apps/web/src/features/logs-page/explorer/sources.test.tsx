import { describe, expect, test } from "bun:test";
import { useRouterState } from "@tanstack/react-router";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithProviders } from "#/test/render";
import {
	ALL_SOURCES,
	isRuntimeOnly,
	type LogSource,
	logSourcesSearch,
	parseSources,
	sourcesParam,
	useSourcesParam,
} from "./sources";

describe("parseSources", () => {
	test("nothing, or something that is not text, is both sources", () => {
		expect(parseSources(undefined)).toEqual(ALL_SOURCES);
		expect(parseSources(3)).toEqual(ALL_SOURCES);
		expect(parseSources("")).toEqual(ALL_SOURCES);
	});

	test("reads one source or a comma list", () => {
		expect(parseSources("runtime")).toEqual(["runtime"]);
		expect(parseSources("requests")).toEqual(["requests"]);
		expect(parseSources("runtime,requests")).toEqual(["runtime", "requests"]);
	});

	test("drops unknown names; nothing known is both", () => {
		expect(parseSources("requests,bogus")).toEqual(["requests"]);
		expect(parseSources("bogus")).toEqual(ALL_SOURCES);
		expect(parseSources(",")).toEqual(ALL_SOURCES);
	});

	test("merges repeats, trims, and orders runtime first", () => {
		expect(parseSources(" requests , runtime,requests")).toEqual([
			"runtime",
			"requests",
		]);
	});
});

describe("sourcesParam", () => {
	test("spells the set in the API's order", () => {
		expect(sourcesParam(["runtime", "requests"])).toBe("runtime,requests");
		expect(sourcesParam(["requests", "runtime"])).toBe("runtime,requests");
		expect(sourcesParam(["requests"])).toBe("requests");
		expect(sourcesParam(["runtime"])).toBe("runtime");
		expect(sourcesParam([])).toBe("");
	});
});

describe("isRuntimeOnly", () => {
	test("is true for the runtime log alone", () => {
		expect(isRuntimeOnly(["runtime"])).toBe(true);
		expect(isRuntimeOnly(["runtime", "requests"])).toBe(false);
		expect(isRuntimeOnly(["requests"])).toBe(false);
		expect(isRuntimeOnly([])).toBe(false);
	});
});

describe("logSourcesSearch", () => {
	test("keeps a text value and drops anything else", () => {
		expect(logSourcesSearch.sources.parse("requests")).toBe("requests");
		expect(logSourcesSearch.sources.parse(undefined)).toBeUndefined();
		expect(logSourcesSearch.sources.parse(42)).toBeUndefined();
	});
});

function Probe() {
	const { sources, setSources } = useSourcesParam();
	const search = useRouterState({ select: (s) => s.location.searchStr });
	const pick = (next: LogSource[]) => () => setSources(next);
	return (
		<div>
			<output aria-label="sources">{sources.join(",")}</output>
			<output aria-label="search">{search}</output>
			<button type="button" onClick={pick(["requests"])}>
				requests
			</button>
			<button type="button" onClick={pick(["runtime"])}>
				runtime
			</button>
			<button type="button" onClick={pick(["requests", "runtime"])}>
				both
			</button>
			<button type="button" onClick={pick([])}>
				none
			</button>
		</div>
	);
}

const sources = () => screen.getByLabelText("sources");
const search = () => screen.getByLabelText("search");

describe("useSourcesParam", () => {
	test("is both sources when the address has no ?sources=", async () => {
		await renderWithProviders(<Probe />, { path: "/" });
		expect(sources()).toHaveTextContent("runtime,requests");
	});

	test("reads the address and ignores what it does not know", async () => {
		const first = await renderWithProviders(<Probe />, {
			path: "/?sources=requests",
		});
		expect(sources()).toHaveTextContent(/^requests$/);
		first.unmount();
		await renderWithProviders(<Probe />, { path: "/?sources=nope" });
		expect(sources()).toHaveTextContent("runtime,requests");
	});

	test("writes a subset into the address, keeps other params, and removes the key for both", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Probe />, { path: "/?q=error" });
		await user.click(screen.getByRole("button", { name: "requests" }));
		await waitFor(() => expect(sources()).toHaveTextContent(/^requests$/));
		expect(search().textContent).toContain("sources=requests");
		expect(search().textContent).toContain("q=error");

		await user.click(screen.getByRole("button", { name: "both" }));
		await waitFor(() =>
			expect(sources()).toHaveTextContent("runtime,requests"),
		);
		expect(search().textContent).not.toContain("sources");
		expect(search().textContent).toContain("q=error");
	});

	test("choosing nothing falls back to both rather than an empty view", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Probe />, { path: "/?sources=runtime" });
		expect(sources()).toHaveTextContent(/^runtime$/);
		await user.click(screen.getByRole("button", { name: "none" }));
		await waitFor(() =>
			expect(sources()).toHaveTextContent("runtime,requests"),
		);
		expect(search().textContent).not.toContain("sources");
	});
});
