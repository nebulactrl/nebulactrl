// biome-ignore-all lint/suspicious/noTemplateCurlyInString: the product's own `${{ }}` expression syntax, not a JavaScript template
/**
 * Builders for the generator expressions a variable value may contain
 * (`${{secret(32)}}`). The control plane runs them once, when the variable is
 * written; the browser only writes the expression and never sees the result.
 */

export type GeneratorKind = "secret" | "randomBytes" | "uuid" | "randomInt";

export const ALPHABETS = [
	{ id: "alnum", label: "Letters and digits" },
	{ id: "hex", label: "Hex (0-9, a-f)" },
	{ id: "base64", label: "Base64 characters" },
	{ id: "base64url", label: "URL-safe base64 characters" },
	{ id: "digits", label: "Digits" },
	{ id: "lower", label: "Lowercase letters" },
] as const;
export type Alphabet = (typeof ALPHABETS)[number]["id"];

export const ENCODINGS = ["base64", "hex", "base64url"] as const;
export type Encoding = (typeof ENCODINGS)[number];

export interface GeneratorOptions {
	kind: GeneratorKind;
	/** secret: characters (1..1024); randomBytes: bytes (1..1024). */
	length: number;
	alphabet: Alphabet;
	encoding: Encoding;
	min: number;
	max: number;
}

export const DEFAULT_GENERATOR: GeneratorOptions = {
	kind: "secret",
	length: 32,
	alphabet: "alnum",
	encoding: "base64",
	min: 0,
	max: 100,
};

const MAX_LENGTH = 1024;

/** Why the options cannot be used, or null. */
export function generatorProblem(o: GeneratorOptions): string | null {
	switch (o.kind) {
		case "secret":
		case "randomBytes": {
			const unit = o.kind === "secret" ? "characters" : "bytes";
			if (!Number.isInteger(o.length) || o.length < 1 || o.length > MAX_LENGTH)
				return `Use between 1 and ${MAX_LENGTH} ${unit}.`;
			return null;
		}
		case "randomInt":
			if (!Number.isSafeInteger(o.min) || !Number.isSafeInteger(o.max))
				return "Use whole numbers.";
			return o.min > o.max ? "The minimum must not exceed the maximum." : null;
		default:
			return null;
	}
}

/** The expression to insert, e.g. `${{secret(32, hex)}}`. */
export function generatorExpression(o: GeneratorOptions): string {
	switch (o.kind) {
		case "secret":
			return o.alphabet === "alnum"
				? `\${{secret(${o.length})}}`
				: `\${{secret(${o.length}, ${o.alphabet})}}`;
		case "randomBytes":
			return o.encoding === "base64"
				? `\${{randomBytes(${o.length})}}`
				: `\${{randomBytes(${o.length}, ${o.encoding})}}`;
		case "randomInt":
			return `\${{randomInt(${o.min}, ${o.max})}}`;
		case "uuid":
			return "${{uuid()}}";
	}
}

/** "32 characters: letters and digits" — the shape, never a sample value. */
export function generatorShape(o: GeneratorOptions): string {
	switch (o.kind) {
		case "secret": {
			const alphabet = ALPHABETS.find((a) => a.id === o.alphabet)?.label;
			return `${o.length} characters: ${(alphabet ?? "").toLowerCase()}`;
		}
		case "randomBytes":
			return `${o.length} random bytes, ${o.encoding}-encoded`;
		case "randomInt":
			return `a whole number from ${o.min} to ${o.max}`;
		case "uuid":
			return "a random UUID (version 4)";
	}
}

/** Generators that make the variable a secret (the server forces the flag on). */
export function generatesSecret(kind: GeneratorKind): boolean {
	return kind === "secret" || kind === "randomBytes";
}

/** Function snippets the `${{` autocomplete offers. */
export const FUNCTION_SNIPPETS: readonly {
	name: string;
	insert: string;
	description: string;
}[] = [
	{
		name: "secret(32)",
		insert: "secret(32)",
		description: "A random secret, generated once",
	},
	{
		name: "randomBytes(32)",
		insert: "randomBytes(32)",
		description: "Random bytes, base64 (or hex)",
	},
	{
		name: "uuid()",
		insert: "uuid()",
		description: "A random UUID, generated once",
	},
	{
		name: "randomInt(1, 100)",
		insert: "randomInt(1, 100)",
		description: "A random whole number, generated once",
	},
	{
		name: "urlencode(KEY)",
		insert: "urlencode(KEY)",
		description: "URL-escape another variable",
	},
	{
		name: "base64(KEY)",
		insert: "base64(KEY)",
		description: "Base64-encode another variable",
	},
];
