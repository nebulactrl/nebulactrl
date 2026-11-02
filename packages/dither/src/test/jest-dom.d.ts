import type { TestingLibraryMatchers } from "@testing-library/jest-dom/matchers";

// The jest-dom matchers that src/test/setup.ts adds to bun:test's expect.
declare module "bun:test" {
	interface Matchers<T>
		extends TestingLibraryMatchers<typeof expect.stringContaining, T> {}
	interface AsymmetricMatchers extends TestingLibraryMatchers {}
}

declare global {
	/** React's switch for act() warnings; src/test/setup.ts turns it on. */
	var IS_REACT_ACT_ENVIRONMENT: boolean | undefined;
}
