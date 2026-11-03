import { describe, expect, test } from "bun:test";
import { fireEvent, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { makeService } from "#/features/canvas/lib/fixtures";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { CronRows } from "./cron-rows";
import {
	buildPatch,
	type SettingsDraft,
	toDraft,
	validateDraft,
} from "./draft";
import { OneShotRow } from "./oneshot-row";

const service = makeService({ id: "s1", kind: "worker" });
const baseline = toDraft(service);

const preview = {
	"POST /task-schedules/preview": {
		body: { valid: true, description: "Every day", nextRuns: [] },
	},
};

/** The rows over a working copy, exposing the draft they produce. */
function Harness({
	start,
	onDraft,
}: {
	start: SettingsDraft;
	onDraft: (draft: SettingsDraft) => void;
}) {
	const [draft, setDraft] = useState(start);
	onDraft(draft);
	const set: <K extends keyof SettingsDraft>(
		key: K,
		value: SettingsDraft[K],
	) => void = (key, value) =>
		setDraft((current) => ({ ...current, [key]: value }));
	return (
		<>
			<OneShotRow draft={draft} set={set} />
			{draft.oneshot ? (
				<CronRows
					draft={draft}
					set={set}
					errors={validateDraft(draft, "image")}
				/>
			) : null}
		</>
	);
}

async function mount(start: SettingsDraft) {
	stubApi(preview);
	const latest = { draft: start };
	await renderWithProviders(
		<Harness
			start={start}
			onDraft={(draft) => {
				latest.draft = draft;
			}}
		/>,
	);
	return latest;
}

const oneshot: SettingsDraft = { ...baseline, oneshot: true, port: "" };

describe("CronRows", () => {
	test("a long-running service has no schedule rows", async () => {
		await mount(baseline);
		expect(
			screen.queryByRole("switch", { name: "Run on a schedule" }),
		).toBeNull();
	});

	test("a one-shot shows the schedule switch, off, with nothing under it", async () => {
		await mount(oneshot);
		expect(
			screen.getByRole("switch", { name: "Run on a schedule" }),
		).not.toBeChecked();
		expect(screen.queryByLabelText("Cron expression")).toBeNull();
		expect(screen.queryByLabelText("Timeout (s)")).toBeNull();
	});

	test("turning it on shows the schedule, zone, timeout and overlap rows and stages the cron config", async () => {
		const user = userEvent.setup();
		const latest = await mount(oneshot);
		await user.click(screen.getByRole("switch", { name: "Run on a schedule" }));

		expect(screen.getByLabelText("Cron expression")).toHaveValue("0 3 * * *");
		expect(
			screen.getByRole("combobox", { name: "Schedule" }),
		).toHaveTextContent("Daily 03:00");
		expect(
			screen.getByRole("combobox", { name: "Time zone" }),
		).toHaveTextContent("UTC");
		expect(screen.getByLabelText("Timeout (s)")).toHaveValue("");
		expect(
			screen.getByRole("switch", {
				name: "Skip a run while the previous one runs",
			}),
		).toBeChecked();
		const config = buildPatch(baseline, latest.draft, "image")?.config;
		expect(config?.oneshot).toBe(true);
		expect(config?.cron).toEqual({ schedule: "0 3 * * *", timezone: "UTC" });
	});

	test("a preset fills the expression and a typed one reads as Custom", async () => {
		const user = userEvent.setup();
		const latest = await mount({ ...oneshot, cronEnabled: true });
		await user.click(screen.getByRole("combobox", { name: "Schedule" }));
		await user.click(await screen.findByRole("option", { name: "Hourly" }));
		expect(latest.draft.cronSchedule).toBe("@hourly");
		expect(screen.getByLabelText("Cron expression")).toHaveValue("@hourly");

		fireEvent.change(screen.getByLabelText("Cron expression"), {
			target: { value: "*/7 * * * *" },
		});
		expect(latest.draft.cronSchedule).toBe("*/7 * * * *");
		expect(
			screen.getByRole("combobox", { name: "Schedule" }),
		).toHaveTextContent("Custom");
	});

	test("the timeout and the overlap switch go into the config", async () => {
		const user = userEvent.setup();
		const latest = await mount({ ...oneshot, cronEnabled: true });
		fireEvent.change(screen.getByLabelText("Timeout (s)"), {
			target: { value: "900" },
		});
		await user.click(
			screen.getByRole("switch", {
				name: "Skip a run while the previous one runs",
			}),
		);
		const config = buildPatch(baseline, latest.draft, "image")?.config;
		expect(config?.cron).toEqual({
			schedule: "0 3 * * *",
			timezone: "UTC",
			timeoutSeconds: 900,
			concurrency: "allow",
		});
	});

	test("a timeout out of range and an empty schedule are reported under their rows", async () => {
		await mount({
			...oneshot,
			cronEnabled: true,
			cronSchedule: "",
			cronTimeoutSeconds: "90000",
		});
		expect(screen.getByText("A schedule is required.")).toBeInTheDocument();
		expect(
			screen.getByText("Timeout must be between 1 and 86400."),
		).toBeInTheDocument();
	});

	test("turning One-shot job off clears the schedule and hides its rows", async () => {
		const user = userEvent.setup();
		const latest = await mount({ ...oneshot, cronEnabled: true });
		expect(screen.getByLabelText("Cron expression")).toBeInTheDocument();
		await user.click(screen.getByRole("switch", { name: "One-shot job" }));
		expect(latest.draft.oneshot).toBe(false);
		expect(latest.draft.cronEnabled).toBe(false);
		expect(screen.queryByLabelText("Cron expression")).toBeNull();
		expect(
			buildPatch(baseline, latest.draft, "image")?.config?.cron,
		).toBeUndefined();
	});
});
