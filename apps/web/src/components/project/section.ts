/**
 * The environment pages a project or environment switch keeps you on:
 * /p/<project>/<env>/<section>. Anything else (the canvas, a service's
 * own page) opens the canvas of the new place.
 */
const ENV_SECTIONS = [
	"deployments",
	"observability",
	"variables",
	"previews",
] as const;

export type EnvSection = (typeof ENV_SECTIONS)[number];

/** The section of a pathname, or undefined for the canvas and anything else. */
export function envSection(pathname: string): EnvSection | undefined {
	const section = pathname.split("/")[4];
	return (ENV_SECTIONS as readonly string[]).includes(section ?? "")
		? (section as EnvSection)
		: undefined;
}

/** The route to open `section` (or the canvas) of an environment. */
export function envSectionRoute(section: EnvSection | undefined) {
	switch (section) {
		case "deployments":
			return "/p/$projectId/$envSlug/deployments" as const;
		case "observability":
			return "/p/$projectId/$envSlug/observability" as const;
		case "variables":
			return "/p/$projectId/$envSlug/variables" as const;
		case "previews":
			return "/p/$projectId/$envSlug/previews" as const;
		default:
			return "/p/$projectId/$envSlug" as const;
	}
}
