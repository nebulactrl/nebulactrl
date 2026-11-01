// Which parts of a domain have staged changes, read from the service's
// pending-change list (`domains.<host><path>[.<field>]`, options as JSON).

import type { FieldChange, RouteOptions } from "#/api/gen/types.gen";
import { OPTION_GROUPS, type OptionGroup, pickGroup } from "./options";

type StagedBucket = "rule" | OptionGroup;

export interface DomainStaged {
	/** The domain itself is new (or newly attached) and not deployed yet. */
	added: boolean;
	buckets: ReadonlySet<StagedBucket>;
}

const RULE_FIELDS: Record<string, true> = {
	stripPrefix: true,
	addPrefix: true,
	kind: true,
	redirectTo: true,
	redirectCode: true,
	preservePath: true,
};

function parseOptions(text: string): RouteOptions {
	if (text.trim() === "") return {};
	try {
		const parsed: unknown = JSON.parse(text);
		return typeof parsed === "object" && parsed !== null
			? (parsed as RouteOptions)
			: {};
	} catch {
		return {};
	}
}

export function domainStaged(
	changes: readonly FieldChange[],
	hostname: string,
	pathPrefix: string,
): DomainStaged {
	const key = `${hostname}${pathPrefix}`;
	const own = `domains.${key}`;
	const buckets = new Set<StagedBucket>();
	let added = false;
	for (const change of changes) {
		if (change.field === own) {
			added = change.to === "added";
			continue;
		}
		if (!change.field.startsWith(`${own}.`)) continue;
		const name = change.field.slice(own.length + 1);
		if (RULE_FIELDS[name] === true) buckets.add("rule");
		else if (
			name === "tlsMode" ||
			name === "certificateId" ||
			name === "dnsResolver"
		)
			buckets.add("tls");
		else if (name === "authUsers") buckets.add("access");
		else if (name === "options") {
			const from = parseOptions(change.from);
			const to = parseOptions(change.to);
			for (const group of OPTION_GROUPS) {
				if (
					JSON.stringify(pickGroup(from, group)) !==
					JSON.stringify(pickGroup(to, group))
				) {
					buckets.add(group);
				}
			}
		}
	}
	return { added, buckets };
}
