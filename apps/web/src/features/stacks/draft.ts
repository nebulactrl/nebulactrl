import {
	buildResources,
	resourceErrors,
	type StackResourcesConfig,
	type StackResourcesDraft,
	toResourcesDraft,
	waitSecondsError,
} from "./model";

// The stack part of the settings form's working copy. The service settings
// draft holds these three fields next to its own; this file knows how to fill,
// check and send them.

export interface StackFields {
	stackCompose: string;
	/** Seconds `up --wait` may take, as typed; empty keeps the default. */
	stackWaitSeconds: string;
	stackResources: StackResourcesDraft;
}

export interface StackConfigRead {
	compose?: string | undefined;
	waitSeconds?: number | undefined;
	resources?: StackResourcesConfig | undefined;
}

/** The form's fields from a service's `config.stack` (blank for any other service). */
export function toStackFields(
	stack: StackConfigRead | null | undefined,
): StackFields {
	return {
		stackCompose: stack?.compose ?? "",
		stackWaitSeconds: stack?.waitSeconds ? String(stack.waitSeconds) : "",
		stackResources: toResourcesDraft(stack?.resources),
	};
}

export interface StackFieldErrors {
	stackCompose?: string;
	stackWaitSeconds?: string;
	stackResources?: string;
}

/**
 * What is wrong with the stack fields. The compose text itself is judged by
 * the live check (violations), which the form adds on top; here only what
 * needs no server: an empty file, the wait timeout and the limits.
 */
export function validateStackFields(fields: StackFields): StackFieldErrors {
	const errors: StackFieldErrors = {};
	if (fields.stackCompose.trim() === "") {
		errors.stackCompose = "A Compose file is required.";
	}
	const wait = waitSecondsError(fields.stackWaitSeconds);
	if (wait) errors.stackWaitSeconds = wait;
	if (Object.keys(resourceErrors(fields.stackResources)).length > 0) {
		errors.stackResources = "Fix the highlighted limits.";
	}
	return errors;
}

/** `config.stack` as the API takes it: the components are derived by the server, never sent. */
export function buildStackInput(fields: StackFields): {
	compose: string;
	resources?: StackResourcesConfig;
	waitSeconds?: number;
} {
	const resources = buildResources(fields.stackResources);
	const wait = fields.stackWaitSeconds.trim();
	return {
		compose: fields.stackCompose,
		...(Object.keys(resources).length > 0 ? { resources } : {}),
		...(wait === "" ? {} : { waitSeconds: Number.parseInt(wait, 10) }),
	};
}
