import { afterEach, describe, expect, test } from "bun:test";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { GroupUrls } from "./group-urls";

const REVEAL = "POST /services/:id/database/reveal";
const realClipboard = Object.getOwnPropertyDescriptor(navigator, "clipboard");

/**
 * Records what is written; `false` removes the Clipboard API (plain http).
 * Call after `userEvent.setup()`, which installs a clipboard of its own.
 */
function stubClipboard(available = true) {
	const written: string[] = [];
	Object.defineProperty(navigator, "clipboard", {
		configurable: true,
		value: available
			? {
					writeText: async (text: string) => {
						written.push(text);
					},
				}
			: undefined,
	});
	return written;
}

afterEach(() => {
	if (realClipboard)
		Object.defineProperty(navigator, "clipboard", realClipboard);
	else Reflect.deleteProperty(navigator, "clipboard");
});

const revealed = (key: string, value: string) => ({
	body: make("Revealed", {
		key,
		value,
		expiresAt: new Date(Date.now() + 60_000).toISOString(),
	}),
});

describe("GroupUrls", () => {
	test("Copy writer URL reveals DATABASE_WRITE_URL and copies the value", async () => {
		const api = stubApi({
			[REVEAL]: revealed("DATABASE_WRITE_URL", "postgres://w:pw@primary/db"),
		});
		const user = userEvent.setup();
		const written = stubClipboard();
		await renderWithProviders(<GroupUrls serviceId="svc-1" />);
		await user.click(screen.getByRole("button", { name: "Copy writer URL" }));
		expect((await screen.findAllByText("Copied")).length).toBeGreaterThan(0);
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]).toMatchObject({
			method: "POST",
			path: "/services/svc-1/database/reveal",
			body: { key: "DATABASE_WRITE_URL" },
		});
		expect(written).toEqual(["postgres://w:pw@primary/db"]);
		expect(api.unmatched).toEqual([]);
	});

	test("Copy reader URL reveals DATABASE_READ_URL", async () => {
		const api = stubApi({
			[REVEAL]: revealed("DATABASE_READ_URL", "postgres://r:pw@replica/db"),
		});
		const user = userEvent.setup();
		const written = stubClipboard();
		await renderWithProviders(<GroupUrls serviceId="svc-1" />);
		await user.click(screen.getByRole("button", { name: "Copy reader URL" }));
		expect((await screen.findAllByText("Copied")).length).toBeGreaterThan(0);
		expect(api.calls[0]?.body).toEqual({ key: "DATABASE_READ_URL" });
		expect(written).toEqual(["postgres://r:pw@replica/db"]);
	});

	test("a refused reveal is reported with the server's reason and copies nothing", async () => {
		stubApi({
			[REVEAL]: problem(403, {
				title: "Forbidden",
				detail: "You may not reveal secrets here.",
				code: "forbidden",
			}),
		});
		const user = userEvent.setup();
		const written = stubClipboard();
		await renderWithProviders(<GroupUrls serviceId="svc-1" />);
		await user.click(screen.getByRole("button", { name: "Copy reader URL" }));
		expect(
			await screen.findByText("Could not reveal DATABASE_READ_URL"),
		).toBeInTheDocument();
		expect(
			screen.getByText("You may not reveal secrets here."),
		).toBeInTheDocument();
		expect(written).toEqual([]);
		// The buttons are usable again.
		expect(
			screen.getByRole("button", { name: "Copy reader URL" }),
		).toBeEnabled();
	});

	test("a browser without clipboard access says nothing was copied", async () => {
		stubApi({ [REVEAL]: revealed("DATABASE_WRITE_URL", "postgres://x") });
		const user = userEvent.setup();
		stubClipboard(false);
		await renderWithProviders(<GroupUrls serviceId="svc-1" />);
		await user.click(screen.getByRole("button", { name: "Copy writer URL" }));
		expect(
			(await screen.findAllByText("Nothing was copied")).length,
		).toBeGreaterThan(0);
	});

	test("both buttons are locked while a reveal is in flight", async () => {
		let release: () => void = () => {};
		const gate = new Promise<void>((resolve) => {
			release = resolve;
		});
		stubApi({
			[REVEAL]: async () => {
				await gate;
				return Response.json({
					key: "DATABASE_WRITE_URL",
					value: "postgres://x",
					expiresAt: new Date(Date.now() + 60_000).toISOString(),
				});
			},
		});
		const user = userEvent.setup();
		stubClipboard();
		await renderWithProviders(<GroupUrls serviceId="svc-1" />);
		await user.click(screen.getByRole("button", { name: /Copy writer URL/ }));
		expect(
			screen.getByRole("button", { name: /Copy writer URL/ }),
		).toBeDisabled();
		expect(
			screen.getByRole("button", { name: /Copy reader URL/ }),
		).toBeDisabled();
		release();
		expect((await screen.findAllByText("Copied")).length).toBeGreaterThan(0);
		expect(
			screen.getByRole("button", { name: /Copy writer URL/ }),
		).toBeEnabled();
	});
});
