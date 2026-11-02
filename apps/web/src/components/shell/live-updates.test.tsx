import { describe, expect, test } from "bun:test";
import { QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { testQueryClient } from "#/test/render";
import { LiveUpdates } from "./live-updates";

function eventsResponse(body: ReadableStream<Uint8Array>) {
	return new Response(body, {
		headers: { "Content-Type": "text/event-stream" },
	});
}

function mount() {
	return render(
		<QueryClientProvider client={testQueryClient()}>
			<LiveUpdates />
		</QueryClientProvider>,
	);
}

describe("LiveUpdates", () => {
	test("says nothing while the feed connects or is live", async () => {
		let requested = 0;
		const encoder = new TextEncoder();
		globalThis.fetch = (async () => {
			requested++;
			return eventsResponse(
				new ReadableStream({
					start(controller) {
						controller.enqueue(encoder.encode('data: {"type":"ping"}\n\n'));
					},
				}),
			);
		}) as unknown as typeof fetch;
		const view = mount();
		await waitFor(() => expect(requested).toBeGreaterThan(0));
		expect(screen.queryByText(/Live updates paused/)).toBeNull();
		view.unmount();
	});

	test("opens the /events feed", async () => {
		const urls: string[] = [];
		globalThis.fetch = (async (input: RequestInfo | URL) => {
			urls.push(new Request(input).url);
			return new Response("nope", { status: 500 });
		}) as unknown as typeof fetch;
		const view = mount();
		await waitFor(() =>
			expect(urls.some((url) => url.endsWith("/events"))).toBe(true),
		);
		view.unmount();
	});

	test("warns that updates are paused when the feed cannot connect, and goes quiet again on unmount", async () => {
		globalThis.fetch = (async () =>
			new Response("nope", { status: 500 })) as unknown as typeof fetch;
		const view = mount();
		const pill = await screen.findByText("Live updates paused — retrying");
		expect(pill).toHaveAttribute("role", "status");
		view.unmount();
		expect(screen.queryByText("Live updates paused — retrying")).toBeNull();
	});
});
