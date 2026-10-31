import { describe, expect, test } from "bun:test";
import type { Deployment } from "#/api/models";
import type { DebugCommand, DebugRelease } from "./queries";
import {
	buildDebugReport,
	REPORT_MAX_LOG_LINES,
	scrubReportText,
} from "./report";

const deployment = {
	id: "dep-1",
	serviceId: "svc-1",
	releaseId: "rel-1",
	serverId: "srv-1",
	buildServerId: null,
	trigger: "manual",
	actorType: "user",
	actorLabel: "alice@example.com",
	status: "failed",
	strategy: "rolling",
	statusMessage: "Health check failed",
	message: null,
	rolledBackFrom: null,
	generation: 7,
	createdAt: "2026-10-27T10:00:00.000Z",
	startedAt: "2026-10-27T10:00:00.500Z",
	finishedAt: "2026-10-27T10:00:09.000Z",
	release: null,
	waitingFor: null,
	commit: null,
	cancelRequestedAt: null,
	canceledAtStep: null,
} as unknown as Deployment;

const release = {
	id: "rel-1",
	serviceId: "svc-1",
	deploymentId: "dep-1",
	generation: 7,
	createdAt: "2026-10-27T10:00:00.000Z",
	imageRef: "nginx:1",
	source: { type: "image", image: "nginx:1", digest: "nginx@sha256:abc" },
	build: null,
	placement: {
		strategy: "rolling",
		targetServerId: "srv-1",
		targetServerName: "agent1",
		buildServerId: null,
		buildServerName: "",
		registryId: null,
		registryRef: "",
		platform: "",
	},
	config: { image: "nginx:1", port: 80 },
	variables: [
		{
			key: "API_KEY",
			secret: true,
			reference: false,
			platform: false,
			external: false,
			scope: "runtime",
		},
		{
			key: "PORT",
			secret: false,
			reference: false,
			platform: true,
			external: false,
			scope: "runtime",
		},
	],
	variablesPruned: false,
} as unknown as DebugRelease;

const commands = [
	{
		id: "cmd-1",
		kind: "deploy",
		serverId: "srv-1",
		serverName: "agent1",
		status: "failed",
		outcome: "failed",
		error: "",
		payload: { envKeys: ["API_KEY"], token: "••••••" },
		result: null,
		createdAt: "2026-10-27T10:00:00.100Z",
		sentAt: "2026-10-27T10:00:00.200Z",
		ackedAt: "2026-10-27T10:00:00.300Z",
		finishedAt: "2026-10-27T10:00:09.000Z",
		expiresAt: "2026-10-27T10:15:00.000Z",
	},
] as unknown as DebugCommand[];

function report(extra: Partial<Parameters<typeof buildDebugReport>[0]> = {}) {
	return buildDebugReport({
		deployment,
		serviceName: "api",
		serverName: "agent1",
		buildServerName: null,
		events: [
			{
				id: 1,
				step: "status",
				status: "queued",
				message: "",
				createdAt: "2026-10-27T10:00:00.100Z",
			},
			{
				id: 2,
				step: "health",
				status: "failed",
				message: "did not pass\nsecond line",
				createdAt: "2026-10-27T10:00:08.100Z",
			},
		],
		release,
		commands,
		logLines: [
			{
				ts: "2026-10-27T10:00:01.000Z",
				stream: "debug",
				line: "health: probe failed",
			},
		],
		generatedAt: "2026-10-27T10:05:00.000Z",
		...extra,
	});
}

describe("buildDebugReport", () => {
	test("has every section with the deployment's facts", () => {
		const text = report();
		for (const heading of [
			"## Deployment",
			"## Timeline (2 events)",
			"## Release",
			"## Agent commands",
			"## Log (1 of 1 lines",
		]) {
			expect(text).toContain(heading);
		}
		expect(text).toContain("dep-1");
		expect(text).toContain("api (svc-1)");
		expect(text).toContain("alice@example.com");
		expect(text).toContain("agent1");
	});

	test("the timeline shows offsets from creation and from the previous event", () => {
		const text = report();
		expect(text).toContain("+100 ms");
		expect(text).toContain("+8.100 s");
		expect(text).toContain("+8.000 s");
		expect(text).toContain("status → queued");
		expect(text).toContain("health failed");
		expect(text).toContain("did not pass ⏎ second line");
	});

	test("variables are keys with flags, never values", () => {
		const text = report();
		expect(text).toContain("API_KEY  [secret]");
		expect(text).toContain("PORT  [platform]");
		expect(text).toContain("Configuration (secrets masked)");
	});

	test("commands show the redacted payload", () => {
		const text = report();
		expect(text).toContain("deploy cmd-1 on agent1");
		expect(text).toContain('"token": "••••••"');
	});

	test("says so when the release or commands are missing", () => {
		const text = report({
			release: null,
			commands: null,
			events: [],
			logLines: [],
		});
		expect(text).toContain("No release recorded");
		expect(text).toContain("No commands.");
		expect(text).toContain("No events recorded.");
		expect(text).toContain("No log lines.");
	});

	test("a pruned environment is stated", () => {
		const text = report({
			release: { ...release, variablesPruned: true, variables: [] },
		});
		expect(text).toContain("environment was pruned");
	});

	test("keeps the newest log lines and says how many were left out", () => {
		const many = Array.from({ length: REPORT_MAX_LOG_LINES + 25 }, (_, i) => ({
			stream: "debug",
			line: `line ${i}`,
		}));
		const text = report({ logLines: many });
		expect(text).toContain("… 25 older lines left out");
		expect(text).not.toContain("line 0\n");
		expect(text).toContain(`line ${REPORT_MAX_LOG_LINES + 24}`);
	});

	test("masks a credential that slipped through", () => {
		const text = report({
			logLines: [
				{ line: "connecting to postgres://app:hunter2hunter2@db:5432/app" },
				{ line: "Authorization: Bearer abcdefghijklmnop" },
				{ line: "API_KEY=sekret-canary-12345 started" },
			],
		});
		expect(text).not.toContain("hunter2hunter2");
		expect(text).not.toContain("abcdefghijklmnop");
		expect(text).not.toContain("sekret-canary-12345");
	});
});

describe("scrubReportText", () => {
	test("leaves ordinary text alone", () => {
		const line = "docker container create nc-web-1: created abc123 in 50ms";
		expect(scrubReportText(line)).toBe(line);
	});
	test("masks assignments of credential-named keys", () => {
		expect(scrubReportText("password: hunter22")).toBe("password: ••••••");
		expect(scrubReportText('token="abc def"')).toBe("token=••••••");
	});
});
