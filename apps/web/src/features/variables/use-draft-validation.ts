import { useEffect, useState } from "react";
import { validateVariable } from "#/api/gen/sdk.gen";
import type { ValidateVariableResult } from "#/api/gen/types.gen";
import { REFERENCE_OPEN } from "./reference-autocomplete";

const DEBOUNCE_MS = 400;

/** A template needs the server when it has an expression to parse or resolve. */
export function needsValidation(value: string): boolean {
	return value.includes(REFERENCE_OPEN);
}

/**
 * Dry-runs a draft value on the control plane (parse, generator arguments,
 * references) shortly after typing stops. Nothing is written, and the answer
 * never contains a generated or secret value. Returns null while the draft
 * has nothing to check or the check is in flight.
 */
export function useDraftValidation(
	serviceId: string,
	key: string,
	value: string,
	isSecret: boolean,
): ValidateVariableResult | null {
	const [result, setResult] = useState<{
		for: string;
		data: ValidateVariableResult;
	} | null>(null);
	const signature = `${key}\u0000${isSecret}\u0000${value}`;

	useEffect(() => {
		if (!needsValidation(value)) return;
		const controller = new AbortController();
		const timer = window.setTimeout(() => {
			validateVariable({
				path: { serviceId },
				body: { ...(key ? { key } : {}), value, isSecret },
				signal: controller.signal,
			})
				.then(({ data }) => setResult({ for: signature, data }))
				.catch(() => {
					// The save path reports real failures; a failed check just shows nothing.
				});
		}, DEBOUNCE_MS);
		return () => {
			window.clearTimeout(timer);
			controller.abort();
		};
	}, [serviceId, key, value, isSecret, signature]);

	return needsValidation(value) && result?.for === signature
		? result.data
		: null;
}
