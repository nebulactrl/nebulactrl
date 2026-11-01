import path from "node:path";
import type { Plugin, ViteDevServer } from "vite";
import { matchRoute } from "../../src/routes.ts";
import { loadSiteData } from "../../src/server/site-data.ts";
import type { Assets, RenderContext } from "../../src/site.ts";
import { readConfig } from "./config.ts";
import { assembleInstaller } from "./installer.ts";
import { readFacts, textOutputs } from "./outputs.ts";

/**
 * Dev: renders every route on request through Vite's SSR loader, with the same
 * `renderPage` the build uses, and serves the generated files (llms.txt, search
 * index, installer, ...) from memory. The build does not use this plugin.
 */
export function siteDev(): Plugin {
	let installer: Buffer | null = null;
	return {
		name: "nebulactrl-site-dev",
		apply: "serve",
		configureServer(server: ViteDevServer) {
			server.middlewares.use(async (req, res, next) => {
				const url = new URL(req.url ?? "/", "http://localhost");
				if (req.method !== "GET" && req.method !== "HEAD") return next();
				try {
					const config = readConfig(process.env, {
						dev: true,
						devPort: server.config.server.port ?? 5174,
					});
					const data = loadSiteData(config.githubUrl);
					const pathname = decodeURIComponent(url.pathname);

					if (pathname === "/install.sh") {
						installer ??= assembleInstaller();
						res.setHeader("Content-Type", "text/x-shellscript; charset=utf-8");
						res.end(installer);
						return;
					}
					const generated = textOutputs(data, config).find(
						(o) => `/${o.file}` === pathname,
					);
					if (generated) {
						res.setHeader("Content-Type", generated.type);
						res.end(generated.body);
						return;
					}

					const wantsHtml = (req.headers.accept ?? "").includes("text/html");
					if (!wantsHtml || path.extname(pathname) !== "") return next();

					const assets: Assets = {
						clientJs: "/src/entry-client.ts",
						css: [],
						inlineCss: null,
						fonts: [],
						dev: true,
					};
					const ctx: RenderContext = { config, assets, facts: readFacts(data) };
					const mod = (await server.ssrLoadModule(
						"/src/entry-server.tsx",
					)) as typeof import("../../src/entry-server.tsx");
					const route = matchRoute(pathname, data);
					const html = await server.transformIndexHtml(
						url.pathname,
						mod.renderPage(route, ctx, data),
					);
					res.statusCode = route.kind === "not-found" ? 404 : 200;
					res.setHeader("Content-Type", "text/html; charset=utf-8");
					res.end(html);
				} catch (error) {
					server.ssrFixStacktrace(error as Error);
					next(error);
				}
			});
		},
	};
}
