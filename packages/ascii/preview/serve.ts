/**
 * Builds and serves the ASCII scene gallery.
 *
 *   cd packages/ascii && bun run preview        (http://localhost:4180)
 *
 * The font is the site's IBM Plex Mono 400, read from apps/site's install.
 */
import { resolve } from "node:path";

const here = import.meta.dir;
const port = Number(process.env.PORT ?? 4180);
const font = resolve(
	here,
	"../../../apps/site/node_modules/@fontsource/ibm-plex-mono/files/ibm-plex-mono-latin-400-normal.woff2",
);

/** Rebuilt on every request, so an edit shows on reload. */
async function bundle(): Promise<Blob> {
	const built = await Bun.build({
		entrypoints: [resolve(here, "main.ts")],
		target: "browser",
		format: "esm",
	});
	if (!built.success || built.outputs.length !== 1) {
		for (const log of built.logs) console.error(String(log));
		throw new Error("preview build failed");
	}
	return built.outputs[0] as Blob;
}

Bun.serve({
	port,
	async fetch(req) {
		const { pathname } = new URL(req.url);
		if (pathname === "/main.js") {
			return new Response(await bundle(), {
				headers: { "content-type": "text/javascript" },
			});
		}
		if (pathname === "/plex-mono-400.woff2") {
			return new Response(Bun.file(font), {
				headers: { "content-type": "font/woff2" },
			});
		}
		return new Response(Bun.file(resolve(here, "index.html")));
	},
});
console.log(`ascii preview: http://localhost:${port}`);
