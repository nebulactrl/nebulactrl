import { describe, expect, test } from "bun:test";
import { rollbackPrompt } from "./rollback-prompt";

const base = {
	serviceName: "api",
	label: "sha256:c4717a8d",
	when: "2d ago",
	message: null,
	disclaimer: "Application image and configuration only.",
};

describe("rollbackPrompt", () => {
	test("production is named, the release is told by its age, and the key is guarded", () => {
		const prompt = rollbackPrompt({
			...base,
			environmentName: "Production",
			production: true,
		});
		expect(prompt.title).toBe(
			"Roll back api in production to the release from 2d ago?",
		);
		expect(prompt.guarded).toBe(true);
		expect(prompt.body).toBe(
			"sha256:c4717a8d · deployed 2d ago. Application image and configuration only.",
		);
	});

	test("the deploy message tells releases of one digest apart", () => {
		const prompt = rollbackPrompt({
			...base,
			environmentName: "production",
			production: true,
			message: "  fix login redirect ",
		});
		expect(prompt.body).toContain("“fix login redirect”");
	});

	test("another environment is named by its own name and is not guarded", () => {
		const prompt = rollbackPrompt({
			...base,
			environmentName: "staging",
			production: false,
		});
		expect(prompt.title).toBe(
			"Roll back api in staging to the release from 2d ago?",
		);
		expect(prompt.guarded).toBe(false);
	});

	test("an environment that has not loaded is guarded and not misnamed", () => {
		const prompt = rollbackPrompt({
			...base,
			environmentName: undefined,
			production: true,
		});
		expect(prompt.title).toBe(
			"Roll back api in this environment to the release from 2d ago?",
		);
		expect(prompt.guarded).toBe(true);
	});

	test("a release from just now reads naturally, and no time falls back to the label", () => {
		const now = rollbackPrompt({
			...base,
			environmentName: "staging",
			production: false,
			when: "just now",
		});
		expect(now.title).toContain("from a moment ago");
		const unknown = rollbackPrompt({
			...base,
			environmentName: "staging",
			production: false,
			when: "",
		});
		expect(unknown.title).toBe("Roll back api in staging to sha256:c4717a8d?");
	});
});
