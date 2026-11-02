import { describe, expect, it } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ExportView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { ConnectionVariables } from "./connection-variables";

const OPEN = "$" + "{{";

function entry(over: Partial<ExportView> = {}): ExportView {
	return {
		key: "DATABASE_URL",
		description: "",
		primary: false,
		reference: `${OPEN}postgres.DATABASE_URL}}`,
		secret: false,
		...over,
	};
}

const renderIt = (exports: ExportView[]) =>
	renderWithProviders(
		<ConnectionVariables serviceId="db1" exports={exports} />,
	);

const reveal = (value: string, extra: Record<string, unknown> = {}) => ({
	"POST /services/db1/database/reveal": (request: Request) =>
		request
			.clone()
			.json()
			.then((body: { key: string }) =>
				Response.json({
					key: body.key,
					value,
					expiresAt: new Date(Date.now() + 60_000).toISOString(),
					...extra,
				}),
			),
});

function stubClipboard(writeText: (text: string) => Promise<void>) {
	const original = Object.getOwnPropertyDescriptor(navigator, "clipboard");
	Object.defineProperty(navigator, "clipboard", {
		configurable: true,
		value: { writeText },
	});
	return () => {
		if (original) Object.defineProperty(navigator, "clipboard", original);
		else Reflect.deleteProperty(navigator, "clipboard");
	};
}

describe("ConnectionVariables", () => {
	it("renders nothing when the database offers no exports", async () => {
		const { container } = await renderIt([]);
		expect(container.querySelector("section")).toBeNull();
		expect(screen.queryByText("Connection variables")).toBeNull();
	});

	it("lists each export with its reference, a secret badge and a primary badge", async () => {
		await renderIt([
			entry({ key: "DATABASE_URL", primary: true, secret: true }),
			entry({
				key: "DATABASE_HOST",
				reference: `${OPEN}postgres.DATABASE_HOST}}`,
				description: "Private hostname",
			}),
		]);
		const list = screen.getByRole("list", { name: "Connection variables" });
		const rows = within(list).getAllByRole("listitem");
		expect(rows).toHaveLength(2);
		const url = within(rows[0] as HTMLElement);
		expect(url.getByText("secret")).toBeInTheDocument();
		expect(url.getByText("primary")).toBeInTheDocument();
		expect(url.getByText(`${OPEN}postgres.DATABASE_URL}}`)).toBeInTheDocument();
		const host = within(rows[1] as HTMLElement);
		expect(host.queryByText("secret")).toBeNull();
		expect(host.queryByText("primary")).toBeNull();
		expect(
			host.getByText(`${OPEN}postgres.DATABASE_HOST}}`),
		).toBeInTheDocument();
		// Only a secret can be revealed; every export can be copied as a reference.
		expect(
			url.getByRole("button", { name: "Show DATABASE_URL for 60 seconds" }),
		).toBeInTheDocument();
		expect(
			host.queryByRole("button", { name: /Show DATABASE_HOST/ }),
		).toBeNull();
		expect(
			host.getByRole("button", { name: "Copy reference DATABASE_HOST" }),
		).toBeInTheDocument();
		// The plain explanation without the primary/replica note.
		expect(screen.queryByText(/is always the/)).toBeNull();
	});

	it("shows the description of an export as its tooltip", async () => {
		const user = userEvent.setup();
		await renderIt([
			entry({ key: "DATABASE_HOST", description: "Private hostname" }),
		]);
		await user.hover(screen.getByRole("button", { name: "DATABASE_HOST" }));
		expect(await screen.findByText("Private hostname")).toBeInTheDocument();
	});

	it("orders the service's own exports, then writer keys, then reader keys, with role badges", async () => {
		await renderIt([
			entry({ key: "DATABASE_READ_URL" }),
			entry({ key: "DATABASE_WRITE_URL" }),
			entry({ key: "DATABASE_URL" }),
		]);
		const rows = within(
			screen.getByRole("list", { name: "Connection variables" }),
		).getAllByRole("listitem");
		expect(
			rows.map((row) => row.querySelector("span.truncate")?.textContent),
		).toEqual(["DATABASE_URL", "DATABASE_WRITE_URL", "DATABASE_READ_URL"]);
		expect(
			within(rows[1] as HTMLElement).getByText("writer"),
		).toBeInTheDocument();
		expect(
			within(rows[2] as HTMLElement).getByText("reader"),
		).toBeInTheDocument();
		expect(screen.getByText(/is always the/)).toBeInTheDocument();
	});

	it("Show reveals the value in the row, Hide removes it", async () => {
		const user = userEvent.setup();
		const api = stubApi(reveal("postgres://u:pw@db/app"));
		await renderIt([entry({ secret: true })]);
		await user.click(
			screen.getByRole("button", { name: "Show DATABASE_URL for 60 seconds" }),
		);
		const field = await screen.findByLabelText("DATABASE_URL (revealed)");
		expect(field).toHaveValue("postgres://u:pw@db/app");
		expect(api.calls[0]?.path).toBe("/services/db1/database/reveal");
		expect(api.calls[0]?.body).toEqual({ key: "DATABASE_URL" });
		// While shown, a second reveal is locked.
		expect(
			screen.getByRole("button", { name: "Show DATABASE_URL for 60 seconds" }),
		).toBeDisabled();
		await user.click(screen.getByRole("button", { name: "Hide" }));
		expect(screen.queryByLabelText("DATABASE_URL (revealed)")).toBeNull();
		expect(
			screen.getByRole("button", { name: "Show DATABASE_URL for 60 seconds" }),
		).toBeEnabled();
	});

	it("Copy puts the revealed value on the clipboard and shows nothing in the row", async () => {
		const user = userEvent.setup();
		const written: string[] = [];
		const restore = stubClipboard(async (text) => {
			written.push(text);
		});
		try {
			stubApi(reveal("s3cret-value"));
			await renderIt([entry({ secret: true })]);
			await user.click(
				screen.getByRole("button", { name: "Copy the value of DATABASE_URL" }),
			);
			expect(await screen.findByText("Copied")).toBeInTheDocument();
			expect(written).toEqual(["s3cret-value"]);
			expect(screen.queryByLabelText("DATABASE_URL (revealed)")).toBeNull();
		} finally {
			restore();
		}
	});

	it("tells how to proceed when the browser blocks the clipboard", async () => {
		const user = userEvent.setup();
		const restore = stubClipboard(async () => {
			throw new Error("denied");
		});
		try {
			stubApi(reveal("s3cret-value"));
			await renderIt([entry({ secret: true })]);
			await user.click(
				screen.getByRole("button", { name: "Copy the value of DATABASE_URL" }),
			);
			expect(await screen.findByText("Nothing was copied")).toBeInTheDocument();
			expect(screen.getByText(/Use Show for 60 s instead/)).toBeInTheDocument();
		} finally {
			restore();
		}
	});

	it("explains a failed reveal and leaves the buttons usable", async () => {
		const user = userEvent.setup();
		stubApi({
			"POST /services/db1/database/reveal": problem(403, {
				code: "forbidden",
				title: "Forbidden",
				detail: "reveal needs the admin role",
			}),
		});
		await renderIt([entry({ secret: true })]);
		await user.click(
			screen.getByRole("button", { name: "Show DATABASE_URL for 60 seconds" }),
		);
		expect(
			await screen.findByText("Could not reveal DATABASE_URL"),
		).toBeInTheDocument();
		expect(screen.getByText(/reveal needs the admin role/)).toBeInTheDocument();
		await waitFor(() =>
			expect(
				screen.getByRole("button", {
					name: "Show DATABASE_URL for 60 seconds",
				}),
			).toBeEnabled(),
		);
		expect(screen.queryByLabelText("DATABASE_URL (revealed)")).toBeNull();
	});

	it("is the target of the #connection anchor", async () => {
		await renderIt([entry()]);
		const section = screen.getByRole("region", {
			name: "Connection variables",
		});
		expect(section).toHaveAttribute("id", "connection");
	});
});
