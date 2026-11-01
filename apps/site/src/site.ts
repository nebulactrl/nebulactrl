import type { SiteConfig } from "../scripts/lib/config.ts";

export const SITE_NAME = "NebulaCtrl";
export const DESCRIPTION =
	"NebulaCtrl is a free, self hosted deploy platform for Docker servers you own. Deploy from Git or an image, let each release prove itself before it takes traffic, and run databases and backups beside your apps. Bring your AI agent over MCP or the CLI; over MCP, production changes wait for your approval. Open source under AGPL-3.0-only.";

/** Everything a page needs to know about the build, passed down explicitly. */
export interface RenderContext {
	config: SiteConfig;
	assets: Assets;
	/** Facts counted from the repository at build time, so copy cannot drift from them. */
	facts: Facts;
}

export interface Assets {
	/** URL of the client entry (a module script). */
	clientJs: string;
	/** Stylesheet URLs linked from pages other than the landing page. */
	css: string[];
	/** Landing only: the stylesheet inlined, so the first paint needs no CSS request. */
	inlineCss: string | null;
	/** Font files to preload (sans and mono 400). */
	fonts: string[];
	dev: boolean;
}

export interface Facts {
	templates: number;
	/** Pages in the docs, API areas excluded. */
	docsPages: number;
}

export function installCommand(siteUrl: string): string {
	return `curl -fsSL ${siteUrl}/install.sh | sudo bash`;
}
