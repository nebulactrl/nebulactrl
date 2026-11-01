/** What a rollback confirm says, so two releases of one service are told apart. */
export interface RollbackPrompt {
	title: string;
	body: string;
	/** The confirm button is the production key (cover, then press). */
	guarded: boolean;
}

export interface RollbackPromptInput {
	serviceName: string;
	/** The environment's name; absent while the project's environments load. */
	environmentName: string | undefined;
	production: boolean;
	/** `releaseLabel` of the target: a short commit or `sha256:c4717a8d`. */
	label: string;
	/** `relativeTime` of when the target was deployed: "2d ago"; "" if unknown. */
	when: string;
	/** The note given with that deployment, if any. */
	message: string | null;
	disclaimer: string;
}

/**
 * "Roll back api in production to the release from 2d ago?" names the
 * environment and the moment: every row of a service on one image tag shows
 * the same digest, so the label alone cannot say which release is meant.
 * Production confirms behind the same guard as Deploy.
 */
export function rollbackPrompt(input: RollbackPromptInput): RollbackPrompt {
	const where =
		input.environmentName === undefined
			? "this environment"
			: input.production
				? "production"
				: input.environmentName;
	const when = input.when === "just now" ? "a moment ago" : input.when;
	const note = input.message?.trim();
	const release = [
		input.label,
		...(note ? [`“${note}”`] : []),
		...(when ? [`deployed ${when}`] : []),
	].join(" · ");
	return {
		title: `Roll back ${input.serviceName} in ${where} to ${when ? `the release from ${when}` : input.label}?`,
		body: `${release}. ${input.disclaimer}`,
		guarded: input.production,
	};
}
