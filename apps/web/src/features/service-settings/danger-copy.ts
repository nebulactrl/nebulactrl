/**
 * The words of the Danger zone. A destructive confirm names production when
 * the service lives there, so nobody deletes it thinking it is a preview.
 */

interface DeleteCopyInput {
	serviceName: string;
	isRoute: boolean;
	/** The environment the service is in; undefined while the project has not loaded. */
	environment: { name: string; isProduction: boolean } | undefined;
}

export function deleteDescription({
	serviceName,
	isRoute,
	environment,
}: DeleteCopyInput): string {
	const effect = isRoute
		? `Deleting ${serviceName} removes its route from the proxy on the next deploy, frees its port and deletes the service with its deployment history. The service it forwards to is not touched. This cannot be undone.`
		: `Deleting ${serviceName} stops the container, removes its routes and deletes the service with its variables and deployment history. This cannot be undone.`;
	return environment?.isProduction
		? `${serviceName} is in production (${environment.name}). ${effect}`
		: effect;
}

/** The hold button's label: "Hold to delete from production" in production. */
export function holdLabel({
	isRoute,
	environment,
}: Pick<DeleteCopyInput, "isRoute" | "environment">): string {
	const what = isRoute ? "route" : "service";
	return environment?.isProduction
		? `Hold to delete ${what} from production`
		: `Hold to delete ${what}`;
}
