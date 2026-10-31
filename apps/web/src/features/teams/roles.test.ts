import { describe, expect, test } from "bun:test";
import {
	absoluteInviteLink,
	assignableRoles,
	canChangeMember,
	canDeleteTeam,
	canManageMembers,
	describeDelivery,
	describeProjectAccess,
	isRole,
	ROLES,
	supportsProjectRestriction,
} from "./roles";

describe("role capabilities", () => {
	test("only owners and admins manage members", () => {
		expect(ROLES.filter((r) => canManageMembers(r))).toEqual([
			"owner",
			"admin",
		]);
		expect(canManageMembers(undefined)).toBe(false);
	});

	test("only owners make owners", () => {
		expect(assignableRoles("owner")).toEqual(ROLES);
		expect(assignableRoles("admin")).toEqual(["admin", "developer", "viewer"]);
		expect(assignableRoles("developer")).toEqual([]);
		expect(assignableRoles("viewer")).toEqual([]);
		expect(assignableRoles(undefined)).toEqual([]);
	});

	test("admins cannot touch owners", () => {
		expect(canChangeMember("owner", "owner")).toBe(true);
		expect(canChangeMember("admin", "owner")).toBe(false);
		expect(canChangeMember("admin", "admin")).toBe(true);
		expect(canChangeMember("developer", "viewer")).toBe(false);
	});

	test("owners and admins are never restricted to projects", () => {
		expect(ROLES.filter((r) => supportsProjectRestriction(r))).toEqual([
			"developer",
			"viewer",
		]);
		expect(describeProjectAccess("admin", ["a"])).toBe("All projects");
		expect(describeProjectAccess("developer", undefined)).toBe("All projects");
		expect(describeProjectAccess("developer", null)).toBe("All projects");
		expect(describeProjectAccess("developer", [])).toBe("No projects");
		expect(describeProjectAccess("viewer", ["a"])).toBe("1 project");
		expect(describeProjectAccess("viewer", ["a", "b"])).toBe("2 projects");
	});

	test("only owners delete a team", () => {
		expect(ROLES.filter((r) => canDeleteTeam(r))).toEqual(["owner"]);
	});

	test("isRole narrows unknown input", () => {
		expect(isRole("viewer")).toBe(true);
		expect(isRole("root")).toBe(false);
		expect(isRole(undefined)).toBe(false);
	});
});

describe("invite links", () => {
	test("a relative link gets the origin; an absolute one is kept", () => {
		expect(absoluteInviteLink("/invite/nci_x", "https://n.example.com/")).toBe(
			"https://n.example.com/invite/nci_x",
		);
		expect(
			absoluteInviteLink("https://a.test/invite/nci_x", "https://b.test"),
		).toBe("https://a.test/invite/nci_x");
		expect(absoluteInviteLink("invite/nci_x", "https://b.test")).toBe(
			"https://b.test/invite/nci_x",
		);
	});
});

describe("email delivery wording", () => {
	test("each outcome says what to do", () => {
		expect(describeDelivery("sent", "a@b.co")).toContain("emailed to a@b.co");
		expect(describeDelivery("link_only", undefined)).toContain(
			"Share this link",
		);
		expect(describeDelivery("not_configured", "a@b.co")).toContain(
			"no email channel",
		);
		expect(describeDelivery("not_configured", "a@b.co", "set X")).toContain(
			"set X",
		);
		expect(describeDelivery("failed", "a@b.co", "535 auth")).toContain(
			"535 auth",
		);
	});
});
