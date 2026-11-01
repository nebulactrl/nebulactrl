import { cn } from "cn";
import type { CSSProperties } from "react";
import { BRAND_ICONS } from "#/assets/brand-icons";
import spriteUrl from "#/assets/brand-sprite.svg?url";
import { providerInfo } from "./model";

/** A git provider's mark on the same glossy tile as a service's. */
export function ProviderIcon({
	provider,
	size = 32,
	className,
}: {
	provider: string;
	/** Tile edge in px. */
	size?: number;
	className?: string;
}) {
	const slug = providerInfo(provider).slug;
	const brand = BRAND_ICONS[slug];
	const glyphSize = Math.round(size * 0.57);
	return (
		<span
			data-slot="service-icon"
			aria-hidden
			className={cn("service-tile select-none", className)}
			style={
				{
					width: size,
					height: size,
					borderRadius: Math.round(size * 0.29),
					color: brand.glyph,
					"--t": brand.tile,
				} as CSSProperties
			}
		>
			<svg
				width={glyphSize}
				height={glyphSize}
				viewBox="0 0 24 24"
				fill="currentColor"
				aria-hidden="true"
				style={{ filter: "drop-shadow(0 1px 0 rgb(2 4 10 / 0.35))" }}
			>
				<use href={`${spriteUrl}#brand-${slug}`} />
			</svg>
		</span>
	);
}
