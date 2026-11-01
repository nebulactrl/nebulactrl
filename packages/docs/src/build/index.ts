// The build-time docs pipeline, minus the Vite plugin (`@nebulactrl/docs/plugin`).
// Both apps/web (through the plugin) and apps/site (through these functions
// directly) compile docs/** and openapi/openapi.json with exactly this code.

export { packSearch } from "../search.ts";
export {
	buildCorpus,
	type Corpus,
	type CorpusEntry,
	type CorpusOptions,
	isExcluded,
} from "./corpus.ts";
export { type LinkContext, resolveLink } from "./links.ts";
export {
	buildLlms,
	type LlmsOutput,
	ORIGIN_PLACEHOLDER,
	withOrigin,
} from "./llms.ts";
