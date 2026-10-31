/** The words around the "Block outbound internet" switch, kept together so the cue never drifts from the explanation. */
export const OUTBOUND_HELP = {
	description:
		"The service cannot reach the internet. Updates, webhooks and calls to third-party APIs from it will fail.",
	info: "The service only joins its environment's private network, which has no route to the internet. Other services in the environment still reach it by name. Package installs from it fail too.",
	on: "Blocked: no outbound internet",
	off: "Allowed: can reach the internet",
	needsDetach:
		"Detach this service from its shared networks first: a shared network would give it internet access again.",
} as const;
