import { describe, expect, test } from "bun:test";
import { pageCrumb } from "./page-crumb";

describe("pageCrumb", () => {
	test("names the team on the team's pages, so the h1 is not repeated", () => {
		for (const path of ["/", "/servers", "/servers/abc", "/settings/members"]) {
			expect(pageCrumb(path, "Acme")).toBe("Acme");
		}
		expect(pageCrumb("/status-pages/x", "Acme")).toBe("Acme");
		expect(pageCrumb("/status-pages/incidents/y", "Acme")).toBe("Acme");
	});

	test("names the scope on account and admin pages", () => {
		expect(pageCrumb("/account/security", "Acme")).toBe("Your account");
		expect(pageCrumb("/admin/updates", "Acme")).toBe("This installation");
	});

	test("shows nothing until the team's name is known", () => {
		expect(pageCrumb("/", undefined)).toBeUndefined();
	});

	test("says when no route owns the path", () => {
		expect(pageCrumb("/nope", "Acme")).toBe("Page not found");
	});
});
