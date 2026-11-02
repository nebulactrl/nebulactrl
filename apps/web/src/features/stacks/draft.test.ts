import { describe, expect, test } from "bun:test";
import {
	buildStackInput,
	type StackFields,
	toStackFields,
	validateStackFields,
} from "./draft";

const VALID: StackFields = {
	stackCompose: "services:\n  web:\n    image: nginx\n",
	stackWaitSeconds: "",
	stackResources: {},
};

describe("toStackFields", () => {
	test("is blank for a service that is not a stack", () => {
		expect(toStackFields(undefined)).toEqual({
			stackCompose: "",
			stackWaitSeconds: "",
			stackResources: {},
		});
		expect(toStackFields(null).stackCompose).toBe("");
	});

	test("fills the form from config.stack", () => {
		expect(
			toStackFields({
				compose: "services: {}",
				waitSeconds: 120,
				resources: {
					web: { memoryMb: 512, cpus: 0.5 },
					db: { memoryMb: 1024 },
				},
			}),
		).toEqual({
			stackCompose: "services: {}",
			stackWaitSeconds: "120",
			stackResources: {
				web: { memoryMb: "512", cpus: "0.5" },
				db: { memoryMb: "1024", cpus: "" },
			},
		});
	});

	test("a zero or missing wait timeout reads as the default (empty)", () => {
		expect(
			toStackFields({ compose: "x", waitSeconds: 0 }).stackWaitSeconds,
		).toBe("");
		expect(toStackFields({ compose: "x" }).stackWaitSeconds).toBe("");
	});
});

describe("validateStackFields", () => {
	test("accepts a compose file with default timeout and no limits", () => {
		expect(validateStackFields(VALID)).toEqual({});
	});

	test("an empty or blank Compose file is required", () => {
		expect(validateStackFields({ ...VALID, stackCompose: "" })).toEqual({
			stackCompose: "A Compose file is required.",
		});
		expect(validateStackFields({ ...VALID, stackCompose: " \n\t" })).toEqual({
			stackCompose: "A Compose file is required.",
		});
	});

	test("rejects wait timeouts outside 1 to 3600 seconds", () => {
		const message =
			"Wait timeout must be a whole number of seconds from 1 to 3600.";
		for (const bad of ["0", "3601", "1.5", "abc"]) {
			expect(
				validateStackFields({ ...VALID, stackWaitSeconds: bad })
					.stackWaitSeconds,
			).toBe(message);
		}
		for (const good of ["1", "300", "3600"]) {
			expect(validateStackFields({ ...VALID, stackWaitSeconds: good })).toEqual(
				{},
			);
		}
	});

	test("points at the limits when any component's limit is invalid", () => {
		expect(
			validateStackFields({
				...VALID,
				stackResources: { web: { memoryMb: "2", cpus: "" } },
			}).stackResources,
		).toBe("Fix the highlighted limits.");
		expect(
			validateStackFields({
				...VALID,
				stackResources: { web: { memoryMb: "64", cpus: "1" } },
			}),
		).toEqual({});
	});

	test("reports every problem at once", () => {
		expect(
			validateStackFields({
				stackCompose: "",
				stackWaitSeconds: "0",
				stackResources: { web: { memoryMb: "", cpus: "999" } },
			}),
		).toEqual({
			stackCompose: "A Compose file is required.",
			stackWaitSeconds:
				"Wait timeout must be a whole number of seconds from 1 to 3600.",
			stackResources: "Fix the highlighted limits.",
		});
	});
});

describe("buildStackInput", () => {
	test("sends only the compose text when nothing else is set", () => {
		expect(buildStackInput(VALID)).toEqual({ compose: VALID.stackCompose });
	});

	test("sends the compose text byte for byte, without trimming", () => {
		expect(
			buildStackInput({ ...VALID, stackCompose: "  services: {}\n\n" }).compose,
		).toBe("  services: {}\n\n");
	});

	test("includes the wait timeout as a number and the limits of components that have one", () => {
		expect(
			buildStackInput({
				stackCompose: "c",
				stackWaitSeconds: " 90 ",
				stackResources: {
					web: { memoryMb: "256", cpus: "1.5" },
					db: { memoryMb: "", cpus: "" },
				},
			}),
		).toEqual({
			compose: "c",
			waitSeconds: 90,
			resources: { web: { memoryMb: 256, cpus: 1.5 } },
		});
	});

	test("never sends components: the server derives them", () => {
		expect(Object.keys(buildStackInput(VALID))).toEqual(["compose"]);
	});
});
