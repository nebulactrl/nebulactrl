import { describe, expect, test } from "bun:test";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { RunOutput } from "./run-output";

const sse = (...events: string[]) =>
	new Response(events.join(""), {
		headers: { "Content-Type": "text/event-stream" },
	});
const frame = (line: string) => `data: ${JSON.stringify({ line })}\n\n`;
const end = "event: end\ndata: {}\n\n";
/** A stream that never answers until it is aborted: the run is still connecting. */
const pending = (request: Request) =>
	new Promise<Response>((_, reject) => {
		request.signal.addEventListener("abort", () =>
			reject(new DOMException("aborted", "AbortError")),
		);
	});

describe("RunOutput", () => {
	test("counts the lines of a finished run and asks for its stream with follow", async () => {
		const api = stubApi({
			"GET /task-runs/:runId/logs": () =>
				sse(frame("first"), frame("second"), end),
		});
		await renderWithProviders(
			<RunOutput runId="ro-1" active={false} label="Output of nightly" />,
		);
		expect(await screen.findByText("2 lines")).toBeInTheDocument();
		expect(screen.getByText("Finished")).toBeInTheDocument();
		expect(api.calls[0]?.path).toBe("/task-runs/ro-1/logs");
		expect(api.calls[0]?.query.get("follow")).toBe("true");
		expect(api.unmatched).toEqual([]);
	});

	test("uses the singular for one line", async () => {
		stubApi({
			"GET /task-runs/:runId/logs": () => sse(frame("only"), end),
		});
		await renderWithProviders(
			<RunOutput runId="ro-2" active={false} label="Output" />,
		);
		expect(await screen.findByText("1 line")).toBeInTheDocument();
	});

	test("says a finished run printed nothing", async () => {
		stubApi({ "GET /task-runs/:runId/logs": () => sse(end) });
		await renderWithProviders(
			<RunOutput runId="ro-3" active={false} label="Output" />,
		);
		expect(
			await screen.findByText("This run printed nothing."),
		).toBeInTheDocument();
		expect(screen.getByText("Finished")).toBeInTheDocument();
		expect(screen.getByText("0 lines")).toBeInTheDocument();
	});

	test("shows Loading while a finished run's output has not arrived yet", async () => {
		stubApi({ "GET /task-runs/:runId/logs": pending });
		await renderWithProviders(
			<RunOutput runId="ro-4" active={false} label="Output" />,
		);
		expect(screen.getByText("Loading…")).toBeInTheDocument();
		expect(screen.getByText("Waiting for output…")).toBeInTheDocument();
		expect(screen.queryByText("Finished")).not.toBeInTheDocument();
	});

	test("says Live and waits for output while the run is active", async () => {
		stubApi({ "GET /task-runs/:runId/logs": pending });
		await renderWithProviders(<RunOutput runId="ro-5" active label="Output" />);
		expect(screen.getByText("Live")).toBeInTheDocument();
		expect(screen.getByText("Waiting for output…")).toBeInTheDocument();
	});

	test("keeps saying Live for an active run even when the stream ended", async () => {
		stubApi({ "GET /task-runs/:runId/logs": () => sse(frame("a"), end) });
		await renderWithProviders(<RunOutput runId="ro-6" active label="Output" />);
		expect(await screen.findByText("1 line")).toBeInTheDocument();
		expect(screen.getByText("Live")).toBeInTheDocument();
	});

	test("Follow starts on and can be switched off", async () => {
		stubApi({ "GET /task-runs/:runId/logs": () => sse(frame("a"), end) });
		const user = userEvent.setup();
		await renderWithProviders(
			<RunOutput runId="ro-7" active={false} label="Output" />,
		);
		await screen.findByText("1 line");
		const follow = screen.getByRole("switch", { name: "Follow" });
		expect(follow).toBeChecked();
		expect(
			screen.queryByRole("button", { name: "Jump to latest" }),
		).not.toBeInTheDocument();
		await user.click(follow);
		expect(follow).not.toBeChecked();
	});

	test("offers Jump to latest once Follow is off and lines exist, which turns Follow back on", async () => {
		stubApi({ "GET /task-runs/:runId/logs": () => sse(frame("a"), end) });
		const user = userEvent.setup();
		await renderWithProviders(
			<RunOutput runId="ro-8" active={false} label="Output" />,
		);
		await screen.findByText("1 line");
		await user.click(screen.getByRole("switch", { name: "Follow" }));
		await user.click(screen.getByRole("button", { name: "Jump to latest" }));
		expect(screen.getByRole("switch", { name: "Follow" })).toBeChecked();
		expect(
			screen.queryByRole("button", { name: "Jump to latest" }),
		).not.toBeInTheDocument();
	});

	test("does not offer Jump to latest when there is no output to jump to", async () => {
		stubApi({ "GET /task-runs/:runId/logs": () => sse(end) });
		const user = userEvent.setup();
		await renderWithProviders(
			<RunOutput runId="ro-9" active={false} label="Output" />,
		);
		await screen.findByText("This run printed nothing.");
		await user.click(screen.getByRole("switch", { name: "Follow" }));
		expect(
			screen.queryByRole("button", { name: "Jump to latest" }),
		).not.toBeInTheDocument();
	});
});
