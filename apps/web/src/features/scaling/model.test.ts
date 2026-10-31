import { describe, expect, test } from "bun:test";
import { makeService } from "#/features/canvas/lib/fixtures";
import {
	appliedToast,
	buildScalingBody,
	initialScaleProcess,
	processCanScale,
	readinessText,
	scalableProcesses,
	scaleEventText,
	scaleRows,
	scaleSubject,
	scalingOfProcess,
	scalingSummary,
	toFormState,
	usesServiceScaling,
	validateScaling,
} from "./model";

const base = makeService().scaling;
const autoscaling = { enabled: true, min: 1, max: 3, targetCpuPercent: 70 };

describe("validateScaling", () => {
	test("accepts the defaults", () => {
		expect(validateScaling(toFormState(base))).toEqual({});
		expect(
			validateScaling({ ...toFormState(base), mode: "autoscale" }),
		).toEqual({});
	});

	test("manual replicas stay within 1–10", () => {
		const state = toFormState(base);
		expect(validateScaling({ ...state, replicas: 0 }).replicas).toBeDefined();
		expect(validateScaling({ ...state, replicas: 11 }).replicas).toBeDefined();
		expect(validateScaling({ ...state, replicas: 2.5 }).replicas).toBeDefined();
		expect(validateScaling({ ...state, replicas: 10 })).toEqual({});
	});

	test("autoscale needs min ≤ max and a 20–95 target", () => {
		const state = { ...toFormState(base), mode: "autoscale" as const };
		expect(validateScaling({ ...state, min: 4, max: 2 }).max).toBeDefined();
		expect(validateScaling({ ...state, min: 0 }).min).toBeDefined();
		expect(validateScaling({ ...state, max: 11 }).max).toBeDefined();
		expect(
			validateScaling({ ...state, targetCpuPercent: 19 }).targetCpuPercent,
		).toBeDefined();
		expect(
			validateScaling({ ...state, targetCpuPercent: 96 }).targetCpuPercent,
		).toBeDefined();
		expect(
			validateScaling({ ...state, targetCpuPercent: Number.NaN })
				.targetCpuPercent,
		).toBeDefined();
		expect(
			validateScaling({ ...state, min: 2, max: 2, targetCpuPercent: 95 }),
		).toEqual({});
	});

	test("ignores autoscale fields in manual mode", () => {
		expect(validateScaling({ ...toFormState(base), min: 9, max: 1 })).toEqual(
			{},
		);
	});
});

describe("buildScalingBody", () => {
	test("sends nothing when nothing changed", () => {
		expect(buildScalingBody(base, toFormState(base))).toBeNull();
		const on = { ...base, autoscaling };
		expect(buildScalingBody(on, toFormState(on))).toBeNull();
	});

	test("manual sends only the replica count", () => {
		expect(
			buildScalingBody(base, { ...toFormState(base), replicas: 3 }),
		).toEqual({ replicas: 3 });
	});

	test("switching to manual turns autoscaling off in the same body", () => {
		const on = { ...base, replicas: 2, ready: 2, autoscaling };
		expect(
			buildScalingBody(on, { ...toFormState(on), mode: "manual" }),
		).toEqual({
			autoscaling: { enabled: false },
		});
		expect(
			buildScalingBody(on, { ...toFormState(on), mode: "manual", replicas: 4 }),
		).toEqual({ autoscaling: { enabled: false }, replicas: 4 });
	});

	test("autoscale sends the whole policy and never a replica count", () => {
		expect(
			buildScalingBody(base, {
				mode: "autoscale",
				replicas: 5,
				min: 2,
				max: 6,
				targetCpuPercent: 60,
			}),
		).toEqual({
			autoscaling: { enabled: true, min: 2, max: 6, targetCpuPercent: 60 },
		});
	});
});

describe("text", () => {
	test("scalingSummary", () => {
		expect(scalingSummary({ ...base, replicas: 3, ready: 2 })).toBe(
			"2/3 replicas",
		);
		expect(scalingSummary({ ...base, replicas: 1, ready: 1 })).toBe(
			"1/1 replica",
		);
		expect(
			scalingSummary({ ...base, replicas: 3, ready: 2, autoscaling }),
		).toBe("2/3 replicas · autoscaling 1–3 @ 70% CPU");
	});

	test("readinessText", () => {
		expect(readinessText({ ...base, replicas: 2, ready: 2 })).toBe(
			"2 of 2 replicas ready",
		);
		expect(readinessText({ ...base, replicas: 2, ready: 1 })).toBe(
			"1 of 2 ready, starting",
		);
		expect(readinessText({ ...base, replicas: 1, ready: 2 })).toBe(
			"2 of 1 ready, stopping",
		);
	});

	test("scaleEventText names the autoscaler for the system actor", () => {
		const event = {
			at: "2026-10-26T10:00:00Z",
			from: 2,
			to: 3,
			reason: "CPU 84% > 70% for 2m",
			actor: "system",
		};
		expect(scaleEventText(event)).toBe(
			"Scaled 2 → 3 · CPU 84% > 70% for 2m · autoscaler",
		);
		expect(
			scaleEventText({ ...event, reason: "Set manually", actor: "alice" }),
		).toBe("Scaled 2 → 3 · Set manually · alice");
	});

	test("appliedToast", () => {
		const state = toFormState(base);
		expect(appliedToast("api", { ...state, replicas: 2 })).toBe(
			"Scaling api to 2 replicas",
		);
		expect(appliedToast("api", { ...state, replicas: 1 })).toBe(
			"Scaling api to 1 replica",
		);
		expect(appliedToast("api", { ...state, mode: "autoscale" })).toBe(
			"Autoscaling api between 1 and 3 replicas",
		);
	});
});

describe("processes", () => {
	const scaling = (over: Partial<typeof base> = {}) => ({ ...base, ...over });
	const process = (
		name: string,
		isDefault: boolean,
		over: Partial<typeof base> = {},
		removed = false,
	) => ({
		name,
		default: isDefault,
		port: 0,
		command: [],
		singleton: false,
		stopGraceSeconds: 30,
		scaling: scaling(over),
		observed: null,
		pending: false,
		removed,
	});
	const service = makeService({
		name: "shop",
		scaling: scaling({ replicas: 2, ready: 2 }),
		processes: [
			process("web", true, { replicas: 2, ready: 2 }),
			process("worker", false, { replicas: 3, ready: 1 }),
			process("clock", false, {
				eligible: false,
				ineligibleReason: "Singleton processes run exactly one instance",
			}),
			process("old", false, {}, true),
		],
	});
	const plain = makeService({ name: "api" });

	test("a service without processes has none to pick and scales as a whole", () => {
		expect(scalableProcesses(plain)).toEqual([]);
		expect(initialScaleProcess(plain, "worker")).toBeUndefined();
		expect(scaleRows(plain)).toEqual([]);
		expect(usesServiceScaling(plain)).toBe(true);
		expect(scalingOfProcess(plain, "worker")).toBe(plain.scaling);
		expect(scaleSubject(plain)).toBe("api");
	});

	test("the processes to pick are the ones the release will run, default first", () => {
		expect(scalableProcesses(service)).toEqual([
			{ name: "web", isDefault: true },
			{ name: "worker", isDefault: false },
			{ name: "clock", isDefault: false },
		]);
	});

	test("the dialog opens on the requested process, else the default", () => {
		expect(initialScaleProcess(service, "worker")).toBe("worker");
		expect(initialScaleProcess(service)).toBe("web");
		expect(initialScaleProcess(service, "gone")).toBe("web");
		expect(initialScaleProcess(service, "old")).toBe("web");
	});

	test("the default process and unknown names use the service's own endpoint", () => {
		expect(usesServiceScaling(service)).toBe(true);
		expect(usesServiceScaling(service, "web")).toBe(true);
		expect(usesServiceScaling(service, "gone")).toBe(true);
		expect(usesServiceScaling(service, "worker")).toBe(false);
	});

	test("a named process reads its own scaling", () => {
		expect(scalingOfProcess(service, "worker").replicas).toBe(3);
		expect(scalingOfProcess(service, "web")).toBe(service.scaling);
		expect(scalingOfProcess(service).replicas).toBe(2);
	});

	test("a process scales by its own eligibility", () => {
		expect(processCanScale(service, "worker")).toBe(true);
		expect(processCanScale(service, "clock")).toBe(false);
	});

	test("the subject names the process only when there are several", () => {
		expect(scaleSubject(service, "worker")).toBe("worker of shop");
		expect(scaleSubject(service)).toBe("shop");
	});

	test("rows say what each process runs, and why one cannot scale", () => {
		const rows = scaleRows(service);
		expect(rows.map((row) => row.name)).toEqual(["web", "worker", "clock"]);
		expect(rows[1]?.text).toBe("1/3 replicas");
		expect(rows[1]?.reason).toBeNull();
		expect(rows[2]).toMatchObject({
			eligible: false,
			text: "Runs one instance",
			reason: "Singleton processes run exactly one instance",
		});
	});

	test("rows carry the autoscaling policy", () => {
		const scaled = makeService({
			processes: [
				process("web", true, { replicas: 1, ready: 1 }),
				process("worker", false, {
					replicas: 2,
					ready: 2,
					autoscaling,
				}),
			],
		});
		expect(scaleRows(scaled)[1]?.text).toBe(
			"2/2 replicas · autoscaling 1–3 @ 70% CPU",
		);
	});
});
