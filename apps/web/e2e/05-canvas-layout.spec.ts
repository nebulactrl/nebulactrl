import { expect, type Page, test } from "@playwright/test";
import { deleteProject, getJson, uniqueName } from "./support/api";
import { WHOAMI } from "./support/env";
import { createImageService, createProject, serviceNode } from "./support/ui";

// Flow 5: the canvas. Add two services, drag one, reload: the card is where it
// was left, both in the page and in the layout the control plane saved.

interface Point {
	x: number;
	y: number;
}

async function position(page: Page, name: string): Promise<Point> {
	const box = await serviceNode(page, name).boundingBox();
	if (!box) throw new Error(`${name} has no box`);
	return { x: box.x, y: box.y };
}

/** Pointer travel of the drag, in screen pixels, and what a card may be off by. */
const DRAG = { x: 40, y: 230 };
const TOLERANCE = 24;

/**
 * A card's screen position once it has stopped moving: the canvas animates a
 * card to where it was dropped and may fit the view, so two equal reads in a row.
 */
async function settled(page: Page, name: string): Promise<Point> {
	let last = await position(page, name);
	await expect
		.poll(
			async () => {
				const now = await position(page, name);
				const still = now.x === last.x && now.y === last.y;
				last = now;
				return still;
			},
			{ intervals: [150], timeout: 5_000 },
		)
		.toBe(true);
	return last;
}

async function serviceId(page: Page, name: string): Promise<string> {
	const id = await serviceNode(page, name).getAttribute("data-service-id");
	if (!id) throw new Error(`${name} has no data-service-id`);
	return id;
}

test("add and move a service, reload, the layout persists", async ({
	page,
}) => {
	const project = uniqueName("e2e-canvas");
	try {
		await createProject(page, project);
		await createImageService(page, { name: "alpha", image: WHOAMI, port: 80 });
		await createImageService(page, { name: "bravo", image: WHOAMI, port: 80 });
		const [alphaId, bravoId] = [
			await serviceId(page, "alpha"),
			await serviceId(page, "bravo"),
		];
		const url = new URL(page.url());
		const [, , projectId] = url.pathname.split("/");
		const environments = await getJson<{ items: { id: string }[] }>(
			page.request,
			`/projects/${projectId}/environments`,
		);
		const canvasPath = `/projects/${projectId}/environments/${environments.items[0]?.id}/canvas`;
		const layout = () =>
			getJson<{ layout: { nodes: Record<string, Point> } }>(
				page.request,
				canvasPath,
			).then((c) => c.layout.nodes);

		// Both cards sit somewhere; drag bravo well away from where it was. The
		// canvas is a pan-and-zoom surface: a card's screen position is its saved
		// (flow) position times the zoom plus the pan, so what the page shows is
		// compared in screen pixels and what was saved in flow units.
		const zoomOf = () =>
			page
				.locator(".react-flow__viewport")
				.evaluate(
					(el) => new DOMMatrixReadOnly(getComputedStyle(el).transform).a,
				);
		const before = {
			alpha: await settled(page, "alpha"),
			bravo: await settled(page, "bravo"),
			zoom: await zoomOf(),
		};
		const bravo = await serviceNode(page, "bravo").boundingBox();
		if (!bravo) throw new Error("bravo has no box");
		const saved = page.waitForResponse(
			(res) =>
				res.url().includes("/canvas") && res.request().method() === "PUT",
		);
		await page.mouse.move(bravo.x + bravo.width / 2, bravo.y + 20);
		await page.mouse.down();
		await page.mouse.move(bravo.x + bravo.width / 2 + 120, bravo.y + 80, {
			steps: 8,
		});
		await page.mouse.move(
			bravo.x + bravo.width / 2 + DRAG.x,
			bravo.y + 20 + DRAG.y,
			{
				steps: 12,
			},
		);
		await page.mouse.up();
		expect((await saved).ok(), "the layout is saved").toBe(true);

		// On screen: bravo followed the pointer. Measured from alpha, because the
		// canvas may pan while a card is dragged near its edge.
		const alphaNow = await settled(page, "alpha");
		const moved = await settled(page, "bravo");
		const zoom = await zoomOf();
		const offsetX = moved.x - alphaNow.x;
		const offsetY = moved.y - alphaNow.y;
		expect(
			Math.abs(offsetX - (before.bravo.x - before.alpha.x) - DRAG.x),
		).toBeLessThan(TOLERANCE);
		expect(
			Math.abs(offsetY - (before.bravo.y - before.alpha.y) - DRAG.y),
		).toBeLessThan(TOLERANCE);

		// Saved: the control plane has both cards, and bravo is where the drag put
		// it: its offset from alpha in canvas units, times the zoom, is what the
		// page shows. Polled, because the layout is written a moment after the
		// card is dropped. A drag that was never persisted keeps the saved offset
		// hundreds of pixels from the one on screen and times out.
		const saveOffset = async () => {
			const nodes = await layout();
			return {
				nodes,
				x: ((nodes[bravoId]?.x ?? 0) - (nodes[alphaId]?.x ?? 0)) * zoom,
				y: ((nodes[bravoId]?.y ?? 0) - (nodes[alphaId]?.y ?? 0)) * zoom,
			};
		};
		await expect
			.poll(
				async () => {
					const saved = await saveOffset();
					return Math.max(
						Math.abs(saved.x - offsetX),
						Math.abs(saved.y - offsetY),
					);
				},
				{
					message:
						"the layout the control plane saved matches the cards on screen",
					timeout: 15_000,
				},
			)
			.toBeLessThan(TOLERANCE);
		const { nodes, x: savedX, y: savedY } = await saveOffset();
		expect(Object.keys(nodes)).toEqual(
			expect.arrayContaining([alphaId, bravoId]),
		);
		expect(
			Math.abs(savedX - (before.bravo.x - before.alpha.x) - DRAG.x),
		).toBeLessThan(TOLERANCE);
		expect(
			Math.abs(savedY - (before.bravo.y - before.alpha.y) - DRAG.y),
		).toBeLessThan(TOLERANCE);
		const dx = savedX / zoom;
		const dy = savedY / zoom;

		// After a reload the cards are laid out as saved: bravo keeps its offset
		// from alpha (the reload may fit the view to another zoom).
		await page.reload();
		await expect(serviceNode(page, "bravo")).toBeVisible();
		const afterAlpha = await settled(page, "alpha");
		const afterBravo = await settled(page, "bravo");
		const zoomAfter = await zoomOf();
		expect(Math.abs(afterBravo.x - afterAlpha.x - dx * zoomAfter)).toBeLessThan(
			TOLERANCE,
		);
		expect(Math.abs(afterBravo.y - afterAlpha.y - dy * zoomAfter)).toBeLessThan(
			TOLERANCE,
		);
		expect(await layout()).toEqual(nodes);
	} finally {
		await deleteProject(page.request, project);
	}
});
