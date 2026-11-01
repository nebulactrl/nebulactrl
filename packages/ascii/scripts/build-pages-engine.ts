/**
 * Bundles the ASCII Eclipse engine plus its tiny bootstrap into ONE minified
 * IIFE for the self-contained maintenance/error pages (they inline the file;
 * no network requests, no globals except `window.NebulaAscii`).
 *
 *   cd packages/ascii && bun run scripts/build-pages-engine.ts [outPath]
 *
 * `outPath` is relative to the repo root; default `deploy/pages/assets/eclipse.js`.
 * The output is committed so `go build` works without bun. Rerun this after
 * touching anything under packages/ascii/src/.
 */
import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { gzipSync } from "node:zlib";

const here = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(here, "../../..");
const entry = resolve(here, "../src/standalone.ts");
const outPath = resolve(
	repoRoot,
	process.argv[2] ?? "deploy/pages/assets/eclipse.js",
);

const result = await Bun.build({
	entrypoints: [entry],
	format: "iife",
	target: "browser",
	minify: true,
	sourcemap: "none",
});
if (!result.success || result.outputs.length !== 1) {
	for (const log of result.logs) console.error(String(log));
	throw new Error("pages engine build failed");
}
const code = (await result.outputs[0]?.text()) ?? "";
if (code.includes("</script")) {
	// The pages inline this file in a <script> element.
	throw new Error("bundle contains </script; it cannot be inlined safely");
}
mkdirSync(dirname(outPath), { recursive: true });
writeFileSync(outPath, code);
const raw = Buffer.byteLength(code);
console.log(
	`wrote ${outPath}: ${raw} bytes minified, ${gzipSync(code).length} bytes gzip`,
);
