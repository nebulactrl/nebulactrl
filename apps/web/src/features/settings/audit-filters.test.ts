import { describe, expect, test } from "bun:test";
import {
	AUDIT_FILTERS,
	auditActionKind,
	auditFilterById,
	auditQuery,
} from "./audit-filters";

describe("audit filters", () => {
	test("lists All first, then one chip per action family", () => {
		expect(AUDIT_FILTERS.map((f) => f.label)).toEqual([
			"All",
			"Deployments",
			"Services",
			"Variables",
			"Secrets",
			"Servers",
			"Tokens",
			"Projects",
		]);
	});

	test("every family chip filters by its prefix (secrets spans secret. and secret_manager.)", () => {
		expect(
			AUDIT_FILTERS.filter((f) => f.id !== "all").map((f) => f.prefix),
		).toEqual([
			"deployment.",
			"service.",
			"variable.",
			"secret",
			"server.",
			"token.",
			"project.",
		]);
		for (const action of [
			"secret.fetch",
			"secret.reveal",
			"secret_manager.test",
		]) {
			expect(action.startsWith(auditFilterById("secrets").prefix ?? "")).toBe(
				true,
			);
		}
		expect(auditActionKind("secret.fetch.denied")).toBe("remove");
		expect(auditActionKind("secret.fetch")).toBe("change");
	});

	test("All sends no type at all", () => {
		expect(auditQuery("all", 50)).toEqual({ limit: 50 });
		expect("type" in auditQuery("all", 50)).toBe(false);
	});

	test("a family sends its prefix as `type`", () => {
		expect(auditQuery("deployments", 50)).toEqual({
			limit: 50,
			type: "deployment.",
		});
		expect(auditQuery("tokens", 20)).toEqual({ limit: 20, type: "token." });
	});

	test("each filter has its own empty-state copy", () => {
		expect(auditFilterById("deployments").empty.title).toBe(
			"No deployment events yet",
		);
		const titles = new Set(AUDIT_FILTERS.map((f) => f.empty.title));
		expect(titles.size).toBe(AUDIT_FILTERS.length);
	});
});

describe("auditActionKind", () => {
	test("reads the verb after the family", () => {
		expect(auditActionKind("token.create")).toBe("create");
		expect(auditActionKind("server.enroll")).toBe("create");
		expect(auditActionKind("domain.add")).toBe("create");
		expect(auditActionKind("deployment.cancel")).toBe("remove");
		expect(auditActionKind("service.delete")).toBe("remove");
		expect(auditActionKind("token.revoke")).toBe("remove");
		expect(auditActionKind("service.discard_change")).toBe("remove");
		expect(auditActionKind("service.update")).toBe("change");
		expect(auditActionKind("variable.set")).toBe("change");
		expect(auditActionKind("deployment.rollback")).toBe("change");
	});

	test("a nested verb counts by its last parts", () => {
		expect(auditActionKind("server.drain.start")).toBe("change");
		expect(auditActionKind("server.drain.cancel")).toBe("remove");
		expect(auditActionKind("server.enroll_token.create")).toBe("create");
	});
});
