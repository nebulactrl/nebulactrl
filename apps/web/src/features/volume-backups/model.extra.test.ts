import { describe, expect, test } from "bun:test";
import {
	backupSummary,
	hookNote,
	inPlaceSentence,
	restoreModeLabel,
	restoreStatusLabel,
	scheduleLine,
	shortSnapshotId,
	splitCommand,
	stagedPointer,
} from "./model";

describe("splitCommand escapes", () => {
	test('inside double quotes only \\" and \\\\ are escapes', () => {
		expect(splitCommand(String.raw`echo "say \"hi\" \\ done"`)).toEqual([
			"echo",
			String.raw`say "hi" \ done`,
		]);
		// any other backslash stays literal inside double quotes
		expect(splitCommand(String.raw`echo "a\nb"`)).toEqual([
			"echo",
			String.raw`a\nb`,
		]);
	});
	test("outside quotes a backslash escapes the next character, even a space", () => {
		expect(splitCommand(String.raw`cp my\ file /tmp/it\'s`)).toEqual([
			"cp",
			"my file",
			"/tmp/it's",
		]);
	});
	test("a trailing lone backslash is kept as a literal character", () => {
		expect(splitCommand("echo a\\")).toEqual(["echo", "a\\"]);
	});
	test("an escaped quote does not open a quoted section", () => {
		expect(splitCommand(String.raw`echo \"x`)).toEqual(["echo", '"x']);
	});
});

describe("scheduleLine", () => {
	const describeCron = (cron: string) => `cron(${cron})`;
	test("a blank expression means manual only, whatever the timezone", () => {
		expect(scheduleLine("", "Europe/Berlin", describeCron)).toBe("Manual only");
		expect(scheduleLine("   ", "Europe/Berlin", describeCron)).toBe(
			"Manual only",
		);
	});
	test("a schedule shows the description and its timezone", () => {
		expect(scheduleLine("0 3 * * *", "Europe/Berlin", describeCron)).toBe(
			"cron(0 3 * * *) · Europe/Berlin",
		);
	});
	test("an unset timezone reads as UTC", () => {
		expect(scheduleLine("0 3 * * *", "", describeCron)).toBe(
			"cron(0 3 * * *) · UTC",
		);
	});
});

describe("backupSummary for unknown statuses", () => {
	test("an unrecognised status is shown as it is", () => {
		expect(
			backupSummary({
				status: "archived",
				mode: "freeze",
				freezeMs: 0,
				sizeBytes: 0,
				bytesAdded: 0,
				files: 0,
			}),
		).toBe("archived");
	});
});

describe("hookNote", () => {
	test("only a failed-but-continued command is noted", () => {
		expect(hookNote({ hookOutcome: "failed_continued" })).toBe(
			"A command failed; the backup went on as you allowed.",
		);
		expect(hookNote({ hookOutcome: "ok" })).toBe("");
		expect(hookNote({})).toBe("");
	});
});

describe("shortSnapshotId", () => {
	test("keeps the first eight characters", () => {
		expect(shortSnapshotId("a1b2c3d4e5f6a7b8")).toBe("a1b2c3d4");
	});
	test("a shorter id is kept whole and a missing one is empty", () => {
		expect(shortSnapshotId("abc")).toBe("abc");
		expect(shortSnapshotId(undefined)).toBe("");
		expect(shortSnapshotId("")).toBe("");
	});
});

describe("restore copy", () => {
	test("the in-place sentence names the volume, or falls back to 'the volume'", () => {
		expect(inPlaceSentence("data")).toContain(
			"This overwrites the files in data.",
		);
		expect(inPlaceSentence("")).toContain(
			"This overwrites the files in the volume.",
		);
		expect(inPlaceSentence("data")).toContain(
			"if that fails nothing is changed",
		);
	});

	test("the staged pointer names the volume, the backup time and the pending changes", () => {
		expect(stagedPointer("data", "2026-01-12T03:30:00Z", 3)).toBe(
			"Restore of data from Jan 12, 03:30 is staged: 3 changes to apply. Press Deploy · 3 in the top bar to switch to it.",
		);
	});
	test("the staged pointer leaves the time out when unknown and never counts below one", () => {
		expect(stagedPointer("data", undefined, 0)).toBe(
			"Restore of data is staged: 1 change to apply. Press Deploy · 1 in the top bar to switch to it.",
		);
		expect(stagedPointer("data", undefined, 1)).toContain("1 change to apply");
	});

	test("restore mode words", () => {
		expect(restoreModeLabel("in_place")).toBe("In place");
		expect(restoreModeLabel("new_volume")).toBe("Into a new volume");
		expect(restoreModeLabel("anything")).toBe("Into a new volume");
	});

	test("restore statuses map to words and pill tones", () => {
		expect(restoreStatusLabel("pre_backup", false)).toEqual({
			label: "Safety backup",
			tone: "deploying",
		});
		expect(restoreStatusLabel("restoring", false)).toEqual({
			label: "Restoring",
			tone: "deploying",
		});
		expect(restoreStatusLabel("succeeded", false)).toEqual({
			label: "Restored",
			tone: "ok",
		});
		expect(restoreStatusLabel("succeeded", true)).toEqual({
			label: "Restored, waiting for Deploy",
			tone: "ok",
		});
		expect(restoreStatusLabel("failed", true)).toEqual({
			label: "Failed",
			tone: "bad",
		});
		expect(restoreStatusLabel("mystery", false)).toEqual({
			label: "mystery",
			tone: "off",
		});
	});
});
