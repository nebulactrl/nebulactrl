import { LayersIcon } from "lucide-react";
import type { CSSProperties } from "react";
import { isBrandSlug } from "#/assets/brand-icons";
import { ServiceIcon } from "#/features/canvas/service-icon";
import { templateInitial } from "./lib";

/**
 * A template's mark: its simple-icons logo when the brand sprite has it, else
 * a neutral tile with the template's initial. Nothing is fetched at runtime.
 */
export function TemplateIcon({
	icon,
	name,
	size = 36,
}: {
	icon: string | undefined;
	name: string;
	size?: number;
}) {
	if (icon && isBrandSlug(icon)) {
		// The service icon resolves a brand from an image repository name.
		return <ServiceIcon service={{ config: { image: icon } }} size={size} />;
	}
	return (
		<span
			aria-hidden
			data-slot="service-icon"
			className="service-tile select-none font-semibold text-white"
			style={
				{
					width: size,
					height: size,
					borderRadius: Math.round(size * 0.29),
					fontSize: Math.round(size * 0.46),
					"--t": "oklch(0.45 0.03 262)",
				} as CSSProperties
			}
		>
			{templateInitial(name)}
		</span>
	);
}

/** The Create menu's mark for the Templates entry: a stack of layers on a tile. */
export function TemplatesMark({ size = 22 }: { size?: number }) {
	return (
		<span
			aria-hidden
			data-slot="service-icon"
			className="service-tile select-none text-white"
			style={
				{
					width: size,
					height: size,
					borderRadius: Math.round(size * 0.29),
					"--t": "oklch(0.52 0.15 295)",
				} as CSSProperties
			}
		>
			<LayersIcon
				width={Math.round(size * 0.57)}
				height={Math.round(size * 0.57)}
				strokeWidth={1.8}
			/>
		</span>
	);
}
