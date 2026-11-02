import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { CronPreview } from "./cron-preview";

const problem = (status: number, detail: string, code: string) => ({
	status,
	body: { status, title: "Error", detail, code },
});

function Harness({
	initial = "",
	zone = "UTC",
}: {
	initial?: string;
	zone?: string;
}) {
	const [schedule, setSchedule] = useState(initial);
	const [timezone, setTimezone] = useState(zone);
	return (
		<>
			<input
				aria-label="schedule"
				value={schedule}
				onChange={(e) => setSchedule(e.target.value)}
			/>
			<input
				aria-label="zone"
				value={timezone}
				onChange={(e) => setTimezone(e.target.value)}
			/>
			<CronPreview schedule={schedule} timezone={timezone} />
		</>
	);
}

describe("CronPreview", () => {
	test("asks for a schedule and sends nothing while it is blank", async () => {
		const api = stubApi({});
		await renderWithProviders(<CronPreview schedule="   " timezone="UTC" />);
		expect(
			screen.getByText("Enter a schedule to see when it runs."),
		).toBeInTheDocument();
		expect(api.calls).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});

	test("shows the English reading and the next runs in the schedule's zone and UTC", async () => {
		const api = stubApi({
			"POST /task-schedules/preview": {
				body: {
					valid: true,
					description: "At 09:00 every day",
					timezone: "Europe/Berlin",
					nextRuns: ["2026-10-27T08:00:00Z", "2026-10-28T08:00:00Z"],
				},
			},
		});
		await renderWithProviders(
			<CronPreview schedule=" 0 9 * * * " timezone=" Europe/Berlin " />,
		);
		expect(await screen.findByText("At 09:00 every day")).toBeInTheDocument();
		expect(screen.getByText("Next runs:")).toBeInTheDocument();
		const items = screen.getAllByRole("listitem");
		expect(items).toHaveLength(2);
		expect(items[0]).toHaveTextContent(
			"Tue 27 Oct 09:00 Europe/Berlin · 08:00 UTC",
		);
		expect(items[1]).toHaveTextContent(
			"Wed 28 Oct 09:00 Europe/Berlin · 08:00 UTC",
		);
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]).toMatchObject({
			method: "POST",
			path: "/task-schedules/preview",
			body: { schedule: "0 9 * * *", timezone: "Europe/Berlin", count: 5 },
		});
	});

	test("does not repeat UTC for a UTC schedule", async () => {
		stubApi({
			"POST /task-schedules/preview": {
				body: {
					valid: true,
					description: "Every hour",
					timezone: "UTC",
					nextRuns: ["2026-10-27T08:00:00Z"],
				},
			},
		});
		await renderWithProviders(<CronPreview schedule="@hourly" timezone="" />);
		await screen.findByText("Every hour");
		const item = screen.getByRole("listitem");
		expect(item).toHaveTextContent("Tue 27 Oct 08:00 UTC");
		expect(item.textContent).not.toContain("08:00 UTC · 08:00 UTC");
		expect(item.textContent?.match(/UTC/g)).toHaveLength(1);
	});

	test("sends an empty zone as written and falls back to a generic reading", async () => {
		const api = stubApi({
			"POST /task-schedules/preview": {
				body: { valid: true, nextRuns: [] },
			},
		});
		await renderWithProviders(<CronPreview schedule="* * * * *" timezone="" />);
		expect(
			await screen.findByText("Runs on this schedule"),
		).toBeInTheDocument();
		expect(screen.queryByText("Next runs:")).not.toBeInTheDocument();
		expect(api.calls[0]?.body).toMatchObject({ timezone: "" });
	});

	test("shows the server's reason when the schedule is invalid", async () => {
		stubApi({
			"POST /task-schedules/preview": {
				body: { valid: false, error: "expected 5 fields, got 2", nextRuns: [] },
			},
		});
		await renderWithProviders(<CronPreview schedule="* *" timezone="UTC" />);
		expect(
			await screen.findByText("expected 5 fields, got 2"),
		).toBeInTheDocument();
		expect(screen.queryByText("Next runs:")).not.toBeInTheDocument();
	});

	test("falls back to a generic sentence for an invalid schedule without a reason", async () => {
		stubApi({
			"POST /task-schedules/preview": { body: { valid: false, nextRuns: [] } },
		});
		await renderWithProviders(<CronPreview schedule="nope" timezone="UTC" />);
		expect(
			await screen.findByText("This is not a valid schedule."),
		).toBeInTheDocument();
	});

	test("shows the error when the preview request fails", async () => {
		stubApi({
			"POST /task-schedules/preview": problem(
				422,
				"timezone is unknown",
				"validation_failed",
			),
		});
		await renderWithProviders(
			<CronPreview schedule="@daily" timezone="Mars/Base" />,
		);
		expect(await screen.findByText("timezone is unknown")).toBeInTheDocument();
	});

	test("says it is checking until the first answer arrives", async () => {
		stubApi({
			"POST /task-schedules/preview": (request) =>
				new Promise<Response>((_, reject) =>
					request.signal.addEventListener("abort", () =>
						reject(new DOMException("aborted", "AbortError")),
					),
				),
		});
		await renderWithProviders(<Harness initial="@daily" />);
		expect(
			await screen.findByText("Checking the schedule…"),
		).toBeInTheDocument();
	});

	test("waits for typing to settle, then sends one request for the final text", async () => {
		const api = stubApi({
			"POST /task-schedules/preview": {
				body: { valid: true, description: "Every day", nextRuns: [] },
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<Harness />);
		await user.type(screen.getByLabelText("schedule"), "@daily");
		expect(api.calls).toEqual([]);
		expect(await screen.findByText("Every day")).toBeInTheDocument();
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]?.body).toMatchObject({ schedule: "@daily" });
	});

	test("keeps the previous answer on screen while a changed schedule is checked", async () => {
		const api = stubApi({
			"POST /task-schedules/preview": (request) =>
				request
					.clone()
					.json()
					.then((body: { schedule: string }) =>
						body.schedule === "@daily"
							? Response.json({
									valid: true,
									description: "Every day",
									nextRuns: [],
								})
							: new Promise<Response>((_, reject) =>
									request.signal.addEventListener("abort", () =>
										reject(new DOMException("aborted", "AbortError")),
									),
								),
					),
		});
		const user = userEvent.setup();
		await renderWithProviders(<Harness initial="@daily" />);
		expect(await screen.findByText("Every day")).toBeInTheDocument();
		await user.clear(screen.getByLabelText("schedule"));
		await user.type(screen.getByLabelText("schedule"), "@weekly");
		await waitFor(() => expect(api.calls).toHaveLength(2));
		expect(api.calls[1]?.body).toMatchObject({ schedule: "@weekly" });
		expect(screen.getByText("Every day")).toBeInTheDocument();
		expect(
			screen.queryByText("Checking the schedule…"),
		).not.toBeInTheDocument();
	});
});
