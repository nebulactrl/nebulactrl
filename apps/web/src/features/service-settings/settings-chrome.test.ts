import { describe, expect, test } from "bun:test";
import {
	registryHelpOpen,
	saveBarVisible,
	sectionHead,
	VARIABLES_HINT,
	variablesHint,
} from "./settings-chrome";
import {
	type CategoryId,
	categoriesFor,
	type SettingsSubject,
} from "./settings-nav-model";

const app: SettingsSubject = {
	kind: "app",
	sourceType: "image",
	oneshot: false,
};

const category = (id: CategoryId, subject: SettingsSubject = app) => {
	const found = categoriesFor(subject).find((c) => c.id === id);
	if (!found) throw new Error(`no ${id} category`);
	return found;
};

describe("saveBarVisible", () => {
	test("pages whose every control saves through the bar show it at rest", () => {
		for (const id of ["general", "deploy", "networking", "advanced"] as const) {
			expect(
				saveBarVisible({
					category: category(id),
					dirty: false,
					saveFailed: false,
				}),
			).toBe(true);
		}
	});

	test("Operations and the Danger zone hide it at rest", () => {
		for (const id of ["operations", "danger"] as const) {
			expect(
				saveBarVisible({
					category: category(id),
					dirty: false,
					saveFailed: false,
				}),
			).toBe(false);
		}
	});

	test("Resources, which opens on Scaling's own Apply, hides it at rest", () => {
		expect(
			saveBarVisible({
				category: category("resources"),
				dirty: false,
				saveFailed: false,
			}),
		).toBe(false);
	});

	test("a Compose stack's limits have no Scaling, so its Resources keeps the bar", () => {
		const stack: SettingsSubject = { ...app, kind: "stack" };
		expect(
			saveBarVisible({
				category: category("resources", stack),
				dirty: false,
				saveFailed: false,
			}),
		).toBe(true);
	});

	test("unsaved edits and a failed save show it anywhere", () => {
		for (const id of ["operations", "resources", "danger"] as const) {
			expect(
				saveBarVisible({
					category: category(id),
					dirty: true,
					saveFailed: false,
				}),
			).toBe(true);
			expect(
				saveBarVisible({
					category: category(id),
					dirty: false,
					saveFailed: true,
				}),
			).toBe(true);
		}
	});
});

describe("sectionHead", () => {
	test("drops a title and a line that repeat the category's", () => {
		const networking = category("networking");
		expect(
			sectionHead(networking, {
				title: "Networking",
				description: networking.description,
			}),
		).toEqual({ showTitle: false, showDescription: false });
	});

	test("keeps a title that says more than the category does", () => {
		const advanced = category("advanced");
		expect(
			sectionHead(advanced, {
				title: "Docker options",
				description: advanced.description,
			}),
		).toEqual({ showTitle: true, showDescription: false });
	});

	test("keeps the head of a section with its own words", () => {
		expect(
			sectionHead(category("deploy"), {
				title: "Health check",
				description: "A release goes live only once this passes.",
			}),
		).toEqual({ showTitle: true, showDescription: true });
	});

	test("never drops a description it cannot compare, or any head without a category", () => {
		expect(
			sectionHead(category("danger"), {
				title: "Danger zone",
				description: {},
			}),
		).toEqual({ showTitle: false, showDescription: true });
		expect(sectionHead(null, { title: "Networking" })).toEqual({
			showTitle: true,
			showDescription: true,
		});
	});
});

describe("variablesHint", () => {
	test("points a search for variables or secrets to the Variables tab", () => {
		for (const query of [
			"env",
			"ENV vars",
			"secret",
			"environment variables",
		]) {
			expect(variablesHint(query)).toBe(VARIABLES_HINT);
		}
	});

	test("says nothing for other searches", () => {
		for (const query of ["", "memory", "environmental", "secretary"]) {
			expect(variablesHint(query)).toBeNull();
		}
	});
});

describe("registryHelpOpen", () => {
	test("a public image keeps the steps behind their summary", () => {
		expect(
			registryHelpOpen({ authSet: false, username: "", password: "" }),
		).toBe(false);
		expect(
			registryHelpOpen({ authSet: false, username: "  ", password: "" }),
		).toBe(false);
	});

	test("stored or typed credentials open them", () => {
		expect(
			registryHelpOpen({ authSet: true, username: "", password: "" }),
		).toBe(true);
		expect(
			registryHelpOpen({ authSet: false, username: "alice", password: "" }),
		).toBe(true);
		expect(
			registryHelpOpen({ authSet: false, username: "", password: "x" }),
		).toBe(true);
	});
});
