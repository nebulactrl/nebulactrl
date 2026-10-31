import { privateNetworkCaption } from "./model";

/**
 * The caption at the canvas floor: everything on this canvas shares one
 * private network, named for the environment. Text, never all-caps, and it
 * ignores the pointer so it can never sit between you and a card.
 */
export function PrivateNetworkCaption({
	environmentName,
	rightInset,
}: {
	environmentName: string;
	/** Width of the inspector, so the caption stays centred in the visible floor. */
	rightInset: number;
}) {
	return (
		<div
			data-slot="private-network-caption"
			className="pointer-events-none absolute bottom-3.5 left-0 z-[5] flex justify-center"
			style={{ right: rightInset }}
		>
			<span className="type-secondary select-none text-ink-4">
				{privateNetworkCaption(environmentName)}
			</span>
		</div>
	);
}
