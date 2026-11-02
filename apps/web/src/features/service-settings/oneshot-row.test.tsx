import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { makeService } from "#/features/canvas/lib/fixtures";
import { buildPatch, type SettingsDraft, toDraft } from "./draft";
import { OneShotRow } from "./oneshot-row";
import { emptyTailnetDraft } from "./tailnet-draft";

const service = makeService({ id: "s1" });
const baseline = toDraft(service);

/** The row over a working copy, exposing the draft it produces. */
function Harness({
	start,
	onDraft,
}: {
	start: SettingsDraft;
	onDraft: (draft: SettingsDraft) => void;
}) {
	const [draft, setDraft] = useState(start);
	onDraft(draft);
	return (
		<OneShotRow
			draft={draft}
			set={(key, value) =>
				setDraft((current) => ({ ...current, [key]: value }))
			}
		/>
	);
}

function mount(start: SettingsDraft = baseline) {
	const latest = { draft: start };
	render(
		<Harness
			start={start}
			onDraft={(draft) => {
				latest.draft = draft;
			}}
		/>,
	);
	return latest;
}

describe("OneShotRow", () => {
	test("a long-running service shows the switch off, in words", () => {
		mount();
		expect(
			screen.getByRole("switch", { name: "One-shot job" }),
		).not.toBeChecked();
		expect(screen.getByText("Off: a long-running service")).toBeInTheDocument();
		expect(
			screen.getByText(
				"Runs once per deploy and exits. Services that depend on it wait for it to finish.",
			),
		).toBeInTheDocument();
	});

	test("a one-shot service shows the switch on, in words", () => {
		mount({ ...baseline, oneshot: true, port: "" });
		expect(screen.getByRole("switch", { name: "One-shot job" })).toBeChecked();
		expect(
			screen.getByText("On: exit code 0 is success; never restarted"),
		).toBeInTheDocument();
	});

	test("turning it on clears the port, the health check and the tailnet", async () => {
		const user = userEvent.setup();
		const start: SettingsDraft = {
			...baseline,
			port: "8080",
			healthType: "http",
			tailnet: emptyTailnetDraft(true),
		};
		const latest = mount(start);
		expect(latest.draft.port).toBe("8080");
		await user.click(screen.getByRole("switch", { name: "One-shot job" }));

		expect(latest.draft.oneshot).toBe(true);
		expect(latest.draft.port).toBe("");
		expect(latest.draft.healthType).toBe("none");
		expect(latest.draft.tailnet).toBeNull();
		expect(
			screen.getByText("On: exit code 0 is success; never restarted"),
		).toBeInTheDocument();
		// What gets saved: a one-shot config with no port and no health check.
		const patch = buildPatch(baseline, latest.draft, service.sourceType);
		expect(patch?.config?.oneshot).toBe(true);
		expect(patch?.config?.port).toBeUndefined();
		expect(patch?.config?.healthcheck?.type).toBe("none");
	});

	test("turning it off leaves the other fields alone", async () => {
		const user = userEvent.setup();
		const start: SettingsDraft = {
			...baseline,
			oneshot: true,
			port: "",
			healthType: "none",
			startCommand: "node migrate.js",
		};
		const latest = mount(start);
		await user.click(screen.getByRole("switch", { name: "One-shot job" }));
		expect(latest.draft.oneshot).toBe(false);
		expect(latest.draft.port).toBe("");
		expect(latest.draft.healthType).toBe("none");
		expect(latest.draft.startCommand).toBe("node migrate.js");
		expect(screen.getByText("Off: a long-running service")).toBeInTheDocument();
	});
});
