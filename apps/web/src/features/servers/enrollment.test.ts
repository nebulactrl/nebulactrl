import { describe, expect, test } from "bun:test";
import { QueryClient } from "@tanstack/react-query";
import type { EnrollmentToken } from "#/api/gen/types.gen";
import {
	cacheEnrollment,
	commandLines,
	enrollmentExpired,
	enrollmentKey,
} from "./enrollment";

const COMMAND =
	"curl -fsSL https://nebula.example/install-agent.sh | sudo sh -s -- --url https://nebula.example --token nct_enroll_abc";

describe("commandLines", () => {
	test("puts each flag and the pipe on a line of their own", () => {
		expect(commandLines(COMMAND)).toEqual([
			"curl -fsSL https://nebula.example/install-agent.sh",
			"| sudo sh -s --",
			"--url https://nebula.example",
			"--token nct_enroll_abc",
		]);
	});

	test("joins back to the original command", () => {
		expect(commandLines(COMMAND).join(" ")).toBe(COMMAND);
	});

	test("keeps a trailing dev flag on its own line", () => {
		expect(commandLines(`${COMMAND} --insecure-dev`).at(-1)).toBe(
			"--insecure-dev",
		);
	});

	test("leaves a single word alone", () => {
		expect(commandLines("ls")).toEqual(["ls"]);
	});
});

describe("enrollmentExpired", () => {
	test("is true at and after the expiry", () => {
		const token = { expiresAt: "2026-01-01T10:00:00Z" };
		expect(enrollmentExpired(token, Date.parse("2026-01-01T09:59:59Z"))).toBe(
			false,
		);
		expect(enrollmentExpired(token, Date.parse("2026-01-01T10:00:00Z"))).toBe(
			true,
		);
	});
});

describe("cacheEnrollment", () => {
	test("keeps the command under its server's key", () => {
		const queryClient = new QueryClient();
		const enrollment = {
			server: { id: "srv-1" },
			installCommand: COMMAND,
			token: "t",
			expiresAt: "2026-01-01T10:00:00Z",
		} as EnrollmentToken;
		cacheEnrollment(queryClient, enrollment);
		expect(
			queryClient.getQueryData<EnrollmentToken>(enrollmentKey("srv-1")),
		).toBe(enrollment);
		expect(queryClient.getQueryData(enrollmentKey("srv-2"))).toBeUndefined();
	});
});
