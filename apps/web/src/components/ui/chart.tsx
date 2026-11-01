import { cn } from "cn";
import * as React from "react";
import * as RechartsPrimitive from "recharts";

// shadcn/ui's chart block (Recharts v3). Series colours come from
// `ChartConfig` as CSS custom properties (`var(--chart-1)`), which SVG
// paints resolve natively, so no colour literal ever reaches a component.

// Format: { THEME_NAME: CSS_SELECTOR }
const THEMES = { light: "", dark: ".dark" } as const;

const INITIAL_DIMENSION = { width: 320, height: 200 } as const;

export type ChartConfig = Record<
	string,
	{
		label?: React.ReactNode;
		icon?: React.ComponentType;
	} & (
		| { color?: string; theme?: never }
		| { color?: never; theme: Record<keyof typeof THEMES, string> }
	)
>;

function ChartContainer({
	id,
	className,
	children,
	config,
	initialDimension = INITIAL_DIMENSION,
	...props
}: React.ComponentProps<"div"> & {
	config: ChartConfig;
	children: React.ComponentProps<
		typeof RechartsPrimitive.ResponsiveContainer
	>["children"];
	initialDimension?: {
		width: number;
		height: number;
	};
}) {
	const uniqueId = React.useId();
	const chartId = `chart-${id ?? uniqueId.replace(/:/g, "")}`;

	return (
		<div
			data-slot="chart"
			data-chart={chartId}
			className={cn(
				// Upstream also matches Recharts' default #ccc/#fff strokes here;
				// this repo forbids colour literals in TSX, so every chart sets
				// its grid, dot and reference-line strokes to tokens itself.
				"flex aspect-video justify-center text-xs [&_.recharts-cartesian-axis-tick_text]:fill-ink-3 [&_.recharts-cartesian-grid_line]:stroke-line [&_.recharts-curve.recharts-tooltip-cursor]:stroke-line-strong [&_.recharts-layer]:outline-hidden [&_.recharts-radial-bar-background-sector]:fill-surface-well [&_.recharts-rectangle.recharts-tooltip-cursor]:fill-white/6 [&_.recharts-sector]:outline-hidden [&_.recharts-surface]:outline-hidden",
				className,
			)}
			{...props}
		>
			<ChartStyle id={chartId} config={config} />
			<RechartsPrimitive.ResponsiveContainer
				initialDimension={initialDimension}
			>
				{children}
			</RechartsPrimitive.ResponsiveContainer>
		</div>
	);
}

const ChartStyle = ({ id, config }: { id: string; config: ChartConfig }) => {
	const colorConfig = Object.entries(config).filter(
		([, config]) => config.theme ?? config.color,
	);

	if (!colorConfig.length) {
		return null;
	}

	return (
		<style
			// biome-ignore lint/security/noDangerouslySetInnerHtml: the rules are built from this component's own ChartConfig (token references), never from user input.
			dangerouslySetInnerHTML={{
				__html: Object.entries(THEMES)
					.map(
						([theme, prefix]) => `
${prefix} [data-chart=${id}] {
${colorConfig
	.map(([key, itemConfig]) => {
		const color =
			itemConfig.theme?.[theme as keyof typeof itemConfig.theme] ??
			itemConfig.color;
		return color ? `  --color-${key}: ${color};` : null;
	})
	.join("\n")}
}
`,
					)
					.join("\n"),
			}}
		/>
	);
};

export { ChartContainer };
