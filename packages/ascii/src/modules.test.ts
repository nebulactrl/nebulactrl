import { expect, test } from "bun:test";

/**
 * Every source module imports without throwing, and `bun test --coverage`
 * counts every file: Bun reports only the files a test loads, so an untested
 * file would otherwise be missing from the coverage report instead of counting
 * as uncovered. `standalone.ts` is left out: it is the maintenance pages' entry
 * script and mounts the scene into the page as soon as it loads.
 */
test("every source module loads", async () => {
	const failed: string[] = [];
	for await (const file of new Bun.Glob("**/*.ts").scan({
		cwd: import.meta.dir,
	})) {
		if (
			file.endsWith(".d.ts") ||
			file.endsWith(".test.ts") ||
			file === "standalone.ts"
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
