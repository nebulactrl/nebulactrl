import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { takeInteraction } from "./interaction";
import {
	installRouteTransitions,
	type TransitionRouter,
} from "./route-transitions";

// installRouteTransitions installs once per process, so every test drives the
// one router installed here and moves its location around.
const router: TransitionRouter = {
	startViewTransition: () => {
		throw new Error("the router's own method must have been replaced");
	},
	latestLocation: { pathname: "/" },
	state: { resolvedLocation: undefined },
};
const routerOwn = router.startViewTransition;
installRouteTransitions(router);
installRouteTransitions(router);

const realNow = performance.now.bind(performance);
const realMatchMedia = window.matchMedia;
let clock = 10_000;

interface Started {
	skipped: boolean;
}

let started: Started[];
let startError: Error | undefined;
let finishLast: () => void;

const nameOf = (element: Element) =>
	(element as HTMLElement).style.getPropertyValue("view-transition-name");
const classOf = (element: Element) =>
	(element as HTMLElement).style.getPropertyValue("view-transition-class");

function fullMotion() {
	window.matchMedia = ((query: string) => ({
		matches: false,
		media: query,
		addEventListener() {},
		removeEventListener() {},
	})) as unknown as typeof window.matchMedia;
}

/** A browser whose view transitions run `update` and finish when the test says so. */
function stubViewTransitions() {
	(document as { startViewTransition?: unknown }).startViewTransition = (
		update: () => Promise<void>,
	) => {
		if (startError) throw startError;
		const record: Started = { skipped: false };
		started.push(record);
		const finished = Promise.withResolvers<void>();
		finishLast = () => finished.resolve();
		return {
			ready: Promise.resolve(),
			updateCallbackDone: Promise.resolve(update()),
			finished: finished.promise,
			skipTransition: () => {
				record.skipped = true;
				finished.resolve();
			},
		};
	};
}

function navigateTo(pathname: string, from: string) {
	router.state.resolvedLocation = { pathname: from };
	router.latestLocation.pathname = pathname;
}

function page(...children: Node[]): HTMLElement {
	const main = document.createElement("main");
	main.id = "main-content";
	main.append(...children);
	document.body.append(main);
	return main;
}

function card(key: string): HTMLElement {
	const node = document.createElement("div");
	node.setAttribute("data-vt", key);
	return node;
}

function press(target: Element) {
	target.dispatchEvent(
		new MouseEvent("pointerdown", { bubbles: true, button: 0 }),
	);
}

beforeEach(() => {
	started = [];
	startError = undefined;
	finishLast = () => {};
	// Each test starts well after the previous transition, and a press made in it is fresh.
	clock += 5000;
	performance.now = () => clock;
	takeInteraction();
	stubViewTransitions();
	router.state.resolvedLocation = undefined;
	router.latestLocation.pathname = "/";
});

afterEach(() => {
	performance.now = realNow;
	window.matchMedia = realMatchMedia;
	(document as { startViewTransition?: unknown }).startViewTransition =
		undefined;
	document.body.innerHTML = "";
	delete document.documentElement.dataset.vtVariant;
	delete document.documentElement.dataset.vtScope;
});

describe("installRouteTransitions", () => {
	test("replaces the router's own view-transition method, once", () => {
		expect(router.startViewTransition).not.toBe(routerOwn);
	});
});

describe("a navigation that does not animate", () => {
	test("runs the update and awaits it when the browser has no view transitions", async () => {
		(document as { startViewTransition?: unknown }).startViewTransition =
			undefined;
		navigateTo("/b", "/a");
		let ran = 0;
		await router.startViewTransition(async () => {
			ran++;
		});
		expect(ran).toBe(1);
		expect(started).toEqual([]);
	});

	test("a change of search or hash on the same path just updates", async () => {
		fullMotion();
		navigateTo("/a", "/a");
		let ran = 0;
		await router.startViewTransition(async () => {
			ran++;
		});
		expect(ran).toBe(1);
		expect(started).toEqual([]);
	});

	test("the first navigation, with no previous location, just updates", async () => {
		fullMotion();
		let ran = 0;
		await router.startViewTransition(async () => {
			ran++;
		});
		expect(ran).toBe(1);
		expect(started).toEqual([]);
	});

	test("a held arrow key navigates instantly", async () => {
		fullMotion();
		const link = document.createElement("a");
		document.body.append(link);
		link.dispatchEvent(
			new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }),
		);
		navigateTo("/b", "/a");
		await router.startViewTransition(async () => {});
		expect(started).toEqual([]);
	});

	test("a second navigation within 120 ms of the last start is not animated", async () => {
		fullMotion();
		navigateTo("/b", "/a");
		await router.startViewTransition(async () => {});
		expect(started).toHaveLength(1);
		finishLast();

		router.state.resolvedLocation = { pathname: "/b" };
		router.latestLocation.pathname = "/c";
		clock += 50;
		await router.startViewTransition(async () => {});
		expect(started).toHaveLength(1);
	});

	test("a new page skips a transition that is still running", async () => {
		fullMotion();
		page();
		navigateTo("/b", "/a");
		await router.startViewTransition(async () => {});
		expect(started[0]?.skipped).toBe(false);
		expect(document.documentElement.dataset.vtVariant).toBe("page");

		router.state.resolvedLocation = { pathname: "/b" };
		router.latestLocation.pathname = "/c";
		clock += 10;
		await router.startViewTransition(async () => {});
		expect(started[0]?.skipped).toBe(true);
		expect(document.documentElement.dataset.vtVariant).toBeUndefined();
		expect(nameOf(document.getElementById("main-content") as HTMLElement)).toBe(
			"",
		);
	});

	test("a search-only update leaves a running transition alone", async () => {
		fullMotion();
		navigateTo("/b", "/a");
		await router.startViewTransition(async () => {});
		router.state.resolvedLocation = { pathname: "/b" };
		router.latestLocation.pathname = "/b";
		await router.startViewTransition(async () => {});
		expect(started[0]?.skipped).toBe(false);
	});

	test("falls back to the plain update when the browser refuses to start a transition", async () => {
		fullMotion();
		const main = page();
		startError = new Error("InvalidStateError");
		navigateTo("/b", "/a");
		let ran = 0;
		await router.startViewTransition(async () => {
			ran++;
		});
		expect(ran).toBe(1);
		expect(nameOf(main)).toBe("");
	});
});

describe("a page transition", () => {
	test("names the page area and the pressed card, hands the card's name to the new page's target and cleans up when finished", async () => {
		fullMotion();
		const source = card("project:p1");
		const main = page(source);
		press(source);
		navigateTo("/p/p1", "/");

		let target: HTMLElement | undefined;
		const seenDuringUpdate: Record<string, string> = {};
		await router.startViewTransition(async () => {
			seenDuringUpdate.page = nameOf(main);
			seenDuringUpdate.source = nameOf(source);
			seenDuringUpdate.variant =
				document.documentElement.dataset.vtVariant ?? "";
			seenDuringUpdate.scope = document.documentElement.dataset.vtScope ?? "";
			target = card("project:p1");
			main.append(target);
		});

		expect(seenDuringUpdate).toEqual({
			page: "vt-page",
			source: "vt-project_p1",
			variant: "page",
			scope: "page",
		});
		expect(classOf(main)).toBe("vt-page");
		// The new page's element took the name over from the card.
		expect(target ? nameOf(target) : "").toBe("vt-project_p1");
		expect(target ? classOf(target) : "").toBe("vt-card");
		expect(nameOf(source)).toBe("");

		finishLast();
		await Promise.resolve();
		await Promise.resolve();
		expect(nameOf(main)).toBe("");
		expect(target ? nameOf(target) : "x").toBe("");
		expect(document.documentElement.dataset.vtVariant).toBeUndefined();
		expect(document.documentElement.dataset.vtScope).toBeUndefined();
	});

	test("with no counterpart on the new page the card keeps its name", async () => {
		fullMotion();
		const source = card("server:s1");
		const main = page(source);
		press(source);
		navigateTo("/servers/s1", "/");
		await router.startViewTransition(async () => {});
		expect(nameOf(source)).toBe("vt-server_s1");
		expect(classOf(source)).toBe("vt-card");
		expect(nameOf(main)).toBe("vt-page");
	});

	test("a press from the sidebar plays the fast variant", async () => {
		fullMotion();
		const label = document.createElement("span");
		label.setAttribute("data-vt", "nav:servers");
		const link = document.createElement("a");
		link.append(label);
		const aside = document.createElement("aside");
		aside.setAttribute("aria-label", "Primary");
		aside.append(link);
		document.body.append(aside);
		const main = page();
		press(link);
		navigateTo("/servers", "/");
		let variant = "";
		await router.startViewTransition(async () => {
			variant = document.documentElement.dataset.vtVariant ?? "";
		});
		expect(variant).toBe("fast");
		expect(nameOf(label)).toBe("vt-nav_servers");
		expect(nameOf(main)).toBe("vt-page");
	});

	test("without a keyed press, the leaving page's anchors are armed", async () => {
		fullMotion();
		const anchor = document.createElement("div");
		anchor.setAttribute("data-vt", "project:p9");
		anchor.setAttribute("data-vt-anchor", "");
		page(anchor);
		navigateTo("/", "/p/p9");
		await router.startViewTransition(async () => {});
		expect(nameOf(anchor)).toBe("vt-project_p9");
	});

	test("a keyed press leaves the page's anchors quiet", async () => {
		fullMotion();
		const anchor = document.createElement("div");
		anchor.setAttribute("data-vt", "project:p9");
		anchor.setAttribute("data-vt-anchor", "");
		const other = card("project:p2");
		page(anchor, other);
		press(other);
		navigateTo("/p/p2", "/p/p9");
		await router.startViewTransition(async () => {});
		expect(nameOf(anchor)).toBe("");
		expect(nameOf(other)).toBe("vt-project_p2");
	});

	test("a page without the shell cross-fades the whole document", async () => {
		fullMotion();
		navigateTo("/login", "/");
		let scope = "";
		await router.startViewTransition(async () => {
			scope = document.documentElement.dataset.vtScope ?? "";
		});
		expect(scope).toBe("root");
		expect(document.documentElement.dataset.vtScope).toBe("root");
	});

	test("leaving the shell (signing out) switches the scope to the document once the update has run", async () => {
		fullMotion();
		const main = page();
		navigateTo("/login", "/");
		const scopes: string[] = [];
		await router.startViewTransition(async () => {
			scopes.push(document.documentElement.dataset.vtScope ?? "");
			main.remove();
		});
		expect(scopes).toEqual(["page"]);
		expect(document.documentElement.dataset.vtScope).toBe("root");
	});

	test("a page that arrives with a dialog open shows at once instead of lifting over it", async () => {
		fullMotion();
		page();
		navigateTo("/servers", "/p/p1");
		await router.startViewTransition(async () => {
			const dialog = document.createElement("div");
			dialog.setAttribute("data-slot", "dialog-content");
			dialog.setAttribute("data-open", "");
			document.body.append(dialog);
		});
		expect(started).toEqual([{ skipped: true }]);
	});

	test("a dialog still closing from the leaving page does not stop the lift", async () => {
		fullMotion();
		page();
		const closing = document.createElement("div");
		closing.setAttribute("data-slot", "dialog-content");
		closing.setAttribute("data-closed", "");
		document.body.append(closing);
		navigateTo("/servers", "/p/p1");
		await router.startViewTransition(async () => {});
		expect(started).toEqual([{ skipped: false }]);
	});
});

describe("reduced motion", () => {
	test("lifts the page without morphing any card", async () => {
		const source = card("project:p1");
		const main = page(source);
		press(source);
		navigateTo("/p/p1", "/");
		let variant = "";
		await router.startViewTransition(async () => {
			variant = document.documentElement.dataset.vtVariant ?? "";
		});
		expect(variant).toBe("reduced");
		expect(nameOf(main)).toBe("vt-page");
		expect(nameOf(source)).toBe("");
	});
});
