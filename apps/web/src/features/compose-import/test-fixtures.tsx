// Test-only builders for the Compose import tests: plan payloads as the API
// returns them, and a stateful host for the components that take `choices` and
// an updater, so a test sees the choices a click produces.
import { type ReactNode, useState } from "react";
import type {
	ComposeChoices,
	ComposePlan,
	ComposePlanEnv,
	ComposePlanService,
	ComposeStackComponent,
	ComposeStackPlan,
} from "#/api/gen/types.gen";

export function service(
	over: Partial<ComposePlanService> & { key: string },
): ComposePlanService {
	return {
		binds: [],
		dependsOn: [],
		domains: [],
		env: [],
		files: [],
		include: true,
		kind: "app",
		name: over.key,
		needs: [],
		networks: [],
		notes: [],
		oneShot: false,
		port: 0,
		portHttp: false,
		ports: [],
		replicas: 1,
		slug: over.key,
		source: { type: "image", image: "nginx:1.27" },
		volumes: [],
		...over,
	};
}

export function envRow(
	over: Partial<ComposePlanEnv> & { key: string },
): ComposePlanEnv {
	return {
		origin: "environment",
		rewritten: false,
		secret: false,
		secretGuess: false,
		skipped: false,
		value: "",
		...over,
	};
}

export function plan(over: Partial<ComposePlan> = {}): ComposePlan {
	return {
		blockers: [],
		checks: [],
		installable: true,
		missing: [],
		mode: "split",
		name: "shop",
		neededFiles: [],
		networks: [],
		notes: [],
		rewrites: [],
		services: [],
		sourceKind: "paste",
		warnings: [],
		...over,
	};
}

export function component(
	over: Partial<ComposeStackComponent> & { name: string },
): ComposeStackComponent {
	return {
		action: "run",
		domains: [],
		image: "nginx:1.27",
		index: 0,
		notes: [],
		port: 0,
		ports: [],
		profiles: [],
		...over,
	};
}

export function stackPlan(
	over: Partial<ComposeStackPlan> = {},
): ComposeStackPlan {
	return {
		components: [],
		composeAvailable: true,
		name: "shop",
		notes: [],
		slug: "shop",
		unrestricted: false,
		variables: [],
		violations: [],
		...over,
	};
}

/** A plan of mode "stack" wrapping `stack`. */
export function stackModePlan(
	stack: ComposeStackPlan,
	over: Partial<ComposePlan> = {},
): ComposePlan {
	return plan({ mode: "stack", stack, ...over });
}

/**
 * Holds `choices` in state like the wizard does and hands `children` the
 * current value and the updater; `seen.choices` is the latest value.
 */
export function ChoicesHost({
	initial = {},
	seen,
	children,
}: {
	initial?: ComposeChoices;
	seen: { choices: ComposeChoices };
	children(
		choices: ComposeChoices,
		onChoices: (update: (current: ComposeChoices) => ComposeChoices) => void,
	): ReactNode;
}) {
	const [choices, setChoices] = useState<ComposeChoices>(initial);
	seen.choices = choices;
	return children(choices, (update) => setChoices(update));
}
