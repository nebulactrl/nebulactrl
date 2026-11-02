import { describe, expect, mock, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { DeliveriesDialog } from "./deliveries-dialog";
import { channel, delivery } from "./test-fixtures";

const PATH = "/notifications/channels/c1/deliveries";

function Reopenable() {
	const [open, setOpen] = useState(true);
	return (
		<>
			<button type="button" onClick={() => setOpen(true)}>
				reopen
			</button>
			<DeliveriesDialog
				channel={open ? channel() : null}
				onClose={() => setOpen(false)}
			/>
		</>
	);
}

describe("DeliveriesDialog", () => {
	test("stays closed and sends no request without a channel", async () => {
		const api = stubApi({});
		await renderWithProviders(
			<DeliveriesDialog channel={null} onClose={() => {}} />,
		);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(api.calls).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});

	test("lists a channel's deliveries newest page first with their status, attempts and response code", async () => {
		const api = stubApi({
			[`GET ${PATH}`]: {
				body: {
					items: [
						delivery({
							id: "d1",
							title: "checkout failed to deploy",
							status: "delivered",
							deliveredAt: "2026-10-26T10:00:05Z",
							responseCode: 200,
							attempts: 1,
						}),
						delivery({
							id: "d2",
							title: "api is down",
							status: "retrying",
							attempts: 2,
							nextAttemptAt: "2026-10-26T10:05:00Z",
							lastError: "dial tcp: connection refused",
						}),
						delivery({
							id: "d3",
							title: "Test message",
							status: "failed",
							isTest: true,
							attempts: 6,
							lastError: "HTTP 500",
						}),
						delivery({
							id: "d4",
							title: "queued thing",
							status: "pending",
							attempts: 0,
						}),
					],
				},
			},
		});
		await renderWithProviders(
			<DeliveriesDialog channel={channel()} onClose={() => {}} />,
		);
		expect(
			await screen.findByText("Delivery log · Ops Slack"),
		).toBeInTheDocument();
		const rows = await screen.findAllByRole("listitem");
		expect(rows).toHaveLength(4);
		const [delivered, retrying, failed, pending] = rows as [
			HTMLElement,
			HTMLElement,
			HTMLElement,
			HTMLElement,
		];
		expect(delivered).toHaveTextContent("Delivered");
		expect(delivered).toHaveTextContent("checkout failed to deploy");
		expect(delivered).toHaveTextContent("1 of 6 attempts");
		expect(delivered).toHaveTextContent("response 200");
		expect(delivered).toHaveTextContent("sent");
		expect(delivered).not.toHaveTextContent("test");
		expect(retrying).toHaveTextContent("Retrying");
		expect(retrying).toHaveTextContent("2 of 6 attempts");
		expect(retrying).toHaveTextContent("next try");
		expect(retrying).toHaveTextContent("dial tcp: connection refused");
		expect(retrying).not.toHaveTextContent("sent");
		expect(failed).toHaveTextContent("Failed");
		expect(within(failed).getByText("test")).toBeInTheDocument();
		expect(failed).toHaveTextContent("HTTP 500");
		expect(pending).toHaveTextContent("Queued");
		expect(pending).toHaveTextContent("not tried yet");
		expect(pending).not.toHaveTextContent("response");
		expect(api.calls[0]?.query.get("limit")).toBe("30");
		expect(api.calls[0]?.query.has("status")).toBe(false);
		expect(api.unmatched).toEqual([]);
	});

	test("an empty log explains when deliveries appear", async () => {
		stubApi({ [`GET ${PATH}`]: { body: { items: null } } });
		await renderWithProviders(
			<DeliveriesDialog channel={channel()} onClose={() => {}} />,
		);
		expect(await screen.findByText("Nothing sent yet")).toBeInTheDocument();
		expect(screen.queryByRole("list")).not.toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: /Load older/ }),
		).not.toBeInTheDocument();
	});

	test("filtering by status asks the server for that status and words an empty result differently", async () => {
		const api = stubApi({
			[`GET ${PATH}`]: (request) => {
				const status = new URL(request.url).searchParams.get("status");
				return Response.json({
					items:
						status === "failed"
							? []
							: [delivery({ id: "d1", title: "all good" })],
				});
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<DeliveriesDialog channel={channel()} onClose={() => {}} />,
		);
		expect(await screen.findByText("all good")).toBeInTheDocument();
		const filter = screen.getByRole("group", { name: "Filter by status" });
		expect(within(filter).getByRole("button", { name: "All" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		await user.click(within(filter).getByRole("button", { name: "Failed" }));
		expect(
			await screen.findByText("Nothing in this state"),
		).toBeInTheDocument();
		expect(
			screen.getByText(
				"No delivery of this channel is in that state right now.",
			),
		).toBeInTheDocument();
		expect(
			api.calls.some((call) => call.query.get("status") === "failed"),
		).toBe(true);
		expect(api.unmatched).toEqual([]);
	});

	test("Load older fetches the next page with the cursor and appends it", async () => {
		const api = stubApi({
			[`GET ${PATH}`]: (request) => {
				const cursor = new URL(request.url).searchParams.get("cursor");
				return Response.json(
					cursor
						? { items: [delivery({ id: "d2", title: "older one" })] }
						: {
								items: [delivery({ id: "d1", title: "newer one" })],
								nextCursor: "cur-1",
							},
				);
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<DeliveriesDialog channel={channel()} onClose={() => {}} />,
		);
		expect(await screen.findByText("newer one")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: /Load older/ }));
		expect(await screen.findByText("older one")).toBeInTheDocument();
		expect(screen.getByText("newer one")).toBeInTheDocument();
		expect(api.calls.at(-1)?.query.get("cursor")).toBe("cur-1");
		await waitFor(() =>
			expect(
				screen.queryByRole("button", { name: /Load older/ }),
			).not.toBeInTheDocument(),
		);
	});

	test("a failed load offers a retry that recovers", async () => {
		let attempts = 0;
		stubApi({
			[`GET ${PATH}`]: () => {
				attempts += 1;
				return attempts === 1
					? Response.json(
							{ status: 500, title: "x", detail: "boom", code: "internal" },
							{ status: 500 },
						)
					: Response.json({ items: [delivery({ title: "back again" })] });
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<DeliveriesDialog channel={channel()} onClose={() => {}} />,
		);
		expect(
			await screen.findByText("The delivery log did not load"),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Try again" }));
		expect(await screen.findByText("back again")).toBeInTheDocument();
	});

	test("Done closes the log", async () => {
		stubApi({ [`GET ${PATH}`]: { body: { items: [] } } });
		const onClose = mock(() => {});
		const user = userEvent.setup();
		await renderWithProviders(
			<DeliveriesDialog channel={channel()} onClose={onClose} />,
		);
		await user.click(await screen.findByRole("button", { name: "Done" }));
		expect(onClose).toHaveBeenCalledTimes(1);
	});

	test("dismissing with Escape closes the log, and reopening starts on All again", async () => {
		const api = stubApi({
			[`GET ${PATH}`]: { body: { items: [delivery({ title: "one" })] } },
		});
		const user = userEvent.setup();
		await renderWithProviders(<Reopenable />);
		const filter = await screen.findByRole("group", {
			name: "Filter by status",
		});
		await user.click(within(filter).getByRole("button", { name: "Retrying" }));
		await waitFor(() =>
			expect(
				api.calls.some((call) => call.query.get("status") === "retrying"),
			).toBe(true),
		);
		await user.keyboard("{Escape}");
		await waitFor(() =>
			expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
		);
		await user.click(screen.getByRole("button", { name: "reopen" }));
		const again = await screen.findByRole("group", {
			name: "Filter by status",
		});
		expect(within(again).getByRole("button", { name: "All" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
	});
});
