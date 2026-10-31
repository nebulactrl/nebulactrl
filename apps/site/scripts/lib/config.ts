/**
 * Build constants, read from the environment once.
 *
 *   SITE_URL    required for `build` (https://nebulactrl.example, no path);
 *               `dev` defaults to http://localhost:<port>.
 *   GITHUB_URL  optional. The release workflow sets it to the repository
 *               (https://github.com/nebulactrl/nebulactrl). Unset hides every
 *               GitHub link (nav, footer, "Edit this page", links from the docs
 *               into the repository) instead of shipping dead ones.
 *   SITE_VERSION  optional release version for the version chip (v0.1.0). The
 *               build falls back to the latest `v*` git tag; with neither the
 *               chip is not drawn.
 *   GITHUB_STARS  optional star count for the nav, read at build time (the site
 *               never asks another origin at run time). Only shown with GITHUB_URL.
 */
export interface SiteConfig {
	/** Origin with no trailing slash. */
	siteUrl: string;
	/** Repository URL with no trailing slash, or null when GITHUB_URL is unset. */
	githubUrl: string | null;
	/** Release version such as "v0.1.0", or null while there is none. */
	version: string | null;
	/** Stars of the repository, or null when unknown or GITHUB_URL is unset. */
	githubStars: number | null;
	dev: boolean;
}

export class ConfigError extends Error {}

function origin(raw: string, name: string): string {
	let url: URL;
	try {
		url = new URL(raw);
	} catch {
		throw new ConfigError(
			`${name} is not a URL: "${raw}". Example: https://nebulactrl.example`,
		);
	}
	if (url.protocol !== "https:" && url.protocol !== "http:") {
		throw new ConfigError(
			`${name} must start with https:// (or http:// for local previews): "${raw}"`,
		);
	}
	return `${url.origin}${url.pathname.replace(/\/+$/, "")}`;
}

export function readConfig(
	env: Record<string, string | undefined>,
	opts: { dev: boolean; devPort?: number },
): SiteConfig {
	const siteRaw = env.SITE_URL?.trim();
	if (!siteRaw && !opts.dev) {
		throw new ConfigError(
			[
				"SITE_URL is required for a production build.",
				"It is the public origin of the site and ends up in canonical links, the sitemap,",
				"llms.txt and the install command. Example:",
				"  SITE_URL=https://nebulactrl.example bun run build",
			].join("\n"),
		);
	}
	const siteUrl = siteRaw
		? origin(siteRaw, "SITE_URL")
		: `http://localhost:${opts.devPort ?? 5174}`;
	const githubRaw = env.GITHUB_URL?.trim();
	const versionRaw = env.SITE_VERSION?.trim();
	const starsRaw = Number(env.GITHUB_STARS?.trim());
	const githubUrl = githubRaw ? origin(githubRaw, "GITHUB_URL") : null;
	return {
		siteUrl,
		githubUrl,
		version: versionRaw ? versionRaw : null,
		githubStars:
			githubUrl && Number.isInteger(starsRaw) && starsRaw > 0 ? starsRaw : null,
		dev: opts.dev,
	};
}
