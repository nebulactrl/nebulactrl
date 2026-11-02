import { expect, test } from "bun:test";

/**
 * Every source module imports without throwing, and `bun test --coverage`
 * counts every file: Bun reports only the files a test loads, so an untested
 * file would otherwise be missing from the coverage report instead of counting
 * as uncovered. The test helpers in `test/` are left out.
 */
test("every source module loads", async () => {
	const failed: string[] = [];
	for await (const file of new Bun.Glob("**/*.{ts,tsx}").scan({
		cwd: import.meta.dir,
	})) {
		if (
			file.endsWith(".d.ts") ||
			/\.test\.tsx?$/.test(file) ||
			file.startsWith("test/")
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
