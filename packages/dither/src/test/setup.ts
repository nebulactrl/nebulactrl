// Preloaded by bunfig.toml before every test file: a DOM (happy-dom) for
// component tests, the jest-dom matchers, and a clean page after each test.
import { GlobalRegistrator } from "@happy-dom/global-registrator";

GlobalRegistrator.register({ url: "http://localhost/" });

// React warns about updates outside act() unless the environment says it is a test.
globalThis.IS_REACT_ACT_ENVIRONMENT = true;

// happy-dom rejects an animation's `finished` promise when the animation is
// cancelled (Motion cancels on unmount); nobody awaits it there, so the
// rejection surfaced as an unhandled error in an unrelated test. Code that does
// await `finished` still sees the rejection.
const animate = Element.prototype.animate;
Element.prototype.animate = function (
	this: Element,
	...args: Parameters<typeof animate>
) {
	const animation = animate.apply(this, args);
	animation.finished.catch(() => {});
	return animation;
};

// Tests run with reduced motion: transitions land at once. A test of the
// full-motion branch stubs matchMedia itself.
const matchMedia = window.matchMedia.bind(window);
window.matchMedia = (query: string) => {
	const list = matchMedia(query);
	if (query.replace(/\s/g, "") === "(prefers-reduced-motion:reduce)") {
		Object.defineProperty(list, "matches", { value: true });
	}
	return list;
};

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
});
