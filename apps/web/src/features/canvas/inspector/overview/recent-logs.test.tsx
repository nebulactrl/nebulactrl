import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { makeService } from "../../lib/fixtures";
import { RecentLogs } from "./recent-logs";

const frame = (line: string, ts?: string) =>
	`data: ${JSON.stringify(ts ? { line, ts } : { line })}\n\n`;
const end = "event: end\ndata: {}\n\n";
const sse = (...events: string[]) =>
	new Response(events.join(""), {
		headers: { "Content-Type": "text/event-stream" },
	});
/** A stream that never answers. */
const pending = () => new Promise<Response>(() => {});

const deployed = (id = "svc-log") =>
	makeService({ id, currentDeploymentId: "dep-1" });

describe("RecentLogs", () => {
	test("shows the last five lines of the service's stream, asking the server for a five-line tail", async () => {
		const api = stubApi({
			"GET /services/:serviceId/logs": () =>
				sse(
					...["one", "two", "three", "four", "five", "six", "seven"].map((l) =>
						frame(l),
					),
					end,
				),
		});
		await renderWithProviders(<RecentLogs service={deployed()} />);
		const log = screen.getByRole("log", { name: "Recent log lines" });
		expect(await within(log).findByText("seven")).toBeInTheDocument();
		for (const shown of ["three", "four", "five", "six", "seven"]) {
			expect(within(log).getByText(shown)).toBeInTheDocument();
		}
		for (const dropped of ["one", "two"]) {
			expect(within(log).queryByText(dropped)).toBeNull();
		}
		expect(api.calls[0]?.path).toBe("/services/svc-log/logs");
		expect(api.calls[0]?.query.get("tail")).toBe("5");
		expect(api.calls[0]?.query.get("follow")).toBe("true");
		expect(api.unmatched).toEqual([]);
	});

	test("a line carries its clock time when the stream sends one", async () => {
		const ts = "2026-10-31T10:11:12Z";
		stubApi({
			"GET /services/:serviceId/logs": () =>
				sse(frame("listening on :8080", ts), frame("no timestamp"), end),
		});
		await renderWithProviders(<RecentLogs service={deployed()} />);
		const withTime = (await screen.findByText("listening on :8080"))
			.parentElement as HTMLElement;
		const clockText = new Date(ts).toLocaleTimeString([], { hour12: false });
		expect(withTime).toHaveTextContent(`${clockText}listening on :8080`);
		const without = screen.getByText("no timestamp")
			.parentElement as HTMLElement;
		expect(without.children).toHaveLength(1);
	});

	test("a timestamp that does not parse is left out", async () => {
		stubApi({
			"GET /services/:serviceId/logs": () =>
				sse(frame("odd clock", "not a date"), end),
		});
		await renderWithProviders(<RecentLogs service={deployed()} />);
		const row = (await screen.findByText("odd clock"))
			.parentElement as HTMLElement;
		expect(row).toHaveTextContent(/^odd clock$/);
	});

	test("warnings and errors are told apart from plain lines", async () => {
		stubApi({
			"GET /services/:serviceId/logs": () =>
				sse(
					frame("INFO started"),
					frame("WARN slow query"),
					frame("ERROR connection refused"),
					frame("plain output"),
					end,
				),
		});
		await renderWithProviders(<RecentLogs service={deployed()} />);
		const info = await screen.findByText("INFO started");
		const warn = screen.getByText("WARN slow query");
		const error = screen.getByText("ERROR connection refused");
		const plain = screen.getByText("plain output");
		// Level only shows as colour, so the classes are the observable.
		expect(info).toHaveClass("text-info");
		expect(warn).toHaveClass("text-warn");
		expect(error).toHaveClass("text-bad");
		expect(plain).not.toHaveClass("text-info", "text-warn", "text-bad");
	});

	test("a service that was never deployed says logs wait for a deploy, and opens no stream", async () => {
		const api = stubApi({});
		await renderWithProviders(
			<RecentLogs service={makeService({ currentDeploymentId: null })} />,
		);
		expect(
			screen.getByText("Logs appear once the service is deployed."),
		).toBeInTheDocument();
		expect(api.calls).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});

	test("while the stream connects it says it is waiting", async () => {
		stubApi({ "GET /services/:serviceId/logs": pending });
		await renderWithProviders(<RecentLogs service={deployed()} />);
		expect(screen.getByText("Waiting for log output…")).toBeInTheDocument();
	});

	test("a stream that ended without output says there is none yet", async () => {
		stubApi({ "GET /services/:serviceId/logs": () => sse(end) });
		await renderWithProviders(<RecentLogs service={deployed()} />);
		expect(await screen.findByText("No log output yet.")).toBeInTheDocument();
	});
});
