import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { stubImages } from "./test-env";

// logo-source keeps the registered catalog and the fetched sprite in module
// state: each test works on a private copy of the module.
let copy = 0;
const load = (): Promise<typeof import("./logo-source")> =>
	import(`./logo-source.ts?src${++copy}`);

const SPRITE = `<svg xmlns="http://www.w3.org/2000/svg">
<symbol id="brand-postgresql" viewBox="0 0 24 24"><title>PostgreSQL</title><path d="M1 1h22v22z"/></symbol>
<symbol id="brand-c.js" viewBox="0 0 48 24"><title>C</title><circle cx="5" cy="5" r="3"/></symbol>
<symbol id="brand-weird" viewBox="0 0 10"><path d="M0 0"/></symbol>
</svg>`;

let fetches: string[];
let images: ReturnType<typeof stubImages>;
let respond: () => Response;
const realFetch = globalThis.fetch;

beforeEach(() => {
	fetches = [];
	respond = () => new Response(SPRITE);
	globalThis.fetch = (async (url: string) => {
		fetches.push(String(url));
		return respond();
	}) as unknown as typeof fetch;
	images = stubImages({ width: 64, height: 64 });
});
afterEach(() => {
	globalThis.fetch = realFetch;
	images.restore();
});

const catalog = (
	hex = (slug: string) =>
		`#${slug.length.toString(16).padStart(2, "0").repeat(3)}`,
) => ({
	spriteUrl: "/sprite.svg",
	hex,
});

describe("brandSvg", () => {
	test("needs a catalog first, and says so", async () => {
		const { brandSvg } = await load();
		await expect(brandSvg("postgresql", "#fff")).rejects.toThrow(
			"No brand catalog registered: call setBrandCatalog() first",
		);
	});

	test("extracts one brand as a standalone svg painted in the fill, scaled to 384px on its long side", async () => {
		const { setBrandCatalog, brandSvg } = await load();
		setBrandCatalog(catalog());
		const svg = await brandSvg("postgresql", "#336791");
		const doc = new DOMParser().parseFromString(
			svg,
			"image/svg+xml",
		).documentElement;
		expect(doc.getAttribute("viewBox")).toBe("0 0 24 24");
		expect(doc.getAttribute("width")).toBe("384");
		expect(doc.getAttribute("height")).toBe("384");
		expect(doc.getAttribute("fill")).toBe("#336791");
		expect(doc.querySelector("path")?.getAttribute("d")).toBe("M1 1h22v22z");
		// The sprite's <title> is dropped so the image carries no text.
		expect(svg).not.toContain("<title>");
	});

	test("a wide brand keeps its ratio: the long side is 384", async () => {
		const { setBrandCatalog, brandSvg } = await load();
		setBrandCatalog(catalog());
		const svg = await brandSvg("c.js", "#000");
		expect(svg).toContain('viewBox="0 0 48 24"');
		expect(svg).toContain('width="384" height="192"');
	});

	test("a slug with regex characters is matched literally", async () => {
		const { setBrandCatalog, brandSvg } = await load();
		setBrandCatalog(catalog());
		// `c.js` must not match `cxjs`, and "c.js" itself must still be found.
		await expect(brandSvg("cxjs", "#000")).rejects.toThrow(
			"Brand cxjs is not in the sprite",
		);
		await expect(brandSvg("c.js", "#000")).resolves.toContain("<circle");
	});

	test("an unknown brand is an error naming it", async () => {
		const { setBrandCatalog, brandSvg } = await load();
		setBrandCatalog(catalog());
		await expect(brandSvg("nope", "#000")).rejects.toThrow(
			"Brand nope is not in the sprite",
		);
	});

	test("a viewBox with only a width takes 24 as its height", async () => {
		const { setBrandCatalog, brandSvg } = await load();
		setBrandCatalog(catalog());
		const svg = await brandSvg("weird", "#000");
		expect(svg).toContain('viewBox="0 0 10"');
		expect(svg).toContain('width="160" height="384"');
	});

	test("the sprite is downloaded once and shared", async () => {
		const { setBrandCatalog, brandSvg } = await load();
		setBrandCatalog(catalog());
		await Promise.all([
			brandSvg("postgresql", "#000"),
			brandSvg("c.js", "#000"),
		]);
		await brandSvg("postgresql", "#fff");
		expect(fetches).toEqual(["/sprite.svg"]);
	});

	test("registering a catalog again forgets the sprite", async () => {
		const { setBrandCatalog, brandSvg } = await load();
		setBrandCatalog(catalog());
		await brandSvg("postgresql", "#000");
		setBrandCatalog({ ...catalog(), spriteUrl: "/other.svg" });
		await brandSvg("postgresql", "#000");
		expect(fetches).toEqual(["/sprite.svg", "/other.svg"]);
	});

	test("a failed download reports the status and is retried next time", async () => {
		const { setBrandCatalog, brandSvg } = await load();
		setBrandCatalog(catalog());
		respond = () => new Response("nope", { status: 503 });
		await expect(brandSvg("postgresql", "#000")).rejects.toThrow(
			"Could not load the brand sprite (503)",
		);
		respond = () => new Response(SPRITE);
		await expect(brandSvg("postgresql", "#000")).resolves.toContain("<svg");
		expect(fetches).toHaveLength(2);
	});
});

describe("logoSourceKey", () => {
	test("is distinct per kind and per value", async () => {
		const { logoSourceKey } = await load();
		expect(logoSourceKey({ kind: "url", src: "/a.svg" })).toBe("url:/a.svg");
		expect(logoSourceKey({ kind: "brand", slug: "redis" })).toBe("brand:redis");
		expect(logoSourceKey({ kind: "mark" })).toBe("mark");
		expect(logoSourceKey({ kind: "url", src: "/a.svg" })).not.toBe(
			logoSourceKey({ kind: "url", src: "/b.svg" }),
		);
	});
});

describe("nebulaMarkSource", () => {
	afterEach(() => {
		document.documentElement.style.cssText = "";
	});

	test("paints the mark with the brand-mark tokens of the current theme", async () => {
		const { nebulaMarkSource } = await load();
		const style = document.documentElement.style;
		style.setProperty("--brand-mark-from", "#010203");
		style.setProperty("--brand-mark-mid", "#040506");
		style.setProperty("--brand-mark-to", "#070809");
		style.setProperty("--brand-mark-star", "#0a0b0c");
		const svg = nebulaMarkSource();
		const stops = Array.from(
			new DOMParser()
				.parseFromString(svg, "image/svg+xml")
				.querySelectorAll("stop"),
			(s) => s.getAttribute("stop-color"),
		);
		expect(stops).toEqual(["rgb(1,2,3)", "rgb(4,5,6)", "rgb(7,8,9)"]);
		expect(svg).toContain('fill="rgb(10,11,12)"');
		expect(svg).toContain('width="384" height="384"');
	});

	test("falls back to the brand colours when the tokens are not defined", async () => {
		const { nebulaMarkSource } = await load();
		const svg = nebulaMarkSource();
		expect(svg).toContain('stop-color="rgb(154,107,255)"');
		expect(svg).toContain('stop-color="rgb(109,107,255)"');
		expect(svg).toContain('stop-color="rgb(79,168,255)"');
		expect(svg).toContain('fill="rgb(238,240,255)"');
	});
});

describe("loadLogo", () => {
	const decode = (src: string) =>
		decodeURIComponent(src.split(",").slice(1).join(","));

	test("a url is loaded as is, with its crossOrigin", async () => {
		const { loadLogo } = await load();
		const image = await loadLogo(
			{ kind: "url", src: `/logo-${copy}.png` },
			"anonymous",
		);
		expect(image.crossOrigin).toBe("anonymous");
		expect(images.loaded).toEqual([`/logo-${copy}.png`]);
	});

	test("the mark is rasterised from the theme's own gradient as an svg data url", async () => {
		const { loadLogo } = await load();
		await loadLogo({ kind: "mark" });
		const [src] = images.loaded;
		expect(src).toStartWith("data:image/svg+xml;charset=utf-8,");
		expect(decode(src as string)).toContain("<linearGradient");
	});

	test("a brand is drawn in its own brand colour from the sprite", async () => {
		const { setBrandCatalog, loadLogo } = await load();
		setBrandCatalog(catalog(() => "#336791"));
		await loadLogo({ kind: "brand", slug: "postgresql" });
		const [src] = images.loaded;
		expect(decode(src as string)).toContain('fill="#336791"');
		expect(decode(src as string)).toContain("M1 1h22v22z");
	});

	test("an unknown brand rejects", async () => {
		const { setBrandCatalog, loadLogo } = await load();
		setBrandCatalog(catalog());
		await expect(loadLogo({ kind: "brand", slug: "missing" })).rejects.toThrow(
			"Brand missing is not in the sprite",
		);
		expect(images.loaded).toEqual([]);
	});
});
