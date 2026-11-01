import fs from "node:fs";
import path from "node:path";
import type { Plugin } from "vite";
import { packSearch } from "../search.ts";
import { buildCorpus, type Corpus } from "./corpus.ts";
import { jsLiteral } from "./js-literal.ts";
import { buildLlms, type LlmsOutput, ORIGIN_PLACEHOLDER } from "./llms.ts";

// Compiles docs/**/*.md (and openapi/openapi.json) into virtual modules at
// build time. Nothing here ships: the browser gets plain data and HTML.
//
//   virtual:docs-manifest   page metadata and the sidebar tree
//   virtual:docs-loaders    slug → () => import(page module)
//   virtual:docs-search     the search index
//   virtual:docs-page/<slug> one page's compiled body (one chunk each)

const MANIFEST = "virtual:docs-manifest";
const LOADERS = "virtual:docs-loaders";
const SEARCH = "virtual:docs-search";
const PAGE_PREFIX = "virtual:docs-page/";
const RESOLVED = "\0";

export interface DocsPluginOptions {
	/** The repository root; `docs/` and `openapi/openapi.json` are read from it. */
	repoRoot: string;
	/**
	 * Where links that leave `docs/` point. `null`: the repository is not
	 * public, so those links render as plain text. Omitted: `REPO_URL`.
	 */
	repoUrl?: string | null;
}

export function docsPlugin({ repoRoot, repoUrl }: DocsPluginOptions): Plugin {
	const docsDir = path.join(repoRoot, "docs");
	const openapiPath = path.join(repoRoot, "openapi", "openapi.json");
	let corpus: Corpus | null = null;
	let llms: LlmsOutput | null = null;

	const load = (): Corpus => {
		if (corpus) return corpus;
		const openapi = fs.existsSync(openapiPath)
			? (JSON.parse(fs.readFileSync(openapiPath, "utf8")) as unknown)
			: null;
		// The Docker web stage holds docs/ and the spec but not the rest of the repo.
		const verifyRepoLinks = fs.existsSync(path.join(repoRoot, "go.mod"));
		const built = buildCorpus({
			docsDir,
			repoRoot,
			openapi,
			verifyRepoLinks,
			...(repoUrl !== undefined ? { repoUrl } : {}),
		});
		if (built.errors.length > 0) {
			throw new Error(
				`docs: ${built.errors.length} problem(s)\n${built.errors.map((e) => `  - ${e}`).join("\n")}`,
			);
		}
		corpus = built;
		llms = null;
		return built;
	};
	const outputs = (): LlmsOutput => {
		llms ??= buildLlms(load());
		return llms;
	};

	return {
		name: "nebulactrl-docs",
		enforce: "pre",
		resolveId(id) {
			if (
				id === MANIFEST ||
				id === LOADERS ||
				id === SEARCH ||
				id.startsWith(PAGE_PREFIX)
			) {
				return RESOLVED + id;
			}
			return null;
		},
		load(id) {
			if (!id.startsWith(RESOLVED)) return null;
			const name = id.slice(RESOLVED.length);
			if (
				name !== MANIFEST &&
				name !== LOADERS &&
				name !== SEARCH &&
				!name.startsWith(PAGE_PREFIX)
			) {
				return null;
			}
			// Rebuild the corpus when files change (dev) and rebuild watching in build --watch.
			this.addWatchFile(openapiPath);
			for (const file of walk(docsDir)) this.addWatchFile(file);
			const built = load();
			if (name === MANIFEST) {
				const metas = [...built.entries.values()].map((entry) => entry.meta);
				return `export const metas = ${jsLiteral(metas)};\nexport const nav = ${jsLiteral(built.nav)};\n`;
			}
			if (name === LOADERS) {
				const lines = [...built.entries.keys()].map(
					(slug) =>
						`\t${jsLiteral(slug)}: () => import(${jsLiteral(`${PAGE_PREFIX}${slug}`)}),`,
				);
				return `export const loaders = {\n${lines.join("\n")}\n};\n`;
			}
			if (name === SEARCH) {
				return `export default ${jsLiteral(packSearch(built.search))};\n`;
			}
			const slug = name.slice(PAGE_PREFIX.length);
			const entry = built.entries.get(slug);
			if (!entry) this.error(`docs: no page "${slug}"`);
			return `export default ${jsLiteral(entry.page)};\n`;
		},
		// llms.txt, llms-full.txt and docs/<slug>.md ship as plain files in dist/;
		// the control plane fills in the origin when it serves them.
		generateBundle() {
			const out = outputs();
			this.emitFile({ type: "asset", fileName: "llms.txt", source: out.index });
			this.emitFile({
				type: "asset",
				fileName: "llms-full.txt",
				source: out.full,
			});
			for (const [slug, markdown] of out.pages) {
				this.emitFile({
					type: "asset",
					fileName: `docs/${slug}.md`,
					source: markdown,
				});
			}
		},
		configureServer(server) {
			// The same files in dev, with the origin of whoever is asking.
			server.middlewares.use((req, res, next) => {
				const url = (req.url ?? "").split("?")[0] ?? "";
				let text: string | undefined;
				let type = "text/plain; charset=utf-8";
				try {
					if (url === "/llms.txt") text = outputs().index;
					else if (url === "/llms-full.txt") text = outputs().full;
					else if (url.startsWith("/docs/") && url.endsWith(".md")) {
						text = outputs().pages.get(
							url.slice("/docs/".length, -".md".length),
						);
						type = "text/markdown; charset=utf-8";
					}
				} catch (error) {
					next(error);
					return;
				}
				if (text === undefined) {
					next();
					return;
				}
				res.setHeader("Content-Type", type);
				res.end(
					text.replaceAll(
						ORIGIN_PLACEHOLDER,
						`http://${req.headers.host ?? "localhost"}`,
					),
				);
			});
			server.watcher.add([docsDir, openapiPath]);
			const onChange = (file: string) => {
				if (!file.startsWith(docsDir) && file !== openapiPath) return;
				corpus = null;
				llms = null;
				for (const mod of server.moduleGraph.idToModuleMap.values()) {
					if (mod.id?.startsWith(`${RESOLVED}virtual:docs-`)) {
						server.moduleGraph.invalidateModule(mod);
					}
				}
				server.ws.send({ type: "full-reload" });
			};
			server.watcher.on("change", onChange);
			server.watcher.on("add", onChange);
			server.watcher.on("unlink", onChange);
		},
	};
}

function walk(dir: string): string[] {
	const out: string[] = [];
	if (!fs.existsSync(dir)) return out;
	for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
		const full = path.join(dir, entry.name);
		if (entry.isDirectory()) out.push(...walk(full));
		else if (entry.name.endsWith(".md")) out.push(full);
	}
	return out;
}
