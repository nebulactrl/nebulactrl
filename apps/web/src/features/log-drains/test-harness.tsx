// Test-only: a stateful host for the draft-editing field components, so a
// test can type into them and read back the draft they produce.

import { type ReactNode, useState } from "react";
import type { SetDraft } from "./kind-fields";
import type { Draft } from "./model";
import { emptyDraft } from "./model";

export interface DraftHost {
	draft: () => Draft;
}

/**
 * Renders `children(draft, set, setDraft)` over a draft that starts as `initial`.
 * `host.draft()` always returns the draft as last set.
 */
export function DraftHarness({
	initial,
	host,
	children,
}: {
	initial?: Partial<Draft>;
	host: DraftHost;
	children: (
		draft: Draft,
		set: SetDraft,
		setDraft: (next: Draft) => void,
	) => ReactNode;
}) {
	const [draft, setDraft] = useState<Draft>({ ...emptyDraft(), ...initial });
	host.draft = () => draft;
	const set: SetDraft = (key, value) =>
		setDraft((prev) => ({ ...prev, [key]: value }));
	return <>{children(draft, set, setDraft)}</>;
}

export function newHost(): DraftHost {
	return { draft: () => emptyDraft() };
}
