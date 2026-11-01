import type { KeyedElement } from "./nav-plan";
import {
	classForKey,
	type NamedElement,
	type NameRegistry,
	toViewTransitionName,
} from "./vt-names";

/** A key whose source is named and whose target is still to be found. */
export interface Armed {
	key: string;
	name: string;
	className: string;
}

/**
 * Names the source elements, just before the browser captures the old page.
 * A key that cannot be a name (no `kind:` prefix) is skipped.
 */
export function armSources<E extends NamedElement>(
	registry: NameRegistry<E>,
	owner: number,
	sources: readonly KeyedElement<E>[],
): Armed[] {
	const armed: Armed[] = [];
	for (const { key, element } of sources) {
		const name = toViewTransitionName(key);
		if (name === null) continue;
		const className = classForKey(key);
		registry.assign(element, name, className, owner);
		armed.push({ key, name, className });
	}
	return armed;
}

/**
 * Hands each armed name to the element that replaced its source, once the
 * new page has rendered and before the browser captures it. `find` lists
 * the elements carrying a key. The first one that is not the source itself
 * takes the name, and a persistent source (a sidebar label) gives it up.
 * With no counterpart the source keeps its name, so it cross-fades in
 * place when it survives and simply fades out when it was unmounted.
 * Returns how many names found a target.
 */
export function pairTargets<E extends NamedElement>(
	registry: NameRegistry<E>,
	owner: number,
	armed: readonly Armed[],
	find: (key: string) => readonly E[],
): number {
	let paired = 0;
	for (const { key, name, className } of armed) {
		const source = registry.holder(name);
		const target = find(key).find((candidate) => candidate !== source);
		if (target === undefined) continue;
		registry.assign(target, name, className, owner);
		paired++;
	}
	return paired;
}
