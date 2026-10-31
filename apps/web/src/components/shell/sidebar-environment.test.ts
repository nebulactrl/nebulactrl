import { describe, expect, test } from "bun:test";
import { resolveSidebarEnvironment } from "./sidebar-environment";
import type { ProjectContext } from "./use-project-context";

const remembered = { projectId: "old", envSlug: "production" };

function projectContext(over: Partial<ProjectContext>): ProjectContext {
	return {
		projectId: "p1",
		project: undefined,
		environments: [],
		environment: undefined,
		envSlug: undefined,
		...over,
	};
}

describe("resolveSidebarEnvironment", () => {
	test("a project of another team is never offered", () => {
		expect(
			resolveSidebarEnvironment({
				context: undefined,
				remembered,
				projectIds: new Set(["mine"]),
			}),
		).toBeUndefined();
	});

	test("a team with no projects offers nothing", () => {
		expect(
			resolveSidebarEnvironment({
				context: undefined,
				remembered,
				projectIds: new Set(),
			}),
		).toBeUndefined();
	});

	test("offers nothing until the team's projects have loaded", () => {
		expect(
			resolveSidebarEnvironment({
				context: undefined,
				remembered,
				projectIds: undefined,
			}),
		).toBeUndefined();
	});

	test("offers the remembered environment while its project is the team's", () => {
		expect(
			resolveSidebarEnvironment({
				context: undefined,
				remembered,
				projectIds: new Set(["old"]),
			}),
		).toEqual(remembered);
	});

	test("the environment on screen wins", () => {
		expect(
			resolveSidebarEnvironment({
				context: projectContext({ projectId: "p1", envSlug: "staging" }),
				remembered,
				projectIds: new Set(),
			}),
		).toEqual({ projectId: "p1", envSlug: "staging" });
	});
});
