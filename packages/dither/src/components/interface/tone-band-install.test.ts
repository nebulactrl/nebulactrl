import { describe, expect, test } from "bun:test";

// `installToneBandMask` installs once per module instance, so each test loads a
// fresh copy of the module.
const fresh = (tag: string): Promise<typeof import("./tone-band-math")> =>
	import(`./tone-band-math.ts?${tag}`);

describe("installToneBandMask", () => {
	test("defines --tone-band-mask on the root as the dither data URI, once", async () => {
		const root = document.documentElement;
		root.style.removeProperty("--tone-band-mask");
		const mod = await fresh("install-1");
		mod.installToneBandMask();
		const value = root.style.getPropertyValue("--tone-band-mask");
		expect(value).toBe(mod.toneBandMaskCss());
		expect(value).toStartWith('url("data:image/svg+xml,');

		// A second call does not rewrite what a theme layer set in between.
		root.style.setProperty("--tone-band-mask", "none");
		mod.installToneBandMask();
		expect(root.style.getPropertyValue("--tone-band-mask")).toBe("none");
		root.style.removeProperty("--tone-band-mask");
	});

	test("toneBandProps installs the mask as a side effect", async () => {
		const root = document.documentElement;
		root.style.removeProperty("--tone-band-mask");
		const mod = await fresh("install-2");
		expect(root.style.getPropertyValue("--tone-band-mask")).toBe("");
		mod.toneBandProps("warning");
		expect(root.style.getPropertyValue("--tone-band-mask")).toStartWith(
			'url("data:image/svg+xml,',
		);
		root.style.removeProperty("--tone-band-mask");
	});
});
