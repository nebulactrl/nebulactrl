// biome-ignore-all lint/suspicious/noTemplateCurlyInString: the product's own `${{ }}` expression syntax, not a JavaScript template
import { describe, expect, test } from "bun:test";
import {
	DEFAULT_GENERATOR,
	generatesSecret,
	generatorExpression,
	generatorProblem,
	generatorShape,
} from "./generator";

describe("generator expressions", () => {
	test("the default is a 32 character secret", () => {
		expect(generatorExpression(DEFAULT_GENERATOR)).toBe("${{secret(32)}}");
	});
	test("a non-default alphabet or encoding is spelled out", () => {
		expect(
			generatorExpression({
				...DEFAULT_GENERATOR,
				length: 16,
				alphabet: "hex",
			}),
		).toBe("${{secret(16, hex)}}");
		expect(
			generatorExpression({
				...DEFAULT_GENERATOR,
				kind: "randomBytes",
				length: 32,
				encoding: "hex",
			}),
		).toBe("${{randomBytes(32, hex)}}");
		expect(
			generatorExpression({ ...DEFAULT_GENERATOR, kind: "randomBytes" }),
		).toBe("${{randomBytes(32)}}");
		expect(generatorExpression({ ...DEFAULT_GENERATOR, kind: "uuid" })).toBe(
			"${{uuid()}}",
		);
		expect(
			generatorExpression({
				...DEFAULT_GENERATOR,
				kind: "randomInt",
				min: 1,
				max: 6,
			}),
		).toBe("${{randomInt(1, 6)}}");
	});
	test("bad options are explained", () => {
		expect(generatorProblem({ ...DEFAULT_GENERATOR, length: 0 })).toContain(
			"1 and 1024",
		);
		expect(
			generatorProblem({ ...DEFAULT_GENERATOR, length: 2000 }),
		).not.toBeNull();
		expect(
			generatorProblem({
				...DEFAULT_GENERATOR,
				kind: "randomInt",
				min: 5,
				max: 1,
			}),
		).toContain("minimum");
		expect(generatorProblem(DEFAULT_GENERATOR)).toBeNull();
	});
	test("the shape never contains a value", () => {
		expect(generatorShape(DEFAULT_GENERATOR)).toBe(
			"32 characters: letters and digits",
		);
		expect(generatesSecret("secret")).toBe(true);
		expect(generatesSecret("uuid")).toBe(false);
	});

	test("problems name the unit and reject non-whole numbers", () => {
		expect(
			generatorProblem({
				...DEFAULT_GENERATOR,
				kind: "randomBytes",
				length: 0,
			}),
		).toBe("Use between 1 and 1024 bytes.");
		expect(generatorProblem({ ...DEFAULT_GENERATOR, length: 1.5 })).toBe(
			"Use between 1 and 1024 characters.",
		);
		expect(
			generatorProblem({
				...DEFAULT_GENERATOR,
				kind: "randomInt",
				min: 0.5,
				max: 3,
			}),
		).toBe("Use whole numbers.");
		expect(
			generatorProblem({
				...DEFAULT_GENERATOR,
				kind: "randomInt",
				min: 2,
				max: 2,
			}),
		).toBeNull();
		expect(generatorProblem({ ...DEFAULT_GENERATOR, kind: "uuid" })).toBeNull();
	});
	test("every kind has a shape that states its parameters", () => {
		expect(
			generatorShape({ ...DEFAULT_GENERATOR, kind: "randomBytes", length: 16 }),
		).toBe("16 random bytes, base64-encoded");
		expect(
			generatorShape({
				...DEFAULT_GENERATOR,
				kind: "randomInt",
				min: 3,
				max: 9,
			}),
		).toBe("a whole number from 3 to 9");
		expect(generatorShape({ ...DEFAULT_GENERATOR, kind: "uuid" })).toBe(
			"a random UUID (version 4)",
		);
		expect(generatorShape({ ...DEFAULT_GENERATOR, alphabet: "hex" })).toBe(
			"32 characters: hex (0-9, a-f)",
		);
	});
	test("only random text and bytes make a secret", () => {
		expect(generatesSecret("randomBytes")).toBe(true);
		expect(generatesSecret("randomInt")).toBe(false);
	});
});
