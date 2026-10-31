import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import {
	buildCorpus,
	buildLlms,
	type Corpus,
	type LlmsOutput,
} from "@nebulactrl/docs/build";
import { packSearch } from "@nebulactrl/docs/search";
import type { SearchData } from "@nebulactrl/docs/types";

/** apps/site/src/server → the repository root. */
export const repoRoot = path.resolve(
	fileURLToPath(new URL(".", import.meta.url)),
	"../../../..",
);

export interface SiteData {
	corpus: Corpus;
	/** llms.txt, llms-full.txt and the raw pages, still carrying the placeholder origin. */
	llms: LlmsOutput;
	search: SearchData;
	openapi: string | null;
	/** The nebula.toml JSON Schema (the control plane embeds the same file), still carrying the placeholder origin. */
	schema: string | null;
	/** The AGPL text from the repository's LICENSE file, when it is there. */
	licenceText: string | null;
}

const cache = new Map<string, SiteData>();

/**
 * Compiles docs/** and openapi/openapi.json with the pipeline the in-app docs
 * use (packages/docs). Any broken link or anchor throws, which fails the build.
 */
export function loadSiteData(githubUrl: string | null): SiteData {
	const key = githubUrl ?? "";
	const hit = cache.get(key);
	if (hit) return hit;
	const schemaPath = path.join(
		repoRoot,
		"internal",
		"projectconfig",
		"schema",
		"nebula.toml.json",
	);
	const openapiPath = path.join(repoRoot, "openapi", "openapi.json");
	const openapiRaw = fs.existsSync(openapiPath)
		? fs.readFileSync(openapiPath, "utf8")
		: null;
	const corpus = buildCorpus({
		docsDir: path.join(repoRoot, "docs"),
		repoRoot,
		openapi: openapiRaw ? (JSON.parse(openapiRaw) as unknown) : null,
		repoUrl: githubUrl,
	});
	if (corpus.errors.length > 0) {
		throw new Error(
			`docs: ${corpus.errors.length} problem(s)\n${corpus.errors.map((e) => `  - ${e}`).join("\n")}`,
		);
	}
	const licencePath = path.join(repoRoot, "LICENSE");
	const data: SiteData = {
		corpus,
		llms: buildLlms(corpus),
		search: packSearch(corpus.search),
		openapi: openapiRaw,
		schema: fs.existsSync(schemaPath)
			? fs.readFileSync(schemaPath, "utf8")
			: null,
		licenceText: fs.existsSync(licencePath)
			? fs.readFileSync(licencePath, "utf8")
			: null,
	};
	cache.set(key, data);
	return data;
}
