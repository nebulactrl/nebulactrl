import { useCallback, useState } from "react";

/**
 * `useState` backed by localStorage, for small view preferences (the chosen
 * range, a unit toggle) that should survive a reload. Reads are validated by
 * `accept`, so a stale or hand-edited value falls back to the default.
 */
export function usePersisted<T extends string>(
	key: string,
	fallback: T,
	accept: (value: unknown) => value is T,
): [T, (next: T) => void] {
	const [value, setValue] = useState<T>(() => {
		try {
			const stored = window.localStorage.getItem(`nebulactrl:${key}`);
			return accept(stored) ? stored : fallback;
		} catch {
			return fallback;
		}
	});
	const set = useCallback(
		(next: T) => {
			setValue(next);
			try {
				window.localStorage.setItem(`nebulactrl:${key}`, next);
			} catch {
				// Private mode or a full quota: the choice lasts for this visit.
			}
		},
		[key],
	);
	return [value, set];
}
