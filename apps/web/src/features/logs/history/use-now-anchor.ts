import { useCallback, useState } from "react";

/**
 * The moment "now" means for a range that reaches it, in epoch milliseconds.
 * It is taken again when the range changes and when the view becomes `active`
 * (a history that was waiting behind the live view asks from a fresh now, not
 * from the moment the page opened); `reanchor` takes it again on demand
 * (Refresh, or the same range chosen again).
 */
export function useNowAnchor(
	signature: string,
	active: boolean,
): [at: number, reanchor: () => void] {
	const [anchor, setAnchor] = useState(() => ({
		signature,
		active,
		at: Date.now(),
	}));
	let current = anchor;
	if (anchor.signature !== signature || anchor.active !== active) {
		const fresh = anchor.signature !== signature || active;
		current = {
			signature,
			active,
			at: fresh ? Date.now() : anchor.at,
		};
		setAnchor(current);
	}
	const reanchor = useCallback(
		() => setAnchor((previous) => ({ ...previous, at: Date.now() })),
		[],
	);
	return [current.at, reanchor];
}
