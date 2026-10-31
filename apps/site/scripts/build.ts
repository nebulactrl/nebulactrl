/**
 * Static build: `SITE_URL=https://nebulactrl.example bun run build` writes
 * apps/site/dist. One Vite build produces the client chunks (islands) and the
 * stylesheet; then every route is rendered to real HTML with react-dom/server
 * and written next to the generated files (llms.txt, installer, sitemap, ...).
 */
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { gzipSync } from "node:zlib";
import { build } from "vite";
import { renderPage } from "../src/entry-server.tsx";
import { allRoutes, type Route, routePath } from "../src/routes.ts";
import { loadSiteData } from "../src/server/site-data.ts";
import type { Assets, RenderContext } from "../src/site.ts";
import { ConfigError, readConfig } from "./lib/config.ts";
import { assembleInstaller } from "./lib/installer.ts";
import { type Output, readFacts, textOutputs } from "./lib/outputs.ts";

const root = path.resolve(fileURLToPath(new URL(".", import.meta.url)), "..");
const dist = path.join(root, "dist");

function write(file: string, body: string | Uint8Array) {
	const target = path.join(dist, file);
	fs.mkdirSync(path.dirname(target), { recursive: true });
	fs.writeFileSync(target, body);
}

/** The newest `v*` tag, or null while the repository has none. */
function latestTag(): string | null {
	const result = Bun.spawnSync(
		["git", "describe", "--tags", "--abbrev=0", "--match", "v*"],
		{ cwd: root, stdout: "pipe", stderr: "ignore" },
	);
	const tag = result.exitCode === 0 ? result.stdout.toString().trim() : "";
	return tag || null;
}

function htmlFile(route: Route): string {
	const p = routePath(route);
	return p === "/" ? "index.html" : `${p.slice(1)}/index.html`;
}

async function main() {
	const started = performance.now();
	const config = readConfig(process.env, { dev: false });
	// The version chip reads the latest release tag when SITE_VERSION is not given.
	if (!config.version) config.version = latestTag();

	// Fail on a broken docs link before spending time on a bundle.
	const data = loadSiteData(config.githubUrl);
	const installer = assembleInstaller();

	fs.rmSync(dist, { recursive: true, force: true });
	await build({
		root,
		logLevel: "warn",
		build: {
			outDir: "dist",
			emptyOutDir: true,
			manifest: true,
			cssCodeSplit: false,
			assetsInlineLimit: 0,
			rollupOptions: { input: path.join(root, "src/entry-client.ts") },
		},
	});

	const manifest = JSON.parse(
		fs.readFileSync(path.join(dist, ".vite/manifest.json"), "utf8"),
	) as Record<string, { file: string; css?: string[] }>;
	const entry = manifest["src/entry-client.ts"];
	if (!entry) throw new Error("client entry missing from the Vite manifest");
	// cssCodeSplit is off, so Vite lists the one stylesheet as its own manifest entry.
	const sheet = manifest["style.css"];
	const css = [...(entry.css ?? []), ...(sheet ? [sheet.file] : [])].map(
		(file) => `/${file}`,
	);
	if (css.length === 0) throw new Error("no stylesheet in the client build");
	const inlineCss = css
		.map((href) => fs.readFileSync(path.join(dist, href.slice(1)), "utf8"))
		.join("\n");
	const fonts = Object.entries(manifest)
		.filter(([key]) =>
			/ibm-plex-sans-latin-wght-normal\.woff2$|ibm-plex-mono-latin-400-normal\.woff2$/.test(
				key,
			),
		)
		.map(([, value]) => `/${value.file}`);
	fs.rmSync(path.join(dist, ".vite"), { recursive: true, force: true });

	const assets: Assets = {
		clientJs: `/${entry.file}`,
		css,
		inlineCss,
		fonts,
		dev: false,
	};
	const ctx: RenderContext = { config, assets, facts: readFacts(data) };

	const routes = allRoutes(data);
	for (const route of routes)
		write(htmlFile(route), renderPage(route, ctx, data));
	write("404.html", renderPage({ kind: "not-found" }, ctx, data));

	const outputs: Output[] = [
		...textOutputs(data, config),
		{ file: "install.sh", type: "text/x-shellscript", body: installer },
	];
	for (const output of outputs) write(output.file, output.body);

	report(routes.length, started);
}

function report(pages: number, started: number) {
	const sizes: Record<string, number> = {};
	const walk = (dir: string) => {
		for (const name of fs.readdirSync(dir, { withFileTypes: true })) {
			const full = path.join(dir, name.name);
			if (name.isDirectory()) walk(full);
			else sizes[path.relative(dist, full)] = fs.statSync(full).size;
		}
	};
	walk(dist);
	const gz = (file: string) =>
		gzipSync(fs.readFileSync(path.join(dist, file))).length;
	const kb = (n: number) => `${(n / 1024).toFixed(1)} KB`;
	const landing = fs.readFileSync(path.join(dist, "index.html"));
	const js = Object.keys(sizes).filter(
		(f) => f.startsWith("assets/") && f.endsWith(".js"),
	);
	console.log(
		`site: ${pages} pages + 404 in ${((performance.now() - started) / 1000).toFixed(1)} s -> ${path.relative(process.cwd(), dist) || "dist"}`,
	);
	console.log(
		`  index.html          ${kb(landing.length)} raw, ${kb(gzipSync(landing).length)} gzip (CSS inlined)`,
	);
	for (const file of js.sort())
		console.log(
			`  ${file.padEnd(36)} ${kb(sizes[file] as number)} raw, ${kb(gz(file))} gzip`,
		);
}

main().catch((error) => {
	if (error instanceof ConfigError) {
		console.error(`\nsite build: ${error.message}\n`);
	} else {
		console.error(error);
	}
	process.exit(1);
});
