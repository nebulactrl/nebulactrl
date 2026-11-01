import { expect, type Page, test } from "@playwright/test";

// Dither shader states on the dev catalogue (`/dev/dither`, dev server only).
// Runs against the same running stack as smoke.spec.ts (`make dev`); only the
// sign-in (E2E_EMAIL / E2E_PASSWORD) is shared with it.
//
//   cd apps/web && bunx playwright test e2e/dither-states.spec.ts
//
// What a host's `data-state` means (packages/dither, lib/gl/dither-shader.tsx):
//   running   drawing frames
//   paused    holds its context but is off screen or in a hidden tab
//   static    one still frame (reduced motion, or `animate={false}`)
//   pending   waiting for its first frame: a context is on its way, so the
//             host is empty (no fallback flash); gives up after about 1.2 s
//   fallback  no WebGL context: unsupported, lost, far from the viewport,
//             handed back to the page-wide budget, or waited too long
//
// The Engine tab's "Motion and performance" section is the fixture for the
// scroll and budget tests: one animated host, a switch that mounts twelve
// more, and the `N live / 8 max` readout from the context budget.

const EMAIL = process.env.E2E_EMAIL ?? "admin@example.com";
const PASSWORD = process.env.E2E_PASSWORD ?? "correct-horse-battery";

const HOST = '[data-slot="dither-shader"]';
const MOTION_HOST =
	'[data-testid="dither-motion-host"] [data-slot="dither-shader"]';
/** The app shell scrolls this element, not the window. */
const SCROLLER = "#main-content";
/** `MAX_LIVE_CONTEXTS` in lib/gl/context-budget.ts. */
const MAX_LIVE = 8;
const POLL = { timeout: 15_000 };

test.use({ viewport: { width: 1280, height: 900 } });

async function signIn(page: Page) {
	const setup = await page.request.get("/api/v1/setup");
	const { needsSetup }: { needsSetup: boolean } = await setup.json();
	if (needsSetup) {
		await page.goto("/setup");
		await page.getByLabel("Your name").fill("E2E Admin");
		await page.getByLabel("Email").fill(EMAIL);
		await page.getByLabel("Password").fill(PASSWORD);
		await page.getByRole("button", { name: /create|continue|set up/i }).click();
	} else {
		await page.goto("/login");
		await page.getByLabel("Email").fill(EMAIL);
		await page.getByLabel("Password").fill(PASSWORD);
		await page.getByRole("button", { name: "Sign in", exact: true }).click();
	}
	await expect(page.locator('[data-page="projects"]')).toBeVisible();
}

type Group = "Backgrounds" | "Engine";

/** Open the catalogue on one of its tabs and wait for that tab's chunk. */
async function openGallery(page: Page, group: Group) {
	await page.goto("/dev/dither");
	await page
		.locator(
			'[data-slot="toggle-group"][aria-label="Group"] [data-slot="toggle-group-item"]',
		)
		.filter({ hasText: group })
		.click();
	await expect(page.locator(HOST).first()).toBeAttached();
	if (group === "Engine") {
		await expect(page.locator(MOTION_HOST)).toBeAttached();
	}
}

function hostStates(page: Page): Promise<string[]> {
	return page
		.locator(HOST)
		.evaluateAll((hosts) =>
			hosts.map((host) => (host as HTMLElement).dataset.state ?? ""),
		);
}

function motionState(page: Page): Promise<string | null> {
	return page.locator(MOTION_HOST).getAttribute("data-state");
}

/** Parse the Engine tab's "N live / M max, K waiting" readout. */
async function readBudget(page: Page) {
	const text = (await page.getByTestId("dither-contexts").textContent()) ?? "";
	const match = /(\d+) live \/ (\d+) max, (\d+) waiting/.exec(text);
	if (!match) throw new Error(`unreadable budget readout: ${text}`);
	return {
		live: Number(match[1]),
		max: Number(match[2]),
		waiting: Number(match[3]),
	};
}

async function scrollMainTo(page: Page, top: number) {
	await page.locator(SCROLLER).evaluate((el, y) => {
		el.scrollTop = y;
	}, top);
}

test.describe("dither shader states", () => {
	test("reduced motion: every shader draws one still frame", async ({
		page,
	}) => {
		await page.emulateMedia({ reducedMotion: "reduce" });
		await signIn(page);

		// One background at a time, so the one host always holds a context.
		await openGallery(page, "Backgrounds");
		await expect
			.poll(async () => {
				const states = await hostStates(page);
				return states.length > 0 && states.every((s) => s === "static");
			}, POLL)
			.toBe(true);

		// The Engine tab mounts more shaders than the budget keeps alive; the
		// ones without a context report `fallback`, none may move.
		await openGallery(page, "Engine");
		await page.locator(MOTION_HOST).scrollIntoViewIfNeeded();
		await expect.poll(() => motionState(page), POLL).toBe("static");
		await expect
			.poll(async () => {
				const states = await hostStates(page);
				return states.every((s) => s === "static" || s === "fallback");
			}, POLL)
			.toBe(true);
	});

	test("scrolling a shader away stops it, scrolling back restarts it", async ({
		page,
	}) => {
		await signIn(page);
		await openGallery(page, "Engine");

		await page.locator(MOTION_HOST).scrollIntoViewIfNeeded();
		await expect.poll(() => motionState(page), POLL).toBe("running");

		// The host sits well below the first screen of the Engine tab.
		await scrollMainTo(page, 0);
		await expect
			.poll(
				() =>
					page
						.locator(MOTION_HOST)
						.evaluate((el) => el.getBoundingClientRect().top),
				POLL,
			)
			.toBeGreaterThan(900);
		// `paused` while it still holds its context; `fallback` once the page
		// budget has taken the context back (a shader far from the viewport).
		// Either way it must not keep running.
		await expect
			.poll(() => motionState(page), POLL)
			.toMatch(/^(paused|fallback)$/);

		await page.locator(MOTION_HOST).scrollIntoViewIfNeeded();
		await expect.poll(() => motionState(page), POLL).toBe("running");
	});

	test("a hidden tab pauses shaders and showing it resumes them", async ({
		page,
	}) => {
		await signIn(page);
		await openGallery(page, "Engine");
		await page.locator(MOTION_HOST).scrollIntoViewIfNeeded();
		await expect.poll(() => motionState(page), POLL).toBe("running");

		await page.evaluate(() => {
			Object.defineProperty(document, "visibilityState", {
				configurable: true,
				get: () => "hidden",
			});
			document.dispatchEvent(new Event("visibilitychange"));
		});
		await expect.poll(() => motionState(page), POLL).toBe("paused");

		await page.evaluate(() => {
			Reflect.deleteProperty(document, "visibilityState");
			document.dispatchEvent(new Event("visibilitychange"));
		});
		await expect.poll(() => motionState(page), POLL).toBe("running");
	});

	test("without WebGL2 every shader shows its fallback and nothing errors", async ({
		page,
	}) => {
		await page.addInitScript(() => {
			const getContext = HTMLCanvasElement.prototype.getContext;
			HTMLCanvasElement.prototype.getContext = function (
				this: HTMLCanvasElement,
				type: string,
				...rest: unknown[]
			) {
				if (type === "webgl2") return null;
				return (getContext as (...args: unknown[]) => unknown).call(
					this,
					type,
					...rest,
				);
			} as typeof getContext;
		});
		await signIn(page);

		const errors: string[] = [];
		page.on("console", (message) => {
			if (message.type() === "error") errors.push(message.text());
		});
		page.on("pageerror", (error) => {
			errors.push(error.message);
		});

		for (const group of ["Backgrounds", "Engine"] as const) {
			await openGallery(page, group);
			await expect
				.poll(async () => {
					const states = await hostStates(page);
					return states.length > 0 && states.every((s) => s === "fallback");
				}, POLL)
				.toBe(true);
		}
		expect(errors).toEqual([]);
	});

	test("more shaders than the budget never hold more than eight contexts", async ({
		page,
	}) => {
		await signIn(page);
		await openGallery(page, "Engine");

		await page.locator(MOTION_HOST).scrollIntoViewIfNeeded();
		await page.getByRole("switch", { name: "Mount twelve shaders" }).click();

		// Thirteen hosts in the Motion section alone, plus the other sections'.
		await expect
			.poll(() => page.locator(HOST).count(), POLL)
			.toBeGreaterThan(MAX_LIVE);

		// More visible shaders than slots: the budget fills to its max, no further.
		await expect
			.poll(async () => (await readBudget(page)).live, POLL)
			.toBe(MAX_LIVE);
		expect((await readBudget(page)).max).toBe(MAX_LIVE);
		await expect
			.poll(async () => (await readBudget(page)).waiting, POLL)
			.toBeGreaterThan(0);

		// The hosts agree with the readout: at most eight draw, the rest are
		// empty (`pending`) for a moment and then wait on their fallback.
		const drawing = async () =>
			(await hostStates(page)).filter(
				(s) => s !== "fallback" && s !== "pending",
			).length;
		await expect.poll(drawing, POLL).toBeLessThanOrEqual(MAX_LIVE);
		await expect.poll(drawing, POLL).toBeGreaterThan(0);

		// Scrolling hands slots around; it must never mint new ones.
		for (const top of [0, 100_000, 0]) {
			await scrollMainTo(page, top);
			await expect
				.poll(async () => (await readBudget(page)).live, POLL)
				.toBeLessThanOrEqual(MAX_LIVE);
			await expect.poll(drawing, POLL).toBeLessThanOrEqual(MAX_LIVE);
		}
	});
});
