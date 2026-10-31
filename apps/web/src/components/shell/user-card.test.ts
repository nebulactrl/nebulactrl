import { describe, expect, test } from "bun:test";
import { userCaption, versionLabel } from "./user-card";

describe("userCaption", () => {
	test("shows the email under a name", () => {
		expect(
			userCaption({
				name: "Ada",
				email: "r@example.com",
				roleLabel: "Owner",
			}),
		).toBe("r@example.com");
	});

	test("shows the role when the name is the email", () => {
		expect(
			userCaption({ name: "", email: "r@example.com", roleLabel: "Owner" }),
		).toBe("Owner");
		expect(
			userCaption({
				name: "r@example.com",
				email: "r@example.com",
				roleLabel: "Owner",
			}),
		).toBe("Owner");
	});
});

describe("versionLabel", () => {
	test("prefixes a release with v once", () => {
		expect(versionLabel("0.9.2")).toBe("v0.9.2");
		expect(versionLabel("v0.9.2")).toBe("v0.9.2");
	});

	test("does not call a development build 'vdev'", () => {
		expect(versionLabel("dev")).toBe("development build");
	});
});
