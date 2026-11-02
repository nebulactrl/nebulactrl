import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { LogLine, LogStream, SseStatus } from "#/api/sse";
import { LogTab } from "./log-tab";

let nextId = 0;
function line(text: string, stream?: string): LogLine {
	nextId += 1;
	return { id: nextId, line: text, ...(stream ? { stream } : {}) };
}

function stream(lines: LogLine[], status: SseStatus = "live"): LogStream {
	return { lines, epoch: 0, status, clear: () => {} };
}

const sample = () => [
	line("compiling the app", "build"),
	line("ERROR build failed: missing module", "build"),
	line("WARN slow registry", "deploy"),
	line("starting container", "deploy"),
	line("server listening on :8080", "runtime"),
	line("level=debug agent trace id=7", "debug"),
];

const chip = (name: RegExp | string) => screen.getByRole("button", { name });

describe("LogTab filters", () => {
	test("every stream and level starts selected and the stream chips carry their counts", () => {
		render(<LogTab stream={stream(sample())} running={false} />);

		for (const name of [
			/^Build 2$/,
			/^Deploy 2$/,
			/^Runtime 1$/,
			/^Debug 1$/,
		]) {
			expect(chip(name)).toHaveAttribute("aria-pressed", "true");
		}
		for (const name of ["Error", "Warn", "Info", "Debug"]) {
			// "Debug" also names the stream chip: the level chip has no count.
			expect(
				screen.getAllByRole("button", { name: new RegExp(`^${name}`) }).length,
			).toBeGreaterThan(0);
		}
		expect(screen.getByText("6 lines")).toBeInTheDocument();
	});

	test("lines without a stream count under deploy", () => {
		render(<LogTab stream={stream([line("plain line")])} running={false} />);
		expect(chip(/^Deploy 1$/)).toBeInTheDocument();
		expect(chip(/^Build 0$/)).toBeInTheDocument();
	});

	test("switching a stream off hides its lines from the count", async () => {
		render(<LogTab stream={stream(sample())} running={false} />);

		await userEvent.setup().click(chip(/^Debug 1$/));

		expect(chip(/^Debug 1$/)).toHaveAttribute("aria-pressed", "false");
		expect(screen.getByText("5 of 6 lines")).toBeInTheDocument();
	});

	test("switching a stream off and on again restores its lines", async () => {
		render(<LogTab stream={stream(sample())} running={false} />);
		const user = userEvent.setup();

		await user.click(chip(/^Build 2$/));
		expect(screen.getByText("4 of 6 lines")).toBeInTheDocument();
		await user.click(chip(/^Build 2$/));

		expect(screen.getByText("6 lines")).toBeInTheDocument();
	});

	test("level chips filter by the level a line states; unmarked lines are info", async () => {
		render(<LogTab stream={stream(sample())} running={false} />);
		const user = userEvent.setup();

		// Turn off info: the four lines that state no level go.
		await user.click(screen.getByRole("button", { name: "Info" }));

		expect(screen.getByRole("button", { name: "Info" })).toHaveAttribute(
			"aria-pressed",
			"false",
		);
		// ERROR + WARN + the debug line remain.
		expect(screen.getByText("3 of 6 lines")).toBeInTheDocument();
	});

	test("the search needs every word, in any case", async () => {
		render(<LogTab stream={stream(sample())} running={false} />);

		await userEvent
			.setup()
			.type(
				screen.getByRole("searchbox", { name: "Search the debug log" }),
				"BUILD failed",
			);

		expect(screen.getByText("1 of 6 lines")).toBeInTheDocument();
	});

	test("with nothing matching it says so and Clear filters restores everything", async () => {
		render(<LogTab stream={stream(sample())} running={false} />);
		const user = userEvent.setup();
		const search = screen.getByRole("searchbox", {
			name: "Search the debug log",
		});

		await user.type(search, "no such text");
		await user.click(chip(/^Runtime 1$/));

		expect(
			screen.getByText(/No line matches the filters\./),
		).toBeInTheDocument();
		expect(screen.getByText("0 of 6 lines")).toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "Clear filters" }));

		expect(search).toHaveValue("");
		expect(chip(/^Runtime 1$/)).toHaveAttribute("aria-pressed", "true");
		expect(screen.getByText("6 lines")).toBeInTheDocument();
		expect(
			screen.queryByText(/No line matches the filters/),
		).not.toBeInTheDocument();
	});
});

describe("LogTab empty and status", () => {
	test("a finished deployment with no lines says none were recorded", () => {
		render(<LogTab stream={stream([], "ended")} running={false} />);
		expect(
			screen.getByText("No log lines were recorded for this deployment."),
		).toBeInTheDocument();
		expect(screen.getByText("Stream ended")).toBeInTheDocument();
		expect(screen.getByText("0 lines")).toBeInTheDocument();
	});

	test("a running deployment with no lines waits for the first one", () => {
		render(<LogTab stream={stream([], "live")} running />);
		expect(
			screen.getByText("Waiting for the first log line…"),
		).toBeInTheDocument();
		expect(screen.getByText("Following live")).toBeInTheDocument();
	});

	test.each<[SseStatus, string]>([
		["connecting", "Connecting…"],
		["live", "Following live"],
		["paused", "Reconnecting…"],
		["waiting", "Waiting for a free connection…"],
		["ended", "Stream ended"],
	])("the %s stream reads '%s'", (status, text) => {
		render(<LogTab stream={stream([], status)} running={false} />);
		expect(screen.getByText(text)).toBeInTheDocument();
	});

	test.each<SseStatus>(["connecting", "waiting", "paused"])(
		"a %s stream with no lines still counts as waiting for the first line",
		(status) => {
			render(<LogTab stream={stream([], status)} running={false} />);
			expect(
				screen.getByText("Waiting for the first log line…"),
			).toBeInTheDocument();
		},
	);
});

describe("LogTab follow", () => {
	test("Follow is on by default and can be switched off", async () => {
		render(<LogTab stream={stream(sample())} running />);
		const follow = screen.getByRole("switch", { name: "Follow" });
		expect(follow).toBeChecked();

		await userEvent.setup().click(follow);

		expect(follow).not.toBeChecked();
	});

	test("after letting go of a running log, Jump to latest turns following back on", async () => {
		render(<LogTab stream={stream(sample())} running />);
		const user = userEvent.setup();
		expect(
			screen.queryByRole("button", { name: "Jump to latest" }),
		).not.toBeInTheDocument();

		await user.click(screen.getByRole("switch", { name: "Follow" }));
		await user.click(screen.getByRole("button", { name: "Jump to latest" }));

		expect(screen.getByRole("switch", { name: "Follow" })).toBeChecked();
		expect(
			screen.queryByRole("button", { name: "Jump to latest" }),
		).not.toBeInTheDocument();
	});

	test("a finished deployment never offers Jump to latest", async () => {
		render(<LogTab stream={stream(sample(), "ended")} running={false} />);
		await userEvent
			.setup()
			.click(screen.getByRole("switch", { name: "Follow" }));
		expect(
			screen.queryByRole("button", { name: "Jump to latest" }),
		).not.toBeInTheDocument();
	});
});
