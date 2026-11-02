import { afterEach, describe, expect, spyOn, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import type { UpdateView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { MARKER_KEY } from "./update-model";
import { useUpdateNotice } from "./use-system-update";

const view = (over: Partial<UpdateView> = {}): UpdateView => ({
	available: false,
	canUpdate: false,
	checkEnabled: true,
	current: "0.1.0",
	isAdmin: true,
	latest: "0.1.0",
	manualCommand: "",
	...over,
});

function Chip() {
	const { chip } = useUpdateNotice();
	return <p data-testid="chip">{chip ?? "no chip"}</p>;
}

let reload: ReturnType<typeof spyOn> | undefined;

/** `window.location.reload` is the page's hand-over to the new UI. */
function stubReload() {
	reload = spyOn(window.location, "reload").mockImplementation(() => {});
	return reload;
}

afterEach(() => {
	reload?.mockRestore();
	reload = undefined;
	window.sessionStorage.clear();
});

// The hook remembers the version this page first heard of, once per page load
// (module state), so the tests below run in order: the first one is the page's
// first answer (0.1.0) and the later ones play out what follows.
describe("useUpdateNotice", () => {
	test("the first answer is the page's version: no reload, no toast, no chip", async () => {
		const spy = stubReload();
		stubApi({ "GET /system/update": { body: view() } });
		await renderWithProviders(<Chip />);
		await waitFor(() =>
			expect(screen.getByTestId("chip")).toHaveTextContent("no chip"),
		);
		expect(spy).not.toHaveBeenCalled();
		expect(window.sessionStorage.getItem(MARKER_KEY)).toBeNull();
	});

	test("a newer release shows the chip for an admin only", async () => {
		stubApi({
			"GET /system/update": {
				body: view({ available: true, latest: "0.2.0", canUpdate: true }),
			},
		});
		await renderWithProviders(<Chip />);
		expect(await screen.findByText("v0.2.0 available")).toBeInTheDocument();
	});

	test("a non-admin gets no chip for the same release", async () => {
		stubApi({
			"GET /system/update": {
				body: view({ available: true, latest: "0.2.0", isAdmin: false }),
			},
		});
		await renderWithProviders(<Chip />);
		await waitFor(() =>
			expect(screen.getByTestId("chip")).toHaveTextContent("no chip"),
		);
	});

	test("a different control-plane version reloads the page once and leaves a marker", async () => {
		const spy = stubReload();
		stubApi({ "GET /system/update": { body: view({ current: "0.2.0" }) } });
		await renderWithProviders(<Chip />);
		await waitFor(() => expect(spy).toHaveBeenCalledTimes(1));
		expect(
			JSON.parse(window.sessionStorage.getItem(MARKER_KEY) ?? "null"),
		).toEqual({
			version: "0.2.0",
			toasted: false,
		});
	});

	test("after the reload it says Updated once and does not reload again", async () => {
		const spy = stubReload();
		window.sessionStorage.setItem(
			MARKER_KEY,
			JSON.stringify({ version: "0.2.0", toasted: false }),
		);
		stubApi({ "GET /system/update": { body: view({ current: "v0.2.0" }) } });
		await renderWithProviders(<Chip />);
		expect(await screen.findByText("Updated to v0.2.0")).toBeInTheDocument();
		expect(spy).not.toHaveBeenCalled();
		expect(
			JSON.parse(window.sessionStorage.getItem(MARKER_KEY) ?? "null"),
		).toEqual({
			version: "0.2.0",
			toasted: true,
		});
	});

	test("a marker for the version already handled prevents a reload loop", async () => {
		const spy = stubReload();
		window.sessionStorage.setItem(
			MARKER_KEY,
			JSON.stringify({ version: "0.2.0", toasted: true }),
		);
		stubApi({ "GET /system/update": { body: view({ current: "0.2.0" }) } });
		await renderWithProviders(<Chip />);
		await waitFor(() =>
			expect(screen.getByTestId("chip")).toHaveTextContent("no chip"),
		);
		expect(spy).not.toHaveBeenCalled();
		expect(screen.queryByText(/Updated to/)).not.toBeInTheDocument();
	});

	test("blocked session storage does not stop the reload", async () => {
		const spy = stubReload();
		const descriptor = Object.getOwnPropertyDescriptor(
			window,
			"sessionStorage",
		);
		Object.defineProperty(window, "sessionStorage", {
			configurable: true,
			get() {
				throw new Error("blocked");
			},
		});
		try {
			stubApi({ "GET /system/update": { body: view({ current: "0.3.0" }) } });
			await renderWithProviders(<Chip />);
			await waitFor(() => expect(spy).toHaveBeenCalledTimes(1));
		} finally {
			if (descriptor)
				Object.defineProperty(window, "sessionStorage", descriptor);
			else Reflect.deleteProperty(window, "sessionStorage");
		}
	});
});
