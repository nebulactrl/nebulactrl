import { expect, mock, test } from "bun:test";

// Vite's docs plugin (packages/docs/src/build/plugin.ts) provides these modules
// at build time; outside Vite the docs pages get an empty site.
mock.module("virtual:docs-loaders", () => ({ loaders: {} }));
mock.module("virtual:docs-manifest", () => ({ metas: [], nav: [] }));

/**
 * Every source module imports without throwing: a bad top-level call or an
 * import cycle that reads a binding too early fails here instead of in the
 * browser. Loading them all also makes `bun test --coverage` count every file:
 * Bun reports only the files a test loads, so an untested file would otherwise
 * be missing from the coverage report instead of counting as uncovered.
 * `main.tsx` mounts the app into the page and is left out.
 */
test("every source module loads", async () => {
	const failed: string[] = [];
	for await (const file of new Bun.Glob("**/*.{ts,tsx}").scan({
		cwd: import.meta.dir,
	})) {
		if (
			file.endsWith(".d.ts") ||
			/\.test\.tsx?$/.test(file) ||
			file === "main.tsx"
		)
			continue;
		try {
			await import(`./${file}`);
		} catch (error) {
			failed.push(`${file}: ${error}`);
		}
	}
	expect(failed).toEqual([]);
});
