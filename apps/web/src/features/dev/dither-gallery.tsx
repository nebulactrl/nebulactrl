import { type ComponentType, lazy, Suspense, useState } from "react";
import { Segmented } from "#/components/ui/segmented";

/*
 * The dither catalogue (dev only, route `/dev/dither`). One tab per group;
 * each group's gallery is its own chunk, so opening Charts does not compile
 * thirty WebGL backgrounds.
 */

const GROUPS = {
	charts: lazy(() =>
		import("#/components/dither/charts/gallery").then((m) => ({
			default: m.ChartsGallery as ComponentType,
		})),
	),
	backgrounds: lazy(() =>
		import("#/components/dither/backgrounds/gallery").then((m) => ({
			default: m.BackgroundsGallery as ComponentType,
		})),
	),
	interface: lazy(() =>
		import("#/components/dither/interface/gallery").then((m) => ({
			default: m.InterfaceGallery as ComponentType,
		})),
	),
	text: lazy(() =>
		import("#/components/dither/text/gallery").then((m) => ({
			default: m.TextGallery as ComponentType,
		})),
	),
	images: lazy(() =>
		import("#/components/dither/images/gallery").then((m) => ({
			default: m.ImagesGallery as ComponentType,
		})),
	),
	engine: lazy(() =>
		import("#/components/dither/engine/gallery").then((m) => ({
			default: m.EngineGallery as ComponentType,
		})),
	),
} as const;

type Group = keyof typeof GROUPS;

const OPTIONS: readonly { value: Group; label: string }[] = [
	{ value: "charts", label: "Charts" },
	{ value: "backgrounds", label: "Backgrounds" },
	{ value: "interface", label: "Interface" },
	{ value: "text", label: "Text" },
	{ value: "images", label: "Images" },
	{ value: "engine", label: "Engine" },
];

export function DitherGalleryPage() {
	const [group, setGroup] = useState<Group>("charts");
	const Gallery = GROUPS[group];
	return (
		<main className="mx-auto flex w-full max-w-[1200px] flex-col gap-5 px-6 py-8">
			<header className="flex flex-wrap items-end justify-between gap-3">
				<div className="flex flex-col gap-1.5">
					<h1 className="type-page-title">Dither</h1>
					<p className="type-secondary max-w-[56ch]">
						Our ordered-dither kit: charts, backgrounds, interface pieces, text
						and images, all drawn from the same cells and tokens. Dev only.
					</p>
				</div>
				<Segmented
					aria-label="Group"
					font="sans"
					value={group}
					onValueChange={setGroup}
					options={OPTIONS}
				/>
			</header>
			<Suspense
				fallback={
					<p className="font-mono text-ink-3 text-xs">Loading {group}…</p>
				}
			>
				<Gallery />
			</Suspense>
		</main>
	);
}
