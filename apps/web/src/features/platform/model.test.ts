import { describe, expect, test } from "bun:test";
import type {
	PlatformKindView,
	PlatformListing,
	PlatformServiceView,
} from "#/api/gen/types.gen";
import {
	anyBusy,
	availability,
	canvasTarget,
	createFailure,
	detailEntries,
	detailLabel,
	EMPTY_DRAFT,
	firstProblem,
	hostnameRequired,
	pollInterval,
	provisionBody,
	REMOVAL_IMPACT,
	removalBlocker,
	removeQuery,
	type SetupDraft,
	setupProblems,
	statusSummary,
	stepRows,
	stepWords,
	validateHostname,
	validatePublicUrl,
	wiringTarget,
} from "./model";

function service(over: Partial<PlatformServiceView> = {}): PlatformServiceView {
	return {
		id: "svc-1",
		kind: "headscale",
		label: "Tailnet control server (Headscale)",
		status: "ready",
		step: "done",
		message: "",
		hostname: "headscale.example.com",
		publicUrl: "https://headscale.example.com",
		createdAt: "2026-10-27T10:00:00Z",
		updatedAt: "2026-10-27T10:00:00Z",
		details: {},
		services: [],
		warnings: [],
		...over,
	};
}

function catalog(over: Partial<PlatformKindView> = {}): PlatformKindView {
	return {
		kind: "headscale",
		label: "Headscale",
		product: "Headscale",
		summary: "",
		image: "",
		license: "BSD-3-Clause",
		agpl: false,
		footprint: "",
		holdsData: false,
		egress: false,
		...over,
	};
}

function listing(kinds: PlatformKindView[]): PlatformListing {
	return { kinds };
}

const draft = (over: Partial<SetupDraft> = {}): SetupDraft => ({
	...EMPTY_DRAFT,
	serverId: "srv-1",
	...over,
});

describe("stepWords and statusSummary", () => {
	test("each step has plain words", () => {
		expect(stepWords("configure")).toBe("Staging");
		expect(stepWords("deploy")).toBe("Deploying");
		expect(stepWords("await")).toBe("Waiting for it to answer");
		expect(stepWords("wire")).toBe("Connecting");
	});

	test("provisioning shows the step, with a busy spinner kind", () => {
		expect(statusSummary({ status: "provisioning", step: "await" })).toEqual({
			kind: "deploying",
			label: "Waiting for it to answer",
			busy: true,
		});
	});

	test("ready, failed and removing say so", () => {
		expect(statusSummary({ status: "ready", step: "done" })).toEqual({
			kind: "live",
			label: "Ready",
			busy: false,
		});
		expect(statusSummary({ status: "failed", step: "deploy" }).kind).toBe(
			"failed",
		);
		expect(statusSummary({ status: "removing", step: "done" })).toMatchObject({
			label: "Removing",
			busy: true,
		});
	});
});

describe("stepRows", () => {
	test("stages before the current one are done", () => {
		const rows = stepRows({ status: "provisioning", step: "await" });
		expect(rows.map((row) => row.state)).toEqual([
			"done",
			"done",
			"current",
			"todo",
		]);
	});

	test("a failed set-up marks the stage it stopped at", () => {
		const rows = stepRows({ status: "failed", step: "deploy" });
		expect(rows.map((row) => row.state)).toEqual([
			"done",
			"failed",
			"todo",
			"todo",
		]);
	});

	test("a ready service has every stage done", () => {
		expect(
			stepRows({ status: "ready", step: "done" }).every(
				(row) => row.state === "done",
			),
		).toBe(true);
	});
});

describe("polling", () => {
	test("polls fast while anything provisions", () => {
		expect(pollInterval(true)).toBe(3_000);
		expect(pollInterval(false)).toBe(15_000);
		const busy = listing([
			catalog({ instance: service({ status: "provisioning" }) }),
		]);
		expect(anyBusy(busy)).toBe(true);
		expect(anyBusy(listing([catalog({ instance: service() })]))).toBe(false);
		expect(anyBusy(undefined)).toBe(false);
	});
});

describe("validation", () => {
	test("hostnames follow the control plane's rules; empty is allowed", () => {
		expect(validateHostname("")).toBeNull();
		expect(validateHostname("headscale.example.com")).toBeNull();
		expect(validateHostname("Headscale.Example.com.")).toBeNull();
		expect(validateHostname("https://headscale.example.com")).toMatch(
			/hostname only/,
		);
		expect(validateHostname("localhost")).toMatch(/two parts/);
		expect(validateHostname("-a.example.com")).toMatch(/hyphen/);
		expect(validateHostname("a_b.example.com")).toMatch(/letters, digits/);
		expect(validateHostname("192.0.2.1")).toMatch(/not an IP/);
		expect(validateHostname(`${"a".repeat(64)}.example.com`)).toMatch(
			/1 to 63/,
		);
	});

	test("a public URL is http(s) without a path and must use the hostname", () => {
		expect(validatePublicUrl("", "")).toBeNull();
		expect(
			validatePublicUrl(
				"https://headscale.example.com",
				"headscale.example.com",
			),
		).toBeNull();
		expect(
			validatePublicUrl(
				"http://headscale.example.com:8080",
				"headscale.example.com",
			),
		).toBeNull();
		expect(validatePublicUrl("ftp://headscale.example.com", "")).toMatch(
			/http or https/,
		);
		expect(validatePublicUrl("https://headscale.example.com/x", "")).toMatch(
			/path/,
		);
		expect(
			validatePublicUrl("https://other.example.com", "headscale.example.com"),
		).toMatch(/must use the hostname/);
		expect(validatePublicUrl("nope", "")).toMatch(/full address/);
	});
});

describe("setupProblems", () => {
	const opts = { hostnameNeeded: false };

	test("a clean draft has none", () => {
		expect(setupProblems(draft(), opts)).toEqual({});
		expect(firstProblem({})).toBeNull();
	});

	test("a missing server is a problem", () => {
		expect(setupProblems(draft({ serverId: "" }), opts).server).toBeDefined();
	});

	test("an empty hostname is required once the server cannot generate one", () => {
		const problems = setupProblems(draft(), { hostnameNeeded: true });
		expect(problems.hostname).toMatch(/no domain/);
		expect(firstProblem(problems)).toBe(problems.hostname ?? null);
		expect(
			setupProblems(draft({ hostname: "headscale.example.com" }), {
				hostnameNeeded: true,
			}),
		).toEqual({});
	});

	test("a server with neither a base domain nor a public address needs a name", () => {
		expect(hostnameRequired({ baseDomain: null, publicIp: null })).toBe(true);
		expect(hostnameRequired({ baseDomain: "x.dev", publicIp: null })).toBe(
			false,
		);
		expect(hostnameRequired({ baseDomain: null, publicIp: "192.0.2.4" })).toBe(
			false,
		);
		expect(hostnameRequired(undefined)).toBe(false);
	});
});

describe("provisionBody", () => {
	test("sends only what was typed", () => {
		expect(provisionBody(draft(), {})).toEqual({
			kind: "headscale",
			serverId: "srv-1",
		});
	});

	test("normalises the hostname and sends replace only when asked", () => {
		expect(
			provisionBody(draft({ hostname: " Headscale.Example.com. " }), {}),
		).toEqual({
			kind: "headscale",
			serverId: "srv-1",
			hostname: "headscale.example.com",
		});
		expect(provisionBody(draft(), { replace: true })).toEqual({
			kind: "headscale",
			serverId: "srv-1",
			replace: true,
		});
	});
});

describe("availability: whether Headscale can be set up now", () => {
	const none = catalog();
	const ctx = { usableServers: 1 };

	test("one that exists is not offered again, even when it failed", () => {
		const exists = catalog({ instance: service({ status: "failed" }) });
		expect(availability(exists, ctx)).toEqual({ state: "exists" });
	});

	test("it is available when a server is online", () => {
		expect(availability(none, ctx)).toEqual({ state: "available" });
	});

	test("no online server blocks it", () => {
		expect(availability(none, { usableServers: 0 }).state).toBe("no_server");
	});
});

describe("wiring", () => {
	test("Headscale is wired into the Tailscale integration", () => {
		expect(wiringTarget("tailscale_integration")).toMatchObject({
			slug: "integrations",
		});
	});
});

describe("createFailure: 409 and 422 codes", () => {
	test("integration_exists offers to replace", () => {
		expect(
			createFailure("integration_exists", "A Tailscale integration").action,
		).toBe("replace");
	});

	test("no_base_domain asks for a hostname", () => {
		const failure = createFailure("no_base_domain", "raw");
		expect(failure.action).toBe("hostname");
		expect(failure.message).toMatch(/Enter the hostname/);
	});

	test("the server's sentence is kept, with a next step for the common ones", () => {
		expect(
			createFailure("acme_email_required", "Set an ACME email.").message,
		).toMatch(/^Set an ACME email\./);
		expect(
			createFailure("proxy_unavailable", "The proxy is down.").message,
		).toMatch(/Pick another server/);
		expect(createFailure("platform_service_exists", "Already.").action).toBe(
			"exists",
		);
		expect(createFailure("public_url_required", "Set the URL.")).toEqual({
			action: "none",
			message: "Set the URL.",
		});
		expect(createFailure(undefined, "Boom")).toEqual({
			action: "none",
			message: "Boom",
		});
	});
});

describe("removal", () => {
	test("the server's reason is kept verbatim with a hint for what to do", () => {
		const inUse = removalBlocker(
			"integration_in_use",
			"The tailnet is in use.",
		);
		expect(inUse.message).toBe("The tailnet is in use.");
		expect(inUse.hint).toMatch(/Stop whatever uses/);
		expect(removalBlocker("something_else", "x")).toEqual({
			message: "x",
			hint: "",
		});
	});

	test("the delete query confirms with the kind and sends only the chosen flag", () => {
		expect(removeQuery({ deleteData: false })).toEqual({
			confirm: "headscale",
		});
		expect(removeQuery({ deleteData: true })).toEqual({
			confirm: "headscale",
			deleteData: true,
		});
	});

	test("the Tailscale integration is removed first", () => {
		expect(REMOVAL_IMPACT).toMatch(/Tailscale/);
	});
});

describe("details and the canvas", () => {
	test("details have labels and a stable order", () => {
		expect(detailLabel("apiKeyExpires")).toBe("API key expires");
		expect(detailLabel("someNewKey")).toBe("Some new key");
		expect(
			detailEntries({
				extra: "x",
				apiKeyExpires: "2027-01-01",
				empty: "",
			}),
		).toEqual([
			["apiKeyExpires", "2027-01-01"],
			["extra", "x"],
		]);
	});

	test("the canvas of the platform project defaults to the platform environment", () => {
		expect(canvasTarget({})).toBeNull();
		expect(canvasTarget({ projectId: "p1" })).toEqual({
			projectId: "p1",
			envSlug: "platform",
		});
		expect(canvasTarget({ projectId: "p1", environmentSlug: "prod" })).toEqual({
			projectId: "p1",
			envSlug: "prod",
		});
	});
});
