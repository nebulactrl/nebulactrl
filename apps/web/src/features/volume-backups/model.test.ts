import { describe, expect, test } from "bun:test";
import {
	backupSummary,
	breadcrumbs,
	buildConfigInput,
	type ConfigDraft,
	type ConfigLike,
	describeKeep,
	draftFromConfig,
	entryDownload,
	formatBackupTime,
	formatPause,
	HOOK_TEMPLATES,
	initialRestoreState,
	isDirty,
	joinCommand,
	joinVolumePath,
	MODE_OPTIONS,
	modeLabel,
	newVolumeSentence,
	normalizeVolumePath,
	parseExcludes,
	recoveryKeyFile,
	restorableVolumes,
	restoreReady,
	restoreRequest,
	restoreStatusLabel,
	scheduleChoiceFor,
	sortEntries,
	splitCommand,
	truncatedNotice,
	volumeFileUrl,
	volumesLine,
} from "./model";

describe("mode copy", () => {
	test("three modes, the safe one first and marked recommended", () => {
		expect(MODE_OPTIONS.map((o) => o.value)).toEqual([
			"freeze",
			"stop",
			"live",
		]);
		expect(MODE_OPTIONS[0]?.label).toBe("Brief freeze (recommended)");
		expect(MODE_OPTIONS[1]?.label).toBe("Stop during backup");
		expect(MODE_OPTIONS[2]?.label).toBe("Keep running, no freeze");
	});
	test("the history drops the recommendation, unknown modes pass through", () => {
		expect(modeLabel("freeze")).toBe("Brief freeze");
		expect(modeLabel("stop")).toBe("Stop during backup");
		expect(modeLabel("odd")).toBe("odd");
	});
});

describe("command lines", () => {
	test("splits like a shell reads quotes, without running one", () => {
		expect(splitCommand("redis-cli BGSAVE")).toEqual(["redis-cli", "BGSAVE"]);
		expect(splitCommand(HOOK_TEMPLATES[0]?.command ?? "")).toEqual([
			"sqlite3",
			"/data/app.db",
			".backup '/data/.nc-backup/app.db'",
		]);
		expect(splitCommand(`sh -c 'echo "a b"; sync'`)).toEqual([
			"sh",
			"-c",
			'echo "a b"; sync',
		]);
		expect(splitCommand(`echo "" x`)).toEqual(["echo", "", "x"]);
		expect(splitCommand("   ")).toEqual([]);
	});
	test("an open quote is an error, not a guess", () => {
		expect(splitCommand(`echo "oops`)).toBeNull();
		expect(splitCommand("echo 'oops")).toBeNull();
	});
	test("join is the inverse of split", () => {
		for (const t of HOOK_TEMPLATES) {
			const argv = splitCommand(t.command) ?? [];
			expect(splitCommand(joinCommand(argv))).toEqual(argv);
		}
		expect(splitCommand(joinCommand(["a b", "", "it's", 'q"x']))).toEqual([
			"a b",
			"",
			"it's",
			'q"x',
		]);
	});
});

const stored: ConfigLike = {
	destinationId: "d1",
	mode: "freeze",
	freezeBudgetSeconds: 5,
	excludes: [],
	keepDaily: 7,
	keepWeekly: 4,
	keepMonthly: 6,
	cron: "",
	timezone: "",
	enabled: false,
};

describe("config draft", () => {
	test("defaults: freeze, 5 s, keep 7/4/6, manual only", () => {
		const d = draftFromConfig(stored);
		expect(d.mode).toBe("freeze");
		expect(d.freezeBudget).toBe("5");
		expect([d.keepDaily, d.keepWeekly, d.keepMonthly]).toEqual(["7", "4", "6"]);
		expect(d.schedule).toBe("manual");
		expect(d.timezone).toBe("UTC");
	});
	test("an unset destination takes the fallback", () => {
		expect(
			draftFromConfig({ ...stored, destinationId: undefined }, "x")
				.destinationId,
		).toBe("x");
	});
	test("a stored hook shows as a command line", () => {
		const d = draftFromConfig({
			...stored,
			preHook: {
				command: ["sqlite3", "/data/app.db", ".backup '/x'"],
				timeoutSeconds: 60,
				continueOnError: true,
			},
		});
		expect(d.preHook.command).toBe(`sqlite3 /data/app.db ".backup '/x'"`);
		expect(d.preHook.timeout).toBe("60");
		expect(d.preHook.continueOnError).toBe(true);
		expect(d.postHook.command).toBe("");
	});
	test("schedule choices reuse the database presets", () => {
		expect(scheduleChoiceFor("")).toBe("manual");
		expect(scheduleChoiceFor("@hourly")).toBe("hourly");
		expect(scheduleChoiceFor("0 3 * * *")).toBe("daily");
		expect(scheduleChoiceFor("0 3 * * 0")).toBe("weekly");
		expect(scheduleChoiceFor("30 3 * * *")).toBe("custom");
	});
});

describe("buildConfigInput", () => {
	const base: ConfigDraft = draftFromConfig(stored);

	test("a valid default draft builds the body", () => {
		const r = buildConfigInput(base);
		expect(r.ok).toBe(true);
		if (!r.ok) return;
		expect(r.input).toEqual({
			destinationId: "d1",
			mode: "freeze",
			freezeBudgetSeconds: 5,
			excludes: [],
			keepDaily: 7,
			keepWeekly: 4,
			keepMonthly: 6,
			cron: "",
			timezone: "UTC",
			enabled: false,
		});
	});
	test("a missing destination is reported", () => {
		const r = buildConfigInput({ ...base, destinationId: "" });
		expect(r.ok).toBe(false);
		if (r.ok) return;
		expect(r.errors.destination).toBeDefined();
	});
	test("freeze budget and keeps are bounded whole numbers", () => {
		for (const freezeBudget of ["0", "61", "x", "1.5", ""]) {
			const r = buildConfigInput({ ...base, freezeBudget });
			expect(r.ok).toBe(false);
		}
		const r = buildConfigInput({ ...base, keepDaily: "-1", keepWeekly: "a" });
		expect(r.ok).toBe(false);
		if (r.ok) return;
		expect(r.errors.keepDaily).toBeDefined();
		expect(r.errors.keepWeekly).toBeDefined();
		expect(buildConfigInput({ ...base, keepMonthly: "0" }).ok).toBe(true);
	});
	test("a hook becomes an argv with its timeout and error policy", () => {
		const r = buildConfigInput({
			...base,
			preHook: {
				command: "redis-cli BGSAVE",
				user: " redis ",
				timeout: "30",
				continueOnError: true,
			},
		});
		expect(r.ok).toBe(true);
		if (!r.ok) return;
		expect(r.input.preHook).toEqual({
			command: ["redis-cli", "BGSAVE"],
			user: "redis",
			timeoutSeconds: 30,
			continueOnError: true,
		});
		expect(r.input.postHook).toBeUndefined();
	});
	test("a broken hook names the field", () => {
		const open = buildConfigInput({
			...base,
			postHook: { ...base.postHook, command: "echo 'x" },
		});
		expect(open.ok).toBe(false);
		if (!open.ok) expect(open.errors.postHook).toBeDefined();
		const slow = buildConfigInput({
			...base,
			preHook: { ...base.preHook, command: "true", timeout: "4000" },
		});
		expect(slow.ok).toBe(false);
		if (!slow.ok) expect(slow.errors.preHookTimeout).toBeDefined();
	});
	test("manual only sends no cron and no enabled schedule", () => {
		const r = buildConfigInput({ ...base, cron: "0 3 * * *", enabled: true });
		expect(r.ok && r.input.cron).toBe("");
		expect(r.ok && r.input.enabled).toBe(false);
	});
	test("a schedule needs a cron expression", () => {
		const custom = buildConfigInput({ ...base, schedule: "custom", cron: " " });
		expect(custom.ok).toBe(false);
		const daily = buildConfigInput({
			...base,
			schedule: "daily",
			cron: "0 3 * * *",
			enabled: true,
			timezone: "Europe/Berlin",
		});
		expect(daily.ok).toBe(true);
		if (daily.ok) {
			expect(daily.input.cron).toBe("0 3 * * *");
			expect(daily.input.enabled).toBe(true);
			expect(daily.input.timezone).toBe("Europe/Berlin");
		}
	});
	test("excludes are trimmed and deduplicated", () => {
		expect(parseExcludes(" cache/ \n\n*.tmp\ncache/\n")).toEqual([
			"cache/",
			"*.tmp",
		]);
	});
	test("dirty compares against the saved draft", () => {
		expect(isDirty(base, draftFromConfig(stored))).toBe(false);
		expect(isDirty({ ...base, mode: "stop" }, base)).toBe(true);
	});
});

describe("describeKeep", () => {
	test("lists the limits that are set", () => {
		expect(describeKeep(7, 4, 6)).toBe("Keep 7 daily, 4 weekly, 6 monthly");
		expect(describeKeep(7, 0, 0)).toBe("Keep 7 daily");
		expect(describeKeep(0, 0, 0)).toBe("Keep everything");
	});
});

describe("backupSummary", () => {
	const done = {
		status: "succeeded",
		mode: "freeze",
		freezeMs: 600,
		sizeBytes: 400 * 1024 * 1024,
		bytesAdded: 38 * 1024 * 1024,
		files: 10,
	};
	test("pause and upload, deduplicated", () => {
		expect(backupSummary(done)).toBe(
			"Done — paused 0.6 s, uploaded 38 MiB (deduplicated)",
		);
	});
	test("the mode actually used wins over the configured one", () => {
		expect(
			backupSummary({ ...done, consistency: "stop", freezeMs: 14_000 }),
		).toBe("Done — stopped 14 s, uploaded 38 MiB (deduplicated)");
		expect(backupSummary({ ...done, consistency: "live", freezeMs: 0 })).toBe(
			"Done — service kept running, uploaded 38 MiB (deduplicated)",
		);
	});
	test("nothing new, and a first backup that is not deduplicated", () => {
		expect(backupSummary({ ...done, bytesAdded: 0 })).toBe(
			"Done — paused 0.6 s, nothing new to upload (deduplicated)",
		);
		expect(backupSummary({ ...done, bytesAdded: done.sizeBytes })).toBe(
			"Done — paused 0.6 s, uploaded 400 MiB",
		);
	});
	test("other states say so plainly", () => {
		expect(backupSummary({ ...done, status: "queued" })).toBe(
			"Waiting to start",
		);
		expect(backupSummary({ ...done, status: "running" })).toBe("Backing up…");
		expect(
			backupSummary({
				...done,
				status: "failed",
				error: "Hook failed (exit 1). Nothing was backed up.",
			}),
		).toBe("Hook failed (exit 1). Nothing was backed up.");
		expect(backupSummary({ ...done, status: "failed" })).toBe("Failed");
		expect(
			backupSummary({
				...done,
				status: "skipped",
				error: "A deployment was in progress; backup skipped.",
			}),
		).toBe("A deployment was in progress; backup skipped.");
		expect(backupSummary({ ...done, status: "expired" })).toBe(
			"Not delivered: server offline. No changes were made.",
		);
	});
	test("pause formatting", () => {
		expect(formatPause(600)).toBe("0.6 s");
		expect(formatPause(0)).toBe("0.0 s");
		expect(formatPause(9_949)).toBe("9.9 s");
		expect(formatPause(14_400)).toBe("14 s");
	});
	test("volumes line", () => {
		expect(volumesLine(["data"])).toBe("Volume data");
		expect(volumesLine(["a", "b"])).toBe("2 volumes: a, b");
		expect(volumesLine([])).toBe("");
	});
});

describe("restore", () => {
	test("the label is UTC, month, day and 24 h time", () => {
		expect(formatBackupTime("2026-01-12T03:30:41Z")).toBe("Jan 12, 03:30");
		expect(formatBackupTime("2026-12-03T23:05:00Z")).toBe("Dec 3, 23:05");
	});
	test("new volume is the default and says nothing changes until Deploy", () => {
		const s = initialRestoreState(["data", "uploads"]);
		expect(s).toEqual({ volume: "data", mode: "new_volume", confirm: "" });
		expect(newVolumeSentence("2026-01-12T03:30:00Z")).toBe(
			"Restore Jan 12, 03:30 into a new volume. Your current data is kept. Nothing changes until you press Deploy.",
		);
		expect(restoreRequest(s)).toEqual({ volume: "data", mode: "new_volume" });
	});
	test("in place needs the slug typed exactly", () => {
		const s = { ...initialRestoreState(["data"]), mode: "in_place" as const };
		expect(restoreReady(s, "web")).toBe(false);
		expect(restoreReady({ ...s, confirm: "we" }, "web")).toBe(false);
		expect(restoreReady({ ...s, confirm: "web" }, "web")).toBe(true);
		expect(restoreRequest({ ...s, confirm: "web" })).toEqual({
			volume: "data",
			mode: "in_place",
			confirmSlug: "web",
		});
	});
	test("no volume, no restore", () => {
		expect(restoreReady(initialRestoreState([]), "web")).toBe(false);
	});
	test("volumes dropped from the service can not be restored", () => {
		expect(restorableVolumes(["data", "old"], ["data", "cache"])).toEqual({
			available: ["data"],
			missing: ["old"],
		});
	});
	test("status words", () => {
		expect(restoreStatusLabel("succeeded", true).label).toBe(
			"Restored, waiting for Deploy",
		);
		expect(restoreStatusLabel("succeeded", false).label).toBe("Restored");
		expect(restoreStatusLabel("pre_backup", false).label).toBe("Safety backup");
		expect(restoreStatusLabel("failed", false).tone).toBe("bad");
	});
});

describe("file browser paths", () => {
	test("normalize keeps paths absolute and inside the volume", () => {
		expect(normalizeVolumePath("")).toBe("/");
		expect(normalizeVolumePath("a//b/")).toBe("/a/b");
		expect(normalizeVolumePath("/a/./b/../c")).toBe("/a/c");
		expect(normalizeVolumePath("/../../x")).toBe("/x");
	});
	test("join", () => {
		expect(joinVolumePath("/", "a")).toBe("/a");
		expect(joinVolumePath("/a", "b c")).toBe("/a/b c");
	});
	test("breadcrumbs start at the volume", () => {
		expect(breadcrumbs("data", "/")).toEqual([{ label: "data", path: "/" }]);
		expect(breadcrumbs("data", "/db/wal")).toEqual([
			{ label: "data", path: "/" },
			{ label: "db", path: "/db" },
			{ label: "wal", path: "/db/wal" },
		]);
	});
	test("folders sort first, then names naturally", () => {
		const sorted = sortEntries([
			{ name: "b.txt", type: "file", size: 1 },
			{ name: "z", type: "dir", size: 0 },
			{ name: "file10", type: "file", size: 1 },
			{ name: "File2", type: "file", size: 1 },
			{ name: "a", type: "dir", size: 0 },
		]);
		expect(sorted.map((e) => e.name)).toEqual([
			"a",
			"z",
			"b.txt",
			"File2",
			"file10",
		]);
	});
	test("files download as they are, folders as zip, links not at all", () => {
		expect(entryDownload("file")).toBe("file");
		expect(entryDownload("dir")).toBe("archive");
		expect(entryDownload("link")).toBeNull();
		expect(entryDownload("other")).toBeNull();
	});
	test("download URLs encode the volume and the path", () => {
		expect(volumeFileUrl("b 1", "data", "/a b/c&d", false)).toBe(
			"/api/v1/volume-backups/b%201/files/content?volume=data&path=%2Fa+b%2Fc%26d",
		);
		expect(volumeFileUrl("b1", "data", "/x", true)).toBe(
			"/api/v1/volume-backups/b1/files/content?volume=data&path=%2Fx&archive=true",
		);
	});
	test("truncated notice", () => {
		expect(truncatedNotice(5000)).toContain("first 5,000 entries");
	});
});

describe("recovery key file", () => {
	test("carries the repository, the password and the note", () => {
		const f = recoveryKeyFile(
			{
				repository: "s3:https://s3.example/b/volumes/svc",
				password: "pw",
				note: "Keep it.",
			},
			"web",
		);
		expect(f.filename).toBe("nebulactrl-web-volume-backup-recovery-key.txt");
		expect(f.text).toContain("Repository: s3:https://s3.example/b/volumes/svc");
		expect(f.text).toContain("Password:   pw");
		expect(f.text).toContain("Keep it.");
	});
});
