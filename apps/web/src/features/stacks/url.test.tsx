import { describe, expect, test } from "bun:test";
import { useLocation } from "@tanstack/react-router";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { z } from "zod";
import { renderWithProviders } from "#/test/render";
import {
	type ShowOptions,
	stackSearch,
	useComponentParam,
	useStackNavigation,
} from "./url";

function Probe({ options }: { options?: ShowOptions }) {
	const component = useComponentParam();
	const { show } = useStackNavigation();
	const location = useLocation();
	return (
		<div>
			<output aria-label="component">{component ?? "none"}</output>
			<output aria-label="search">{JSON.stringify(location.search)}</output>
			<button type="button" onClick={() => options && show(options)}>
				go
			</button>
		</div>
	);
}

async function search(): Promise<Record<string, unknown>> {
	return JSON.parse((await screen.findByLabelText("search")).textContent ?? "");
}

describe("stackSearch", () => {
	const schema = z.object(stackSearch);

	test("keeps a component name", () => {
		expect(schema.parse({ component: "web" })).toEqual({ component: "web" });
	});

	test("drops a malformed component instead of failing the route", () => {
		expect(schema.parse({ component: 12 })).toEqual({ component: undefined });
		expect(schema.parse({ component: { a: 1 } })).toEqual({
			component: undefined,
		});
	});

	test("is optional", () => {
		expect(schema.parse({})).toEqual({});
	});
});

describe("useComponentParam", () => {
	test("reads ?component= from the address", async () => {
		await renderWithProviders(<Probe />, { path: "/x?component=web" });
		expect(await screen.findByLabelText("component")).toHaveTextContent("web");
	});

	test("is undefined without the parameter", async () => {
		await renderWithProviders(<Probe />, { path: "/x" });
		expect(await screen.findByLabelText("component")).toHaveTextContent("none");
	});

	test("an empty value is no component", async () => {
		await renderWithProviders(<Probe />, { path: "/x?component=" });
		expect(await screen.findByLabelText("component")).toHaveTextContent("none");
	});

	test("a non-string value (a number in the address parses as one) is no component", async () => {
		await renderWithProviders(<Probe />, { path: "/x?component=42" });
		expect(await screen.findByLabelText("component")).toHaveTextContent("none");
	});
});

describe("useStackNavigation", () => {
	test("show() moves service, tab and component in one navigation", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<Probe
				options={{ service: "svc-1", tab: "overview", component: "web" }}
			/>,
			{ path: "/x" },
		);
		await user.click(screen.getByRole("button", { name: "go" }));
		await waitFor(async () =>
			expect(await search()).toEqual({
				service: "svc-1",
				tab: "overview",
				component: "web",
			}),
		);
	});

	test("keeps the other search params it was not asked to change", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<Probe options={{ tab: "logs", component: "db" }} />,
			{ path: "/x?service=svc-1&tab=overview&q=err" },
		);
		await user.click(screen.getByRole("button", { name: "go" }));
		await waitFor(async () =>
			expect(await search()).toEqual({
				service: "svc-1",
				tab: "logs",
				q: "err",
				component: "db",
			}),
		);
	});

	test("component null clears the component from the address", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Probe options={{ component: null }} />, {
			path: "/x?service=svc-1&component=web",
		});
		await user.click(screen.getByRole("button", { name: "go" }));
		await waitFor(async () =>
			expect(await search()).toEqual({ service: "svc-1" }),
		);
		expect(screen.getByLabelText("component")).toHaveTextContent("none");
	});

	test("process null clears the process, a name sets it", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Probe options={{ process: null }} />, {
			path: "/x?service=svc-1&process=worker",
		});
		await user.click(screen.getByRole("button", { name: "go" }));
		await waitFor(async () =>
			expect(await search()).toEqual({ service: "svc-1" }),
		);
	});

	test("process sets the process", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Probe options={{ process: "worker" }} />, {
			path: "/x?service=svc-1",
		});
		await user.click(screen.getByRole("button", { name: "go" }));
		await waitFor(async () =>
			expect(await search()).toEqual({ service: "svc-1", process: "worker" }),
		);
	});

	test("changing the service or tab resets the Settings section unless one is given", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Probe options={{ tab: "overview" }} />, {
			path: "/x?service=svc-1&tab=settings&section=compose",
		});
		await user.click(screen.getByRole("button", { name: "go" }));
		await waitFor(async () =>
			expect(await search()).toEqual({ service: "svc-1", tab: "overview" }),
		);
	});

	test("tab settings with a section opens that section", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<Probe options={{ tab: "settings", section: "compose" }} />,
			{ path: "/x?service=svc-1" },
		);
		await user.click(screen.getByRole("button", { name: "go" }));
		await waitFor(async () =>
			expect(await search()).toEqual({
				service: "svc-1",
				tab: "settings",
				section: "compose",
			}),
		);
	});

	test("changing only the component leaves the section alone", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Probe options={{ component: "web" }} />, {
			path: "/x?service=svc-1&tab=settings&section=compose",
		});
		await user.click(screen.getByRole("button", { name: "go" }));
		await waitFor(async () =>
			expect(await search()).toEqual({
				service: "svc-1",
				tab: "settings",
				section: "compose",
				component: "web",
			}),
		);
	});

	test("an empty show() keeps the address as it is", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Probe options={{}} />, {
			path: "/x?service=svc-1&tab=logs",
		});
		await user.click(screen.getByRole("button", { name: "go" }));
		expect(await search()).toEqual({ service: "svc-1", tab: "logs" });
	});
});
