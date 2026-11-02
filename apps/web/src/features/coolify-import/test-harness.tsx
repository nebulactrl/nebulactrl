import { type ReactNode, useState } from "react";
import { EMPTY_DRAFT, type ImportDraft } from "./lib";

/**
 * Holds an ImportDraft the way the wizard does, so a step component can be
 * rendered with real state. `seen` receives every draft, so a test can read the
 * choices the reader's clicks add up to.
 */
export function DraftHarness({
	initial = EMPTY_DRAFT,
	seen,
	children,
}: {
	initial?: ImportDraft;
	seen?: { current: ImportDraft };
	children(
		draft: ImportDraft,
		onDraft: (update: (current: ImportDraft) => ImportDraft) => void,
	): ReactNode;
}) {
	const [draft, setDraft] = useState(initial);
	if (seen) seen.current = draft;
	return <>{children(draft, (update) => setDraft(update))}</>;
}
