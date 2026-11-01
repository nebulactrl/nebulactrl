import { setBrandCatalog } from "@nebulactrl/dither/images/logo-source";
import { BRAND_ICONS, type BrandSlug } from "#/assets/brand-icons";
import spriteUrl from "#/assets/brand-sprite.svg?url";

// Hands the app's brand sprite to the dither kit's `DitheredLogo` and
// `LogoMarquee`, which take `brand="postgres"` and know nothing of our assets.
// Imported once from `main.tsx`.
setBrandCatalog({
	spriteUrl,
	hex: (slug) => BRAND_ICONS[slug as BrandSlug].hex,
});
