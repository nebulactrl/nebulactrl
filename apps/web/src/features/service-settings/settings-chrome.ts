import {
	type Category,
	categoryHasDraftFields,
	type SectionId,
} from "./settings-nav-model";

/**
 * The frame around the Settings sections: when the shared save bar shows, and
 * which parts of a section's head repeat the category's own. Pure: the tab and
 * `Section` only draw what this decides.
 */

/**
 * Sections that sit on a page with draft fields but act at once through their
 * own Apply (Scaling, on the Resources page above the limits). A page that
 * holds one never offers the bar at rest, so it does not show a second save
 * model beside the section's own button.
 */
const OWN_APPLY_SECTIONS: readonly SectionId[] = ["scaling"];

/**
 * Whether the save bar is drawn. Unsaved edits and a failed save always show
 * it, whichever category is open, since Discard and Save cover every
 * category. At rest it shows only on a page whose every control saves through
 * it: Operations and the Danger zone act at once and have nothing to save, and
 * Resources opens on Scaling's own Apply.
 */
export function saveBarVisible(input: {
	category: Pick<Category, "id" | "sections">;
	dirty: boolean;
	saveFailed: boolean;
}): boolean {
	const { category, dirty, saveFailed } = input;
	if (dirty || saveFailed) return true;
	if (!categoryHasDraftFields(category.id)) return false;
	return !category.sections.some((section) =>
		OWN_APPLY_SECTIONS.includes(section.id),
	);
}

export interface SectionHead {
	/** The title repeats the category's heading just above it. */
	showTitle: boolean;
	/** The description repeats the category's line under its heading. */
	showDescription: boolean;
}

const same = (a: string, b: string): boolean =>
	a.trim().toLowerCase() === b.trim().toLowerCase();

/**
 * Which parts of a section's head say something the category's heading has
 * not: "Networking" under "Networking" and "Container hardening and runtime
 * options." under the same sentence are dropped. The description is compared
 * only when it is plain text.
 */
export function sectionHead(
	category: Pick<Category, "label" | "description"> | null,
	section: { title: string; description?: unknown },
): SectionHead {
	if (category === null) return { showTitle: true, showDescription: true };
	const description = section.description;
	return {
		showTitle: !same(section.title, category.label),
		showDescription: !(
			typeof description === "string" && same(description, category.description)
		),
	};
}

/** Where environment variables and secrets live, since no Settings field holds them. */
export const VARIABLES_HINT =
	"Environment variables and secrets are in the Variables tab, not here.";

const VARIABLE_WORDS: readonly string[] = [
	"env",
	"environment",
	"variable",
	"variables",
	"secret",
	"secrets",
];

/**
 * The pointer to give under a settings search that asks for environment
 * variables or secrets, which are edited on the Variables tab. Null for any
 * other query.
 */
export function variablesHint(query: string): string | null {
	const asked = query
		.toLowerCase()
		.split(/[^a-z0-9]+/)
		.some((word) => VARIABLE_WORDS.includes(word));
	return asked ? VARIABLES_HINT : null;
}

/**
 * Whether the token steps under "Registry login" start open. They are for a
 * private image: a public one pulls without a login, so the steps stay behind
 * a summary until credentials are stored or being typed.
 */
export function registryHelpOpen(input: {
	authSet: boolean;
	username: string;
	password: string;
}): boolean {
	return input.authSet || input.username.trim() !== "" || input.password !== "";
}
