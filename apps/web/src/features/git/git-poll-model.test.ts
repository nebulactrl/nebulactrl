import { describe, expect, test } from "bun:test";
import type { GitPollView } from "#/api/gen/types.gen";
import {
	canPollGit,
	newCommitHint,
	POLL_OPTIONS,
	pollChoice,
	pollDeploys,
	pollInput,
	pollStatus,
} from "./git-poll-model";

const view = (over: Partial<GitPollView> = {}): GitPollView => ({
	enabled: true,
	intervalMinutes: 5,
	deploy: true,
	updateAvailable: false,
	...over,
});

describe("git poll model", () => {
	test("the options are Off and the four intervals, in order", () => {
		expect(POLL_OPTIONS.map((o) => [o.value, o.label])).toEqual([
			["off", "Off"],
			["1", "Every minute"],
			["5", "Every 5 minutes"],
			["15", "Every 15 minutes"],
			["60", "Every hour"],
		]);
	});

	test("no row, or a disabled one, is off; an enabled one is its interval", () => {
		expect(pollChoice(null)).toBe("off");
		expect(pollChoice(view({ enabled: false }))).toBe("off");
		expect(pollChoice(view({ intervalMinutes: 15 }))).toBe("15");
	});

	test("deploying is on unless the row says otherwise", () => {
		expect(pollDeploys(null)).toBe(true);
		expect(pollDeploys(view({ deploy: false }))).toBe(false);
	});

	test("the PUT body always carries all three fields", () => {
		expect(pollInput("60", true, null)).toEqual({
			enabled: true,
			intervalMinutes: 60,
			deploy: true,
		});
		expect(pollInput("off", false, view({ intervalMinutes: 15 }))).toEqual({
			enabled: false,
			intervalMinutes: 15,
			deploy: false,
		});
		expect(pollInput("off", true, null)).toEqual({
			enabled: false,
			intervalMinutes: 5,
			deploy: true,
		});
	});

	test("only an app or worker with a git source can check", () => {
		expect(canPollGit({ kind: "app", sourceType: "git" })).toBe(true);
		expect(canPollGit({ kind: "worker", sourceType: "git" })).toBe(true);
		expect(canPollGit({ kind: "app", sourceType: "image" })).toBe(false);
		expect(canPollGit({ kind: "stack", sourceType: "compose" })).toBe(false);
		expect(canPollGit({ kind: "database", sourceType: "image" })).toBe(false);
		expect(canPollGit({ kind: "route", sourceType: "external" })).toBe(false);
	});

	describe("pollStatus", () => {
		const checkedAt = () => new Date(Date.now() - 2 * 60_000).toISOString();

		test("nothing to say without a row", () => {
			expect(pollStatus(null, undefined)).toBeNull();
		});

		test("the running head reads as running", () => {
			const status = pollStatus(
				view({
					checkedAt: checkedAt(),
					headSha: "abc1234def",
					headBranch: "main",
				}),
				"abc1234def",
			);
			expect(status).toEqual({
				tone: "muted",
				text: "Last checked 2m ago · main is at abc1234 (running)",
			});
		});

		test("a head that is not the running commit has no suffix", () => {
			const status = pollStatus(
				view({
					checkedAt: checkedAt(),
					headSha: "abc1234def",
					headBranch: "main",
				}),
				undefined,
			);
			expect(status?.text).toBe("Last checked 2m ago · main is at abc1234");
		});

		test("an update names the new commit and its branch", () => {
			expect(
				pollStatus(
					view({
						updateAvailable: true,
						headSha: "abc1234def",
						headBranch: "main",
						checkedAt: checkedAt(),
					}),
					"0000000",
				),
			).toEqual({
				tone: "update",
				text: "New commit abc1234 on main",
				headSha: "abc1234def",
			});
		});

		test("an error is the status, whatever else is known", () => {
			expect(
				pollStatus(
					view({ error: "remote unreachable", headSha: "abc1234def" }),
					undefined,
				),
			).toEqual({ tone: "error", text: "remote unreachable" });
		});

		test("enabled but never checked says so; a disabled one stays quiet", () => {
			expect(pollStatus(view(), undefined)?.text).toBe("Not checked yet");
			expect(pollStatus(view({ enabled: false }), undefined)).toBeNull();
		});
	});

	test("the card hint names the short sha only while an update waits", () => {
		expect(newCommitHint(null)).toBeNull();
		expect(newCommitHint(view({ headSha: "abc1234def" }))).toBeNull();
		expect(
			newCommitHint(view({ updateAvailable: true, headSha: "abc1234def" })),
		).toBe("New commit abc1234");
	});
});
