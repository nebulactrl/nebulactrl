// Pure helpers of the credential form. The control plane is the authority
// (internal/dns01 checkValues, pkg/agentprotocol CheckDNSVariables); these
// mirror it so the form can say what is missing before it sends anything.

import type { DnsProviderSpec } from "./queries";

/** One name/value pair of the generic form. */
export interface VariableRow {
	id: string;
	name: string;
	value: string;
}

/** The values to send: trimmed names, blank names or values dropped, the last row of a name wins. */
export function rowsToValues(
	rows: readonly VariableRow[],
): Record<string, string> {
	const out: Record<string, string> = {};
	for (const row of rows) {
		const name = row.name.trim();
		if (name === "" || row.value.trim() === "") continue;
		out[name] = row.value;
	}
	return out;
}

/** Names the provider's catalog does not list (the control plane refuses them). */
export function unknownVariables(
	spec: Pick<DnsProviderSpec, "fields">,
	names: readonly string[],
): string[] {
	const known = new Set(spec.fields.map((f) => f.env));
	return names.filter((n) => !known.has(n));
}

/**
 * What the credential still lacks, or null when it is complete: every required
 * variable, one complete authentication method where the provider has
 * several, and at least one variable for a generic provider.
 */
export function credentialProblem(
	spec: DnsProviderSpec,
	values: Readonly<Record<string, string>>,
): string | null {
	const has = (name: string) => (values[name] ?? "").trim() !== "";
	for (const f of spec.fields) {
		if (f.required && !has(f.env)) return `${f.label} (${f.env}) is required`;
	}
	const methods = spec.oneOf ?? [];
	if (methods.length > 0) {
		if (methods.some((set) => set.every(has))) return null;
		return `Set every variable of one method: ${methods
			.map((set) => set.join(" + "))
			.join(" or ")}`;
	}
	if (spec.generic && !spec.fields.some((f) => has(f.env))) {
		return "Set at least one variable; the lego page lists them";
	}
	return null;
}

/** Whether a variable of a generic provider holds a secret (write-only). */
export function isSecretVariable(
	spec: Pick<DnsProviderSpec, "fields">,
	name: string,
): boolean {
	// A name the catalog does not know is hidden: a wrongly shown value leaks.
	return spec.fields.find((f) => f.env === name)?.secret ?? true;
}
