import { describe, expect, test } from "bun:test";
import {
	absoluteTime,
	count,
	formatBytes,
	formatDuration,
	formatMegabytes,
	relativeTime,
	repository,
	shortDigest,
	shortSHA256,
} from "./format";

describe("relativeTime", () => {
	const now = Date.now();
	const isoSecondsAgo = (seconds: number) =>
		new Date(now - seconds * 1_000).toISOString();

	test.each([
		["absent", undefined, ""],
		["null", null, ""],
		["9 seconds ago reads just now", isoSecondsAgo(9), "just now"],
		["11 seconds ago reads seconds", isoSecondsAgo(11), "11s ago"],
		[
			"59 minutes ago reads minutes, not an hour",
			isoSecondsAgo(59 * 60),
			"59m ago",
		],
		["25 hours ago promotes to a day", isoSecondsAgo(25 * 60 * 60), "1d ago"],
		["a future instant reads in", isoSecondsAgo(-6 * 60 - 5), "in 6m"],
	])("%s", (_name, input, expected) => {
		expect(relativeTime(input)).toBe(expected);
	});
});

describe("absoluteTime", () => {
	test("renders a fixed UTC timestamp regardless of host timezone", () => {
		expect(absoluteTime("2026-09-20T18:42:05.123Z")).toBe(
			"2026-09-20 18:42:05 UTC",
		);
	});
});

describe("shortDigest", () => {
	test("a well-formed digest reference shortens to eight hex characters", () => {
		expect(
			shortDigest(
				"ghcr.io/acme/api@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			),
		).toBe("sha256:01234567");
	});

	test("a tag reference (no digest) is returned unchanged", () => {
		expect(shortDigest("ghcr.io/acme/api:latest")).toBe(
			"ghcr.io/acme/api:latest",
		);
	});

	test("nonsense input never throws and is returned unchanged", () => {
		expect(shortDigest("not an image reference")).toBe(
			"not an image reference",
		);
	});
});

describe("shortSHA256", () => {
	test("a well-formed 64-char digest shortens to eight hex characters", () => {
		expect(
			shortSHA256(
				"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef".slice(
					0,
					64,
				),
			),
		).toBe("01234567…");
	});

	test("null/undefined reads not reported", () => {
		expect(shortSHA256(null)).toBe("not reported");
		expect(shortSHA256(undefined)).toBe("not reported");
	});

	test("anything that isn't 64 lowercase hex characters is returned unchanged", () => {
		expect(shortSHA256("not-a-digest")).toBe("not-a-digest");
		expect(shortSHA256("sha256:0123")).toBe("sha256:0123");
	});
});

describe("repository", () => {
	test("strips the digest suffix", () => {
		expect(
			repository(
				"ghcr.io/acme/api@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			),
		).toBe("ghcr.io/acme/api");
	});

	test("a reference without @ is returned unchanged", () => {
		expect(repository("ghcr.io/acme/api")).toBe("ghcr.io/acme/api");
	});
});

describe("count", () => {
	test.each([
		["singular", 1, "node", undefined, "1 node"],
		["plural", 3, "node", undefined, "3 nodes"],
		["zero is plural, not absent", 0, "node", undefined, "0 nodes"],
		["irregular plural", 2, "process", "processes", "2 processes"],
	])("%s", (_name, n, singular, plural, expected) => {
		expect(count(n, singular, plural)).toBe(expected);
	});
});

describe("formatDuration", () => {
	test.each([
		["sub-second reads milliseconds", 340, "340ms"],
		["seconds reads one decimal", 10_600, "10.6s"],
		["minutes reads zero-padded seconds", 302_000, "5m 02s"],
		["hours reads zero-padded minutes", 3_840_000, "1h 04m"],
	])("%s", (_name, ms, expected) => {
		expect(formatDuration(ms)).toBe(expected);
	});
});

describe("formatBytes", () => {
	test.each([
		["binary mebibytes", 512 * 1024 * 1024, "512 MiB"],
		["binary gibibytes", 2 * 1024 * 1024 * 1024, "2 GiB"],
		["zero is a real reading", 0, "0 B"],
	])("%s", (_name, input, expected) => {
		expect(formatBytes(input)).toBe(expected);
	});
});

describe("formatMegabytes", () => {
	test("stays in MiB under a GiB and switches to GiB with one decimal", () => {
		expect(formatMegabytes(212.4)).toBe("212 MiB");
		expect(formatMegabytes(1023)).toBe("1023 MiB");
		expect(formatMegabytes(1024)).toBe("1 GiB");
		expect(formatMegabytes(16040.7)).toBe("15.7 GiB");
		expect(formatMegabytes(-5)).toBe("0 MiB");
	});
});
