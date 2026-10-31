import { describe, expect, test } from "bun:test";
import { makeService } from "#/features/canvas/lib/fixtures";
import { cardStatus } from "#/features/canvas/lib/status";
import {
	buildOnBody,
	etaAfter,
	fromLocalInput,
	isDirty,
	joinLocalInput,
	maintenanceSupported,
	OFF_BODY,
	previewPath,
	splitLocalInput,
	toFormState,
	toLocalInput,
	validateForm,
} from "./model";

const now = new Date(2026, 9, 26, 12, 0, 0);
const off = makeService().maintenance;

describe("datetime-local round trip", () => {
	test("keeps local wall-clock minutes", () => {
		const text = toLocalInput(new Date(2026, 10, 2, 9, 30));
		expect(text).toBe("2026-11-02T09:30");
		expect(fromLocalInput(text)?.getTime()).toBe(
			new Date(2026, 10, 2, 9, 30).getTime(),
		);
	});
	test("empty, partial and impossible values are null", () => {
		expect(fromLocalInput("")).toBeNull();
		expect(fromLocalInput("soon")).toBeNull();
		expect(fromLocalInput("2026-11-02T")).toBeNull();
		expect(fromLocalInput("T09:30")).toBeNull();
		expect(fromLocalInput("2026-02-30T09:30")).toBeNull();
		expect(fromLocalInput("2026-11-02T25:00")).toBeNull();
	});
	test("the date and time fields split and join without loss", () => {
		expect(splitLocalInput("2026-11-02T09:30")).toEqual({
			date: "2026-11-02",
			time: "09:30",
		});
		expect(splitLocalInput("")).toEqual({ date: "", time: "" });
		expect(joinLocalInput("2026-11-02", "09:30")).toBe("2026-11-02T09:30");
		expect(joinLocalInput("", "")).toBe("");
		expect(
			validateForm({ title: "", message: "", eta: "2026-11-02T" }, now).eta,
		).toContain("valid");
	});
});

describe("validateForm", () => {
	const ok = { title: "Upgrade", message: "Back soon", eta: "" };
	test("accepts empty and normal text", () => {
		expect(validateForm({ title: "", message: "", eta: "" }, now)).toEqual({});
		expect(validateForm(ok, now)).toEqual({});
	});
	test("bounds the text like the server (characters, not bytes)", () => {
		expect(
			validateForm({ ...ok, title: "é".repeat(80) }, now).title,
		).toBeUndefined();
		expect(validateForm({ ...ok, title: "x".repeat(81) }, now).title).toContain(
			"80",
		);
		expect(
			validateForm({ ...ok, message: "x".repeat(601) }, now).message,
		).toContain("600");
	});
	test("the expected return must be in the future", () => {
		expect(validateForm({ ...ok, eta: etaAfter(-5, now) }, now).eta).toContain(
			"future",
		);
		expect(
			validateForm({ ...ok, eta: etaAfter(30, now) }, now).eta,
		).toBeUndefined();
	});
});

describe("request bodies", () => {
	test("on carries only what is filled in", () => {
		expect(buildOnBody({ title: "  ", message: "", eta: "" })).toEqual({
			enabled: true,
		});
		const eta = etaAfter(60, now);
		const body = buildOnBody({ title: " Upgrade ", message: "Soon", eta });
		expect(body.title).toBe("Upgrade");
		expect(body.message).toBe("Soon");
		expect(body.eta).toBe(fromLocalInput(eta)?.toISOString() ?? "");
	});
	test("off is just the switch", () => {
		expect(OFF_BODY).toEqual({ enabled: false });
	});
});

describe("form state", () => {
	test("starts from the stored text and drops a passed ETA", () => {
		const m = {
			enabled: true,
			title: "T",
			message: "M",
			eta: new Date(2026, 9, 26, 11, 0).toISOString(),
			since: null,
		};
		expect(toFormState(m, now)).toEqual({ title: "T", message: "M", eta: "" });
	});
	test("dirty compares trimmed text and the ETA", () => {
		const m = { ...off, enabled: true, title: "T", message: "M" };
		expect(isDirty(m, { title: "T ", message: "M", eta: "" }, now)).toBe(false);
		expect(isDirty(m, { title: "T", message: "M2", eta: "" }, now)).toBe(true);
	});
});

describe("previewPath", () => {
	test("asks for the draft with escaped text", () => {
		const url = previewPath("svc-1", {
			title: "A & B",
			message: "<b>x</b>",
			eta: "",
		});
		expect(url.startsWith("/api/v1/services/svc-1/maintenance/preview?")).toBe(
			true,
		);
		const q = new URL(url, "http://x").searchParams;
		expect(q.get("draft")).toBe("true");
		expect(q.get("title")).toBe("A & B");
		expect(q.get("message")).toBe("<b>x</b>");
		expect(q.has("eta")).toBe(false);
	});
});

describe("maintenanceSupported", () => {
	const domain = makeService().domains;
	test("needs a domain", () => {
		expect(maintenanceSupported(makeService())).toBe(false);
	});
	test("not for tcp/udp routes", () => {
		const svc = makeService({
			kind: "route",
			domains: [{ hostname: "a.example.com" } as (typeof domain)[number]],
			config: {
				...makeService().config,
				route: { protocol: "tcp", upstreams: [] },
			},
		});
		expect(maintenanceSupported(svc)).toBe(false);
	});
});

describe("card status", () => {
	const serving = {
		currentDeploymentId: "dep-1",
		latestDeployment: null,
	};
	test("a service in maintenance shows the amber Maintenance pill", () => {
		const svc = makeService({
			...serving,
			maintenance: {
				enabled: true,
				title: "",
				message: "",
				eta: null,
				since: new Date(Date.now() - 120_000).toISOString(),
			},
		});
		const s = cardStatus(svc);
		expect(s.pill).toBe("Maintenance");
		expect(s.kind).toBe("warning");
		expect(s.line).toContain("503");
	});
	test("a failed deployment still wins over maintenance", () => {
		const svc = makeService({
			...serving,
			maintenance: { ...off, enabled: true },
			latestDeployment: {
				id: "d",
				status: "failed",
				trigger: "manual",
				strategy: "rolling",
				statusMessage: null,
				createdAt: "2026-01-01T00:00:00Z",
				finishedAt: null,
				releaseId: null,
				message: null,
				failedStep: null,
				currentStep: null,
			},
		});
		expect(cardStatus(svc).pill).toBe("Failed");
	});
});
