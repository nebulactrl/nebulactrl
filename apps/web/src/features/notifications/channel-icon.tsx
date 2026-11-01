import { cn } from "cn";
import { HashIcon, type LucideIcon, MailIcon, WebhookIcon } from "lucide-react";
import type { CSSProperties } from "react";
import { BRAND_ICONS, type BrandSlug } from "#/assets/brand-icons";
import spriteUrl from "#/assets/brand-sprite.svg?url";
import type { ChannelKind } from "./model";

type Mark =
	| { type: "brand"; slug: BrandSlug }
	| { type: "generic"; Icon: LucideIcon; tile: string; glyph: string };

// Discord and Telegram come from the brand sprite. Slack is not in
// simple-icons any more, so it is a hash glyph on Slack's aubergine; email and
// a signed webhook have no brand at all.
const MARKS: Record<ChannelKind, Mark> = {
	discord: { type: "brand", slug: "discord" },
	telegram: { type: "brand", slug: "telegram" },
	slack: { type: "generic", Icon: HashIcon, tile: "#4a154b", glyph: "#ffffff" },
	email: {
		type: "generic",
		Icon: MailIcon,
		tile: "oklch(0.52 0.12 235)",
		glyph: "#ffffff",
	},
	webhook: {
		type: "generic",
		Icon: WebhookIcon,
		tile: "oklch(0.5 0.15 295)",
		glyph: "#ffffff",
	},
};

/** A channel's mark on the same glossy tile as a service's (`service-tile`). */
export function ChannelIcon({
	kind,
	size = 36,
	className,
}: {
	kind: ChannelKind;
	/** Tile edge in px. */
	size?: number;
	className?: string;
}) {
	const mark = MARKS[kind];
	const brand = mark.type === "brand" ? BRAND_ICONS[mark.slug] : null;
	const tile = brand ? brand.tile : mark.type === "generic" ? mark.tile : "";
	const glyph = brand ? brand.glyph : mark.type === "generic" ? mark.glyph : "";
	const glyphSize = Math.round(size * 0.57);
	const shadow = { filter: "drop-shadow(0 1px 0 rgb(2 4 10 / 0.35))" };
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
					color: glyph,
					"--t": tile,
				} as CSSProperties
			}
		>
			{mark.type === "brand" ? (
				<svg
					width={glyphSize}
					height={glyphSize}
					viewBox="0 0 24 24"
					fill="currentColor"
					aria-hidden="true"
					style={shadow}
				>
					<use href={`${spriteUrl}#brand-${mark.slug}`} />
				</svg>
			) : (
				<mark.Icon
					width={glyphSize}
					height={glyphSize}
					strokeWidth={1.8}
					aria-hidden="true"
					style={shadow}
				/>
			)}
		</span>
	);
}
