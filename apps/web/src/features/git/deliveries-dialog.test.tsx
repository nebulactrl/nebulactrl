import { describe, expect, mock, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { WebhookDeliveryView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, type Overrides } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { DeliveriesDialog } from "./deliveries-dialog";
import { connection } from "./test-fixtures";

function delivery(
	over: Overrides<WebhookDeliveryView> = {},
): WebhookDeliveryView {
	return make("WebhookDeliveryView", {
		id: 1,
		branch: "main",
		commitSha: "bffeb74224043ba2feb48e12f2ac4a0a9de3e04c",
		deliveryId: "d-123",
		deployments: null,
		event: "push",
		headers: {
			"X-Hub-Signature-256": "[redacted]",
			"Content-Type": "application/json",
		},
		httpStatus: 202,
		outcome: "processed",
		payloadBytes: 1534,
		receivedAt: new Date().toISOString(),
		repo: "acme/api",
		summary: "Deployed api",
		...over,
	});
}

const route = "GET /git/connections/:id/deliveries";

describe("DeliveriesDialog", () => {
	test("is closed and silent without a connection", async () => {
		const api = stubApi({});
		await renderWithProviders(
			<DeliveriesDialog connection={null} onClose={() => {}} />,
		);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	test("requests the first page of the connection's log", async () => {
		const api = stubApi({
			[route]: {
				body: make("PageWebhookDeliveryView", { items: [delivery()] }),
			},
		});
		await renderWithProviders(
			<DeliveriesDialog
				connection={connection({ id: "c9" })}
				onClose={() => {}}
			/>,
		);
		expect(await screen.findByText("Deployed api")).toBeInTheDocument();
		expect(api.calls[0]).toMatchObject({
			method: "GET",
			path: "/git/connections/c9/deliveries",
		});
		expect(api.calls[0]?.query.get("limit")).toBe("30");
		expect(api.calls[0]?.query.has("cursor")).toBe(false);
		expect(
			screen.getByRole("dialog", { name: /Webhook deliveries · Acme GitHub/ }),
		).toBeInTheDocument();
	});

	test("a delivery shows outcome, event, repo@branch, short sha and what it started", async () => {
		stubApi({
			[route]: {
				body: make("PageWebhookDeliveryView", {
					items: [
						delivery({ deployments: ["dep1"] }),
						delivery({
							id: 2,
							repo: "acme/web",
							deployments: ["a", "b"],
							summary: "Two",
						}),
					],
				}),
			},
		});
		await renderWithProviders(
			<DeliveriesDialog connection={connection()} onClose={() => {}} />,
		);
		expect(
			await screen.findByText("acme/api@main · bffeb74"),
		).toBeInTheDocument();
		expect(screen.getAllByText("Processed")).toHaveLength(2);
		expect(screen.getAllByText("push")).toHaveLength(2);
		expect(
			screen.getByText(/Deployed api Started 1 deployment\./),
		).toBeInTheDocument();
		expect(screen.getByText(/Two Started 2 deployments\./)).toBeInTheDocument();
	});

	test.each([
		["processed", "Processed"],
		["ignored", "Ignored"],
		["replayed", "Replay rejected"],
		["rejected", "Rejected"],
		["error", "Error"],
	] as const)("outcome %s reads %s", async (outcome, label) => {
		stubApi({
			[route]: {
				body: make("PageWebhookDeliveryView", {
					items: [delivery({ outcome })],
				}),
			},
		});
		await renderWithProviders(
			<DeliveriesDialog connection={connection()} onClose={() => {}} />,
		);
		expect(await screen.findByText(label)).toBeInTheDocument();
	});

	test("an unknown outcome is shown as the server sent it", async () => {
		stubApi({
			// A function reply: the contract lists no "queued" outcome, a newer
			// server might send one.
			[route]: () =>
				Response.json({
					items: [
						{
							...delivery(),
							outcome: "queued" as WebhookDeliveryView["outcome"],
						},
					],
				}),
		});
		await renderWithProviders(
			<DeliveriesDialog connection={connection()} onClose={() => {}} />,
		);
		expect(await screen.findByText("queued")).toBeInTheDocument();
	});

	test("sparse deliveries use placeholders instead of blanks", async () => {
		stubApi({
			[route]: {
				body: make("PageWebhookDeliveryView", {
					items: [
						delivery({
							event: "",
							repo: "",
							branch: "",
							commitSha: "",
							summary: "",
							deployments: [],
						}),
					],
				}),
			},
		});
		await renderWithProviders(
			<DeliveriesDialog connection={connection()} onClose={() => {}} />,
		);
		expect(await screen.findByText("—")).toBeInTheDocument();
		expect(screen.getByText("No details.")).toBeInTheDocument();
		expect(screen.queryByText(/Started/)).not.toBeInTheDocument();
		expect(screen.queryByText(/acme\/api/)).not.toBeInTheDocument();
	});

	test("a repo without branch or commit is shown bare", async () => {
		stubApi({
			[route]: {
				body: make("PageWebhookDeliveryView", {
					items: [delivery({ branch: "", commitSha: "" })],
				}),
			},
		});
		await renderWithProviders(
			<DeliveriesDialog connection={connection()} onClose={() => {}} />,
		);
		expect(await screen.findByText("acme/api")).toBeInTheDocument();
	});

	test("the request line expands to the delivery id and redacted headers, sorted", async () => {
		stubApi({
			[route]: {
				body: make("PageWebhookDeliveryView", { items: [delivery()] }),
			},
		});
		await renderWithProviders(
			<DeliveriesDialog connection={connection()} onClose={() => {}} />,
		);
		const toggle = await screen.findByRole("button", {
			name: /Request · HTTP 202 · 1534 bytes/,
		});
		expect(toggle).toHaveAttribute("aria-expanded", "false");
		const user = userEvent.setup();
		await user.click(toggle);
		expect(toggle).toHaveAttribute("aria-expanded", "true");
		const list = toggle.parentElement?.querySelector("dl");
		expect(list).not.toBeNull();
		const keys = within(list as HTMLElement)
			.getAllByText(/^(delivery|Content-Type|X-Hub-Signature-256)$/)
			.map((e) => e.textContent);
		expect(keys).toEqual(["delivery", "Content-Type", "X-Hub-Signature-256"]);
		expect(within(list as HTMLElement).getByText("d-123")).toBeInTheDocument();
		expect(
			within(list as HTMLElement).getByText("[redacted]"),
		).toBeInTheDocument();
		await user.click(toggle);
		expect(toggle).toHaveAttribute("aria-expanded", "false");
	});

	test("a delivery with no id or headers expands to an empty list", async () => {
		stubApi({
			[route]: {
				body: make("PageWebhookDeliveryView", {
					items: [delivery({ deliveryId: "", headers: {} })],
				}),
			},
		});
		await renderWithProviders(
			<DeliveriesDialog connection={connection()} onClose={() => {}} />,
		);
		const toggle = await screen.findByRole("button", { name: /Request/ });
		await userEvent.setup().click(toggle);
		expect(screen.queryByText("delivery")).not.toBeInTheDocument();
	});

	test("an empty log says nothing has arrived", async () => {
		stubApi({
			[route]: { body: make("PageWebhookDeliveryView", { items: null }) },
		});
		await renderWithProviders(
			<DeliveriesDialog connection={connection()} onClose={() => {}} />,
		);
		expect(await screen.findByText("No deliveries yet")).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Load older" }),
		).not.toBeInTheDocument();
	});

	test("a failed load offers a retry that asks again", async () => {
		let attempts = 0;
		const api = stubApi({
			[route]: () => {
				attempts++;
				return attempts === 1
					? Response.json({ title: "Forbidden", status: 500 }, { status: 500 })
					: Response.json({ items: [delivery({ summary: "Recovered" })] });
			},
		});
		await renderWithProviders(
			<DeliveriesDialog connection={connection()} onClose={() => {}} />,
		);
		expect(
			await screen.findByText("The delivery log did not load"),
		).toBeInTheDocument();
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: /retry|try again/i }));
		expect(await screen.findByText("Recovered")).toBeInTheDocument();
		expect(api.calls).toHaveLength(2);
	});

	test("Load older fetches the next page with the cursor and appends it", async () => {
		const api = stubApi({
			[route]: (req) => {
				const cursor = new URL(req.url).searchParams.get("cursor");
				return Response.json(
					cursor
						? { items: [delivery({ id: 2, summary: "Older one" })] }
						: { items: [delivery({ summary: "Newest" })], nextCursor: "cur-2" },
				);
			},
		});
		await renderWithProviders(
			<DeliveriesDialog connection={connection()} onClose={() => {}} />,
		);
		await screen.findByText("Newest");
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Load older" }));
		expect(await screen.findByText("Older one")).toBeInTheDocument();
		expect(screen.getByText("Newest")).toBeInTheDocument();
		expect(api.calls[1]?.query.get("cursor")).toBe("cur-2");
		expect(api.calls[1]?.query.get("limit")).toBe("30");
		await waitFor(() =>
			expect(
				screen.queryByRole("button", { name: "Load older" }),
			).not.toBeInTheDocument(),
		);
	});

	test("Close calls onClose", async () => {
		stubApi({ [route]: { body: make("PageWebhookDeliveryView") } });
		const onClose = mock();
		await renderWithProviders(
			<DeliveriesDialog connection={connection()} onClose={onClose} />,
		);
		await screen.findByText("No deliveries yet");
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Close" }));
		expect(onClose).toHaveBeenCalledTimes(1);
	});
});
