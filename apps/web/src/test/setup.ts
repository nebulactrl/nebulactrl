// Preloaded by bunfig.toml before every test file: a DOM (happy-dom) for
// component tests, the jest-dom matchers, and a clean page after each test.
import { GlobalRegistrator } from "@happy-dom/global-registrator";

// Following a link would make happy-dom fetch the page and rewrite the document
// under every later test. With main-frame navigation off the window only takes
// the new URL (the fallback), which afterEach puts back.
const BASE_URL = "http://localhost/";
GlobalRegistrator.register({
	url: BASE_URL,
	settings: { navigation: { disableMainFrameNavigation: true } },
});

// stubApi (src/test/api.ts) replaces fetch; every test starts with the real one.
const realFetch = globalThis.fetch;

// React warns about updates outside act() unless the environment says it is a test.
globalThis.IS_REACT_ACT_ENVIRONMENT = true;

// Base UI waits for exit animations before it unmounts a popup; happy-dom never
// finishes them, so a closed dialog could stay mounted (and a re-open hang).
globalThis.BASE_UI_ANIMATIONS_DISABLED = true;

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

// A failed assertion on a DOM node (`expect(document.querySelector(...)).toBeNull()`
// while the node is still there) makes Bun format the node for the message. Left
// to itself it walks the node's own properties: React's fiber and props keys,
// and through them the whole mounted tree and the document, megabytes of text
// that takes seconds, synchronously. Inside `waitFor` that happens on a poll that
// fails while the page is still catching up, and it blocks timers, microtasks
// and so the very update being waited for, so a test that takes 70 ms when the
// update lands first takes 5 s when it does not. Bun honours the inspect hook, so
// a node prints as the start of its markup instead.
const INSPECT_LIMIT = 200;
Object.defineProperty(
	Node.prototype,
	Symbol.for("nodejs.util.inspect.custom"),
	{
		configurable: true,
		value(this: Node): string {
			const markup = this instanceof Element ? this.outerHTML : this.nodeName;
			return markup.length > INSPECT_LIMIT
				? `${markup.slice(0, INSPECT_LIMIT)}…`
				: markup;
		},
	},
);

// Tests run with reduced motion, like renderWithProviders' MotionConfig: text
// morphs and transitions land at once. A test of the full-motion branch stubs
// matchMedia itself.
const matchMedia = window.matchMedia.bind(window);
window.matchMedia = (query: string) => {
	const list = matchMedia(query);
	if (query.replace(/\s/g, "") === "(prefers-reduced-motion:reduce)") {
		Object.defineProperty(list, "matches", { value: true });
	}
	return list;
};

// A safety net for stubs that leak. Tests replace layout, scrolling, animation
// and observer members on the shared prototypes and on window; a test that fails
// before its own restore (or restores in a different order) would hand the stub
// to every later file, which then measures, scrolls or animates against it. The
// baseline is snapshotted here, before any test, and put back after each test.
type Owner = object;
const PROTOTYPE_MEMBERS = [
	"scrollIntoView",
	"scrollTo",
	"scrollBy",
	"animate",
	"getAnimations",
	"getBoundingClientRect",
	"getClientRects",
	"focus",
	"blur",
	"offsetWidth",
	"offsetHeight",
	"offsetTop",
	"offsetLeft",
	"offsetParent",
	"clientWidth",
	"clientHeight",
	"clientTop",
	"clientLeft",
	"scrollWidth",
	"scrollHeight",
	"scrollTop",
	"scrollLeft",
	"click",
] as const;
const WINDOW_MEMBERS = [
	"matchMedia",
	"scrollTo",
	"ResizeObserver",
	"IntersectionObserver",
	"requestAnimationFrame",
	"cancelAnimationFrame",
	"PublicKeyCredential",
	"devicePixelRatio",
	"innerWidth",
	"innerHeight",
	"isSecureContext",
	"EventTarget",
	"location",
	"BASE_UI_ANIMATIONS_DISABLED",
	"IS_REACT_ACT_ENVIRONMENT",
	"open",
	"requestIdleCallback",
	"cancelIdleCallback",
	"setTimeout",
	"clearTimeout",
	"setInterval",
	"clearInterval",
	"getComputedStyle",
	"getSelection",
] as const;
const NAVIGATOR_MEMBERS = [
	"credentials",
	"clipboard",
	"maxTouchPoints",
] as const;

const baseline: Array<{
	owner: Owner;
	name: string;
	descriptor: PropertyDescriptor | undefined;
}> = [];
function snapshot(owner: Owner, names: readonly string[]) {
	for (const name of names) {
		baseline.push({
			owner,
			name,
			descriptor: Object.getOwnPropertyDescriptor(owner, name),
		});
	}
}
snapshot(Element.prototype, PROTOTYPE_MEMBERS);
snapshot(HTMLElement.prototype, PROTOTYPE_MEMBERS);
snapshot(HTMLAnchorElement.prototype, PROTOTYPE_MEMBERS);
snapshot(globalThis, WINDOW_MEMBERS);
snapshot(window, WINDOW_MEMBERS);
snapshot(navigator, NAVIGATOR_MEMBERS);

function restoreBaseline() {
	for (const { owner, name, descriptor } of baseline) {
		const current = Object.getOwnPropertyDescriptor(owner, name);
		if (!descriptor) {
			if (current) Reflect.deleteProperty(owner, name);
			continue;
		}
		if (
			current &&
			current.value === descriptor.value &&
			current.get === descriptor.get &&
			current.set === descriptor.set
		) {
			continue;
		}
		Object.defineProperty(owner, name, descriptor);
	}
}

// Imported after the DOM exists: Testing Library (which jest-dom loads too)
// binds `screen` to the document that exists when it is first imported.
const { afterEach, expect, jest } = await import("bun:test");
const { cleanup, configure } = await import("@testing-library/react");

// A failed query normally serialises the whole document into its message. Inside
// waitFor that happens on every poll, which on a large page can take seconds and
// gigabytes, so a failing test looks like a hang. The message keeps the query;
// `screen.debug()` prints the DOM when a test needs it.
configure({
	getElementError: (message) => {
		const error = new Error(message ?? "");
		error.name = "TestingLibraryElementError";
		return error;
	},
});
// The toast store is global: a toast left open would show up in the next test.
const { toast } = await import("#/components/ui/toast");
// `default` exists only in TypeScript's view of the CommonJS module.
const { default: _commonJsDefault, ...matchers } = await import(
	"@testing-library/jest-dom/matchers"
);

expect.extend(matchers);

// The registrator installs happy-dom's control object as a global; TypeScript's
// DOM lib knows nothing of it.
const happyDOM = (
	globalThis as unknown as { happyDOM: { setURL(url: string): void } }
).happyDOM;

// stubApi (src/test/api.ts) records requests that break the API contract; it is
// loaded here, after the DOM exists, like the other modules that touch it.
const { takeApiViolations } = await import("./api");

afterEach(() => {
	// Read before anything is torn down; cleanup below may throw, the violations
	// of a stubbed request must still fail the test that made it.
	const violations = takeApiViolations();
	try {
		// A test that fails before switching fake timers off would leave them on for
		// every later file: waitFor then spins on fake time and never returns.
		jest.useRealTimers();
		cleanup();
		toast.close();
		document.body.innerHTML = "";
		// Persisted preferences must not carry over to the next test.
		localStorage.clear();
		sessionStorage.clear();
	} finally {
		globalThis.fetch = realFetch;
		restoreBaseline();
		if (window.location.href !== BASE_URL) happyDOM.setURL(BASE_URL);
	}
	if (violations.length > 0) throw new Error(violations.join("\n"));
});
