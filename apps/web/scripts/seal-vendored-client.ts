/**
 * Runs straight after `openapi-ts` (see the `gen:api` script).
 *
 * Hey API copies its fetch runtime into `src/api/gen/{client,core}`. That
 * code is vendored third-party source, written against a looser tsconfig
 * than this app's, so it trips `exactOptionalPropertyTypes` in a dozen
 * places that are not ours to fix. Marking only those two directories
 * `@ts-nocheck` keeps the whole app — including every call site against the
 * generated SDK, types and query options — fully type-checked, instead of
 * relaxing the compiler for 100 hand-written files.
 */
import { readdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const VENDORED = ["src/api/gen/client", "src/api/gen/core"];
const MARKER = "// @ts-nocheck";

let sealed = 0;
for (const dir of VENDORED) {
	for (const entry of readdirSync(dir, { withFileTypes: true })) {
		if (!entry.isFile() || !entry.name.endsWith(".ts")) continue;
		const path = join(dir, entry.name);
		const source = readFileSync(path, "utf8");
		if (source.startsWith(MARKER)) continue;
		writeFileSync(path, `${MARKER}\n${source}`);
		sealed++;
	}
}
console.log(`sealed ${sealed} vendored client files with ${MARKER}`);
