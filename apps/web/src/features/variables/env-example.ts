import type { SuggestedVariable } from "#/api/gen/types.gen";
import type { VariableWrite } from "./use-variable-mutations";

/** What the user decided for one `.env.example` key. */
interface ExampleChoice {
	/** Stage this key. Every key starts checked. */
	checked: boolean;
	/** The value to stage; starts as the proposed one (empty when one is needed). */
	value: string;
}

export type ExampleChoices = Readonly<Record<string, ExampleChoice>>;

/** The suggestions that come from a repository's example files. */
export function exampleItems(
	items: readonly SuggestedVariable[] | null | undefined,
): SuggestedVariable[] {
	return (items ?? []).filter((item) => item.source === "env-example");
}

/** Keys that need the user's own value come first, so they are not missed. */
export function orderForReview(
	items: readonly SuggestedVariable[],
): SuggestedVariable[] {
	const rank = (item: SuggestedVariable) => (item.needsInput ? 0 : 1);
	return [...items].sort((a, b) => rank(a) - rank(b));
}

/** Everything checked, with the proposed values. */
export function initialChoices(
	items: readonly SuggestedVariable[],
): ExampleChoices {
	return Object.fromEntries(
		items.map((item) => [item.key, { checked: true, value: item.value }]),
	);
}

/**
 * Choices for a list that changed under the user (a re-read): what they
 * already decided stays, new keys start checked.
 */
export function mergeChoices(
	items: readonly SuggestedVariable[],
	previous: ExampleChoices,
): ExampleChoices {
	const fresh = initialChoices(items);
	return Object.fromEntries(
		items.map((item) => [item.key, previous[item.key] ?? fresh[item.key]]),
	) as ExampleChoices;
}

export function isChecked(choices: ExampleChoices, key: string): boolean {
	return choices[key]?.checked ?? false;
}

/** Checked keys that still have no value: they are staged empty, and the deploy plan warns. */
export function emptyCount(
	items: readonly SuggestedVariable[],
	choices: ExampleChoices,
): number {
	return items.filter(
		(item) =>
			item.needsInput &&
			isChecked(choices, item.key) &&
			(choices[item.key]?.value ?? "").trim() === "",
	).length;
}

export function checkedCount(
	items: readonly SuggestedVariable[],
	choices: ExampleChoices,
): number {
	return items.filter((item) => isChecked(choices, item.key)).length;
}

/**
 * The staged writes for the checked keys. `source: "suggested"` marks them as
 * coming from the example, so a value left empty is reported by the deploy
 * plan. A secret-looking key is written as a secret.
 */
export function toWrites(
	items: readonly SuggestedVariable[],
	choices: ExampleChoices,
): VariableWrite[] {
	return items
		.filter((item) => isChecked(choices, item.key))
		.map((item) => {
			const write: VariableWrite = {
				key: item.key,
				value: choices[item.key]?.value ?? item.value,
				source: "suggested",
			};
			if (item.proposedSecret) write.isSecret = true;
			return write;
		});
}

const GENERATOR = /^\$\{\{\s*(secret|randomBytes|uuid|randomInt)\(/;
const REFERENCE = /^\$\{\{\s*([\w-]+)\.(\w+)\s*\}\}$/;

/** How a proposed value reads in the list. */
export type ValueDescription =
	| { kind: "generated" }
	| { kind: "reference"; target: string }
	| { kind: "literal"; text: string };

/**
 * How a proposed value reads in the list: a generator says a value will be
 * made, a reference names what it points at, anything else is shown as is.
 */
export function describeValue(value: string): ValueDescription {
	if (GENERATOR.test(value)) return { kind: "generated" };
	const ref = REFERENCE.exec(value);
	if (ref) return { kind: "reference", target: `${ref[1]}.${ref[2]}` };
	return { kind: "literal", text: value };
}

/** "3 keys from .env.example aren't set" */
export function bannerText(count: number, files: readonly string[]): string {
	const names = files.length > 0 ? files : [".env.example"];
	const from = names.length === 1 ? names[0] : ".env.example";
	return `${count} ${count === 1 ? "key" : "keys"} from ${from} ${count === 1 ? "isn't" : "aren't"} set`;
}
