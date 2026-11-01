// Preloaded by bunfig.toml before every test file: a DOM (happy-dom) for
// component tests, the jest-dom matchers, and a clean page after each test.
import { GlobalRegistrator } from "@happy-dom/global-registrator";

GlobalRegistrator.register({ url: "http://localhost/" });

// stubApi (src/test/api.ts) replaces fetch; every test starts with the real one.
const realFetch = globalThis.fetch;

// React warns about updates outside act() unless the environment says it is a test.
globalThis.IS_REACT_ACT_ENVIRONMENT = true;

// Imported after the DOM exists: Testing Library (which jest-dom loads too)
// binds `screen` to the document that exists when it is first imported.
const { afterEach, expect } = await import("bun:test");
const { cleanup } = await import("@testing-library/react");
// `default` exists only in TypeScript's view of the CommonJS module.
const { default: _commonJsDefault, ...matchers } = await import(
	"@testing-library/jest-dom/matchers"
);

expect.extend(matchers);

afterEach(() => {
	cleanup();
	document.body.innerHTML = "";
	globalThis.fetch = realFetch;
});
