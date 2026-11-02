import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { AuditEvent } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { AuditSection } from "./audit-section";

const event = (over: Partial<AuditEvent> = {}): AuditEvent => ({
	id: 1,
	action: "service.update",
	actorId: "u1",
	actorLabel: "ada@example.com",
	actorType: "user",
	createdAt: "2026-03-04T05:06:07Z",
	metadata: {},
	resourceId: "0123456789abcdef",
	resourceType: "service",
	...over,
});

function rowOf(text: string | RegExp): HTMLElement {
	const row = screen.getByText(text).closest("tr");
	if (!row) throw new Error(`no row for ${String(text)}`);
	return row;
}

describe("AuditSection", () => {
	test("lists events: who, what, which resource and the details sentence", async () => {
		stubApi({
			"GET /audit-events": {
				body: {
					items: [
						event({
							id: 1,
							action: "token.create",
							actorLabel: "ada@example.com",
							resourceType: "token",
							resourceId: "abcdef0123456789",
							metadata: { name: "ci" },
						}),
						event({
							id: 2,
							action: "deployment.cancel",
							actorType: "token",
							actorLabel: "ci-deploy",
							resourceType: "deployment",
							resourceId: "",
							metadata: {},
						}),
					],
				},
			},
		});
		await renderWithProviders(<AuditSection />);
		expect(await screen.findByText("Token created")).toBeInTheDocument();

		const created = within(rowOf("Token created"));
		expect(created.getByText("user")).toBeInTheDocument();
		expect(created.getByText("ada@example.com")).toBeInTheDocument();
		expect(created.getByText("Created:")).toBeInTheDocument();
		expect(created.getByText("Token")).toBeInTheDocument();
		// Only the first 8 characters of the resource id are shown.
		expect(created.getByText("abcdef01")).toBeInTheDocument();
		expect(created.queryByText("abcdef0123456789")).toBeNull();
		expect(
			created.getByRole("button", { name: "Copy event details" }),
		).toBeInTheDocument();

		const canceled = within(rowOf("Deployment canceled"));
		expect(canceled.getByText("token")).toBeInTheDocument();
		expect(canceled.getByText("ci-deploy")).toBeInTheDocument();
		expect(canceled.getByText("Removed or ended:")).toBeInTheDocument();
		// No metadata: a dash and no copy button.
		expect(canceled.getByText("–")).toBeInTheDocument();
		expect(
			canceled.queryByRole("button", { name: "Copy event details" }),
		).toBeNull();
	});

	test("an action's tooltip names its kind and id, and the time is absolute on hover", async () => {
		stubApi({
			"GET /audit-events": {
				body: { items: [event({ action: "service.update" })] },
			},
		});
		await renderWithProviders(<AuditSection />);
		const action = (await screen.findByText("Service updated")).closest(
			"span[title]",
		);
		expect(action).toHaveAttribute("title", "Changed (service.update)");
		expect(
			document.querySelector('[title="2026-03-04 05:06:07 UTC"]'),
		).not.toBeNull();
	});

	test("a single event reads singular and says it is all of them", async () => {
		stubApi({ "GET /audit-events": { body: { items: [event()] } } });
		await renderWithProviders(<AuditSection />);
		expect(
			await screen.findByText("1 event loaded, that is all of them."),
		).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
	});

	test("the first request asks for 50 events with no type filter", async () => {
		const api = stubApi({
			"GET /audit-events": { body: { items: [event()] } },
		});
		await renderWithProviders(<AuditSection />);
		await screen.findByText("Service updated");
		expect(api.calls[0]?.query.get("limit")).toBe("50");
		expect(api.calls[0]?.query.has("type")).toBe(false);
		expect(api.calls[0]?.query.has("cursor")).toBe(false);
	});

	test("Load more fetches the next page with the cursor and appends it", async () => {
		const api = stubApi({
			"GET /audit-events": (request) => {
				const cursor = new URL(request.url).searchParams.get("cursor");
				return Response.json(
					cursor === "c1"
						? { items: [event({ id: 2, action: "project.delete" })] }
						: {
								items: [event({ id: 1, action: "project.create" })],
								nextCursor: "c1",
							},
				);
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<AuditSection />);
		expect(await screen.findByText("1 event loaded.")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Load more" }));
		expect(
			await screen.findByText("2 events loaded, that is all of them."),
		).toBeInTheDocument();
		expect(screen.getByText("Project created")).toBeInTheDocument();
		expect(screen.getByText("Project deleted")).toBeInTheDocument();
		expect(api.calls.map((c) => c.query.get("cursor"))).toEqual([null, "c1"]);
		expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
	});

	test("a filter chip sends its action prefix and shows only that family", async () => {
		const api = stubApi({
			"GET /audit-events": (request) => {
				const type = new URL(request.url).searchParams.get("type");
				return Response.json({
					items:
						type === "token."
							? [event({ id: 3, action: "token.revoke" })]
							: [event({ id: 1, action: "service.update" })],
				});
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<AuditSection />);
		await screen.findByText("Service updated");
		const chips = within(
			screen.getByRole("group", { name: "Filter by event type" }),
		);
		expect(chips.getByRole("button", { name: "All" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		await user.click(chips.getByRole("button", { name: "Tokens" }));
		expect(await screen.findByText("Token revoked")).toBeInTheDocument();
		expect(screen.queryByText("Service updated")).toBeNull();
		expect(chips.getByRole("button", { name: "Tokens" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(chips.getByRole("button", { name: "All" })).toHaveAttribute(
			"aria-pressed",
			"false",
		);
		expect(api.calls.at(-1)?.query.get("type")).toBe("token.");
		// Going back to All drops the type again.
		await user.click(chips.getByRole("button", { name: "All" }));
		expect(await screen.findByText("Service updated")).toBeInTheDocument();
		expect(api.calls.at(-1)?.query.has("type")).toBe(false);
	});

	test("an empty log explains what lands there", async () => {
		stubApi({ "GET /audit-events": { body: { items: [] } } });
		await renderWithProviders(<AuditSection />);
		expect(await screen.findByText("Nothing recorded yet")).toBeInTheDocument();
		expect(screen.queryByRole("table")).toBeNull();
	});

	test("a null item list is an empty log", async () => {
		stubApi({ "GET /audit-events": { body: { items: null } } });
		await renderWithProviders(<AuditSection />);
		expect(await screen.findByText("Nothing recorded yet")).toBeInTheDocument();
	});

	test("an empty filter shows that family's own empty state", async () => {
		stubApi({
			"GET /audit-events": (request) => {
				const type = new URL(request.url).searchParams.get("type");
				return Response.json({
					items: type ? [] : [event()],
				});
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<AuditSection />);
		await screen.findByText("Service updated");
		await user.click(screen.getByRole("button", { name: "Secrets" }));
		expect(
			await screen.findByText("No secret access recorded yet"),
		).toBeInTheDocument();
	});

	test("a failed load says so and retries", async () => {
		let calls = 0;
		stubApi({
			"GET /audit-events": () => {
				calls += 1;
				return calls === 1
					? Response.json(
							{
								status: 403,
								title: "Forbidden",
								detail: "no",
								code: "forbidden",
							},
							{ status: 403 },
						)
					: Response.json({ items: [event()] });
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<AuditSection />);
		expect(
			await screen.findByText("The audit log did not load"),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: /try again|retry/i }));
		expect(await screen.findByText("Service updated")).toBeInTheDocument();
		await waitFor(() =>
			expect(screen.queryByText("The audit log did not load")).toBeNull(),
		);
	});
});
