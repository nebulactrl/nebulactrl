import { describe, expect, test } from "bun:test";
import type { IncidentView } from "#/api/gen/types.gen";
import { incidentKind, incidentLine } from "./incidents";

const base: IncidentView = {
	id: "i1",
	kind: "crashed",
	flapping: false,
	detail: "The container exited and has been down for 30s.",
	restarts: 0,
	openedAt: "2026-10-28T12:00:00Z",
	resolvedAt: null,
	resolution: "",
};

describe("health incident rows", () => {
	test("an open crash or loop is red, an open unhealthy check amber", () => {
		expect(incidentKind(base)).toBe("failed");
		expect(incidentKind({ ...base, kind: "restart_loop" })).toBe("failed");
		expect(incidentKind({ ...base, kind: "unhealthy" })).toBe("warning");
	});

	test("a closed incident is quiet whatever it was", () => {
		expect(incidentKind({ ...base, resolvedAt: "2026-10-28T12:05:00Z" })).toBe(
			"stopped",
		);
	});

	test("the line says what it was and how it ended", () => {
		expect(incidentLine(base)).toBe("Crashed");
		expect(
			incidentLine({ ...base, kind: "restart_loop", flapping: true }),
		).toBe("Restart loop · alerts paused");
		expect(
			incidentLine({
				...base,
				resolvedAt: "2026-10-28T12:05:00Z",
				resolution: "recovered",
			}),
		).toBe("Crashed · recovered");
		expect(
			incidentLine({
				...base,
				kind: "unhealthy",
				resolvedAt: "2026-10-28T12:05:00Z",
				resolution: "stopped",
			}),
		).toBe("Unhealthy · service stopped");
	});
});
