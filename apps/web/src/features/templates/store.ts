import { useSyncExternalStore } from "react";
import type { TemplateInstance } from "#/api/gen/types.gen";
import type { LicenseFilter } from "./lib";

// A module-level store, like the create flow's: the Create menu, the ⌘K
// palette, the empty canvas and the `?template=` deep link all open the same
// dialog, which is mounted once by the canvas screen. The stack-delete
// confirmation lives here too, so it outlives the group frame that asked
// for it (the frame disappears as soon as its services are gone).
interface State {
	open: boolean;
	/** The template whose detail step is showing; null is the gallery. */
	templateId: string | null;
	/** The gallery's filters outlive a visit to a detail step. */
	query: string;
	category: string | null;
	license: LicenseFilter | null;
	/** The stack a delete confirmation is open for. */
	deleting: TemplateInstance | null;
	/** The stack whose update diff is open. */
	updating: TemplateInstance | null;
}

let state: State = {
	open: false,
	templateId: null,
	query: "",
	category: null,
	license: null,
	deleting: null,
	updating: null,
};
const listeners = new Set<() => void>();

function set(next: Partial<State>) {
	state = { ...state, ...next };
	for (const listener of listeners) listener();
}

export function useTemplatesState(): State {
	return useSyncExternalStore(
		(listener) => {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
		() => state,
		() => state,
	);
}

export const templatesActions = {
	/** The gallery, or with an id straight to that template's detail step. */
	open(templateId?: string) {
		set({
			open: true,
			templateId: templateId ?? null,
			query: "",
			category: null,
			license: null,
		});
	},
	close() {
		set({ open: false });
	},
	/** Detail step of a template, or null back to the gallery. */
	select(templateId: string | null) {
		set({ templateId });
	},
	setQuery(query: string) {
		set({ query });
	},
	setCategory(category: string | null) {
		set({ category });
	},
	setLicense(license: LicenseFilter | null) {
		set({ license });
	},
	requestUpdate(instance: TemplateInstance) {
		set({ updating: instance });
	},
	cancelUpdate() {
		set({ updating: null });
	},
	requestDelete(instance: TemplateInstance) {
		set({ deleting: instance });
	},
	cancelDelete() {
		set({ deleting: null });
	},
};
