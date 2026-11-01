import { useEffect, useState } from "react";

/** `value` once it has stopped changing for `delayMs`. */
export function useDebounced<T>(value: T, delayMs: number): T {
	const [settled, setSettled] = useState(value);
	useEffect(() => {
		const timer = window.setTimeout(() => setSettled(value), delayMs);
		return () => window.clearTimeout(timer);
	}, [value, delayMs]);
	return settled;
}
