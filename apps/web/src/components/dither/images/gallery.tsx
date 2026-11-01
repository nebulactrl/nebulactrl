import {
	DITHER_METHODS,
	type DitherMethod,
} from "@nebulactrl/dither/images/algorithms";
import { DitherImage } from "@nebulactrl/dither/images/dither-image";
import { DitheredLogo } from "@nebulactrl/dither/images/dithered-logo";
import { ImageCompare } from "@nebulactrl/dither/images/image-compare";
import type {
	LogoEffect,
	LogoFill,
} from "@nebulactrl/dither/images/logo-cells";
import {
	LogoMarquee,
	type MarqueeLogo,
} from "@nebulactrl/dither/images/logo-marquee";
import {
	DITHER_IMAGE_PRESETS,
	type DitherImagePreset,
} from "@nebulactrl/dither/images/look";
import { SAMPLE_IMAGE, SAMPLE_RATIO } from "@nebulactrl/dither/images/sample";
import { ScrollRevealImage } from "@nebulactrl/dither/images/scroll-reveal-image";
import type { DitherMatrix } from "@nebulactrl/dither/lib/bayer";
import {
	canvasToBitmap,
	downloadText,
	exportCSS,
	exportPNG,
	exportSVG,
} from "@nebulactrl/dither/lib/export";
import { DITHER_COLOR_NAMES } from "@nebulactrl/dither/lib/tokens";
import { cn } from "cn";
import { useRef, useState } from "react";
import type { BrandSlug } from "#/assets/brand-icons";
import { Button } from "#/components/ui/button";
import { Segmented } from "#/components/ui/segmented";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Switch } from "#/components/ui/switch";
import { GallerySection, Knob, RangeKnob } from "../text/gallery-parts";

const toOptions = <T extends string>(values: readonly T[]) =>
	values.map((value) => ({ value, label: value }));

/** Up to this many options read as a segmented control; more go in a select. */
const SEGMENTED_MAX = 5;

/** One choice among a few or many short options. */
function Choice<T extends string>({
	label,
	value,
	onChange,
	options,
}: {
	label: string;
	value: T;
	onChange: (next: T) => void;
	options: readonly T[];
}) {
	if (options.length <= SEGMENTED_MAX) {
		return (
			<Segmented<T>
				aria-label={label}
				value={value}
				onValueChange={onChange}
				options={toOptions(options)}
			/>
		);
	}
	return (
		<Select
			value={value}
			onValueChange={(next) => {
				if (typeof next === "string") onChange(next as T);
			}}
		>
			<SelectTrigger size="sm" aria-label={label} className="w-full">
				<SelectValue />
			</SelectTrigger>
			<SelectContent alignItemWithTrigger={false}>
				{options.map((option) => (
					<SelectItem key={option} value={option}>
						{option}
					</SelectItem>
				))}
			</SelectContent>
		</Select>
	);
}

const INKS = ["ink", ...DITHER_COLOR_NAMES] as const;
type Ink = (typeof INKS)[number];

const METHOD_OPTIONS = ["preset", ...DITHER_METHODS] as const;
type MethodChoice = (typeof METHOD_OPTIONS)[number];

const MATRICES: readonly DitherMatrix[] = [
	"bayer2",
	"bayer4",
	"bayer8",
	"blue-noise",
];

/** Your own palette: the page, then two named hues. */
const CUSTOM_PALETTE = ["--dither-paper", "green", "pink"] as const;

function DitherImageDemo() {
	const [preset, setPreset] = useState<DitherImagePreset>("duotone");
	const [color, setColor] = useState<Ink>("blue");
	const [method, setMethod] = useState<MethodChoice>("preset");
	const [custom, setCustom] = useState(false);
	const [cell, setCell] = useState(2);
	const [contrast, setContrast] = useState(1.1);
	const [sharpen, setSharpen] = useState(0.3);
	const [invert, setInvert] = useState(false);
	const [css, setCss] = useState("");
	const canvas = useRef<HTMLCanvasElement | null>(null);
	// A stable callback: the layer re-reports the canvas whenever this changes.
	const [onCanvas] = useState(() => (el: HTMLCanvasElement | null) => {
		canvas.current = el;
	});

	const svg = () => {
		const el = canvas.current;
		if (!el) return null;
		return exportSVG(
			{ kind: "bitmap", bitmap: canvasToBitmap(el) },
			{ cell, title: "Dithered landscape" },
		);
	};

	return (
		<GallerySection
			title="DitherImage"
			description="Any photo dithered to a preset (ink, duotone, newsprint, gameboy, riso) or your own palette, by Bayer, blue noise or error diffusion. It loads near the viewport, resolves from coarse blocks and caches per size."
			stage={
				<div className="flex w-full flex-col gap-3">
					<DitherImage
						src={SAMPLE_IMAGE}
						alt="A made-up landscape: layered violet mountains under a setting sun"
						ratio={SAMPLE_RATIO}
						preset={preset}
						color={color}
						{...(method === "preset" ? {} : { method: method as DitherMethod })}
						{...(custom ? { palette: CUSTOM_PALETTE } : {})}
						cell={cell}
						contrast={contrast}
						sharpen={sharpen}
						invert={invert}
						onCanvas={onCanvas}
						className="w-full rounded-ctl"
					/>
					{css ? (
						<pre className="max-h-28 overflow-auto rounded-ctl border border-line bg-surface-well p-2 font-mono text-[11px] text-ink-3">
							{css.length > 420 ? `${css.slice(0, 420)}…` : css}
						</pre>
					) : null}
				</div>
			}
		>
			<Knob label="Preset">
				<Choice
					label="Preset"
					value={preset}
					onChange={setPreset}
					options={DITHER_IMAGE_PRESETS}
				/>
			</Knob>
			<Knob label="Ink (ink, duotone)">
				<Choice label="Ink" value={color} onChange={setColor} options={INKS} />
			</Knob>
			<Knob label="Method">
				<Choice
					label="Method"
					value={method}
					onChange={setMethod}
					options={METHOD_OPTIONS}
				/>
			</Knob>
			<Knob label="Palette: paper, green, pink">
				<Switch
					aria-label="Custom palette"
					checked={custom}
					onCheckedChange={setCustom}
				/>
			</Knob>
			<RangeKnob
				label="Cell"
				value={cell}
				min={1}
				max={8}
				step={1}
				onChange={setCell}
			/>
			<RangeKnob
				label="Contrast"
				value={contrast}
				min={0.5}
				max={2}
				step={0.05}
				onChange={setContrast}
			/>
			<RangeKnob
				label="Sharpen"
				value={sharpen}
				min={0}
				max={1}
				step={0.1}
				onChange={setSharpen}
			/>
			<Knob label="Invert">
				<Switch
					aria-label="Invert"
					checked={invert}
					onCheckedChange={setInvert}
				/>
			</Knob>
			<Knob label="Export">
				<div className="flex flex-wrap gap-2">
					<Button
						variant="secondary"
						size="md"
						onClick={() => {
							if (canvas.current)
								void exportPNG(canvas.current, {
									scale: 2,
									filename: "dither.png",
								});
						}}
					>
						PNG
					</Button>
					<Button
						variant="secondary"
						size="md"
						onClick={() => {
							const out = svg();
							if (out) downloadText(out, "dither.svg", "image/svg+xml");
						}}
					>
						SVG
					</Button>
					<Button
						variant="secondary"
						size="md"
						onClick={() => {
							const out = svg();
							const el = canvas.current;
							if (out && el)
								setCss(
									exportCSS(
										{
											kind: "svg",
											svg: out,
											width: el.width * cell,
											height: el.height * cell,
										},
										{ selector: ".hero" },
									),
								);
						}}
					>
						CSS
					</Button>
				</div>
			</Knob>
		</GallerySection>
	);
}

type CompareRight = "original" | DitherImagePreset;
const COMPARE_RIGHT: readonly CompareRight[] = [
	"original",
	...DITHER_IMAGE_PRESETS,
];

function ImageCompareDemo() {
	const [left, setLeft] = useState<DitherImagePreset>("newsprint");
	const [right, setRight] = useState<CompareRight>("original");
	const [color, setColor] = useState<Ink>("blue");
	const [labels, setLabels] = useState(true);
	const [contain, setContain] = useState(false);
	const [value, setValue] = useState(50);
	return (
		<GallerySection
			title="ImageCompare"
			description="A before/after slider. Drag, click (it glides), or focus the handle and use the arrow keys: Shift or Page Up/Down for 10, Home and End for the ends. Either side is the original or any dither look."
			stage={
				<ImageCompare
					key={`${left}-${right}-${color}`}
					src={SAMPLE_IMAGE}
					alt="The landscape dithered on the left and on the right"
					ratio={SAMPLE_RATIO}
					before={{ kind: "dither", preset: left, color }}
					after={
						right === "original"
							? { kind: "original" }
							: { kind: "dither", preset: right, color }
					}
					beforeLabel={left}
					afterLabel={right === "original" ? "Original" : right}
					labels={labels}
					fit={contain ? "contain" : "cover"}
					value={value}
					onValueChange={setValue}
					className="w-full rounded-ctl"
				/>
			}
		>
			<Knob label="Left side">
				<Choice
					label="Left preset"
					value={left}
					onChange={setLeft}
					options={DITHER_IMAGE_PRESETS}
				/>
			</Knob>
			<Knob label="Right side">
				<Choice
					label="Right side"
					value={right}
					onChange={setRight}
					options={COMPARE_RIGHT}
				/>
			</Knob>
			<Knob label="Ink">
				<Choice label="Ink" value={color} onChange={setColor} options={INKS} />
			</Knob>
			<RangeKnob
				label="Position"
				value={value}
				min={0}
				max={100}
				step={1}
				onChange={setValue}
			/>
			<Knob label="Labels">
				<Switch
					aria-label="Labels"
					checked={labels}
					onCheckedChange={setLabels}
				/>
			</Knob>
			<Knob label="Contain">
				<Switch
					aria-label="Fit: contain"
					checked={contain}
					onCheckedChange={setContain}
				/>
			</Knob>
		</GallerySection>
	);
}

function ScrollRevealDemo() {
	const panel = useRef<HTMLDivElement | null>(null);
	const [preset, setPreset] = useState<DitherImagePreset>("duotone");
	const [color, setColor] = useState<Ink>("blue");
	const [end, setEnd] = useState<"photo" | "dither">("photo");
	const [coarsest, setCoarsest] = useState(24);
	const [manual, setManual] = useState(false);
	const [progress, setProgress] = useState(0.5);
	const [seen, setSeen] = useState(0);
	return (
		<GallerySection
			title="ScrollRevealImage"
			description="Sharpens from coarse dithered blocks to the full photo as it scrolls into view, in nine steps, then dissolves into the photo cell by cell. Scroll the panel, or set the progress yourself."
			stageClassName="items-stretch p-0"
			stage={
				<div
					ref={panel}
					className="h-96 w-full overflow-y-auto overscroll-contain"
				>
					<div className="flex flex-col gap-6 p-6">
						<p className="text-ink-3 text-sm">
							Scroll down: the image resolves as it reaches you. Progress{" "}
							<span className="font-mono tabular-nums">{seen.toFixed(2)}</span>
						</p>
						<div className="h-72" aria-hidden="true" />
						<ScrollRevealImage
							key={`${end}-${coarsest}-${preset}-${color}`}
							src={SAMPLE_IMAGE}
							alt="The landscape, sharpening as it scrolls into view"
							ratio={SAMPLE_RATIO}
							preset={preset}
							color={color}
							end={end}
							coarsest={coarsest}
							scrollRoot={panel}
							{...(manual ? { progress } : {})}
							onProgressChange={setSeen}
							className="w-full rounded-ctl"
						/>
						<div className="h-48" aria-hidden="true" />
					</div>
				</div>
			}
		>
			<Knob label="Preset">
				<Choice
					label="Preset"
					value={preset}
					onChange={setPreset}
					options={DITHER_IMAGE_PRESETS}
				/>
			</Knob>
			<Knob label="Ink">
				<Choice label="Ink" value={color} onChange={setColor} options={INKS} />
			</Knob>
			<Knob label="Ends on">
				<Segmented<"photo" | "dither">
					aria-label="Ends on"
					value={end}
					onValueChange={setEnd}
					options={toOptions(["photo", "dither"] as const)}
				/>
			</Knob>
			<RangeKnob
				label="First blocks"
				value={coarsest}
				min={6}
				max={48}
				step={2}
				onChange={setCoarsest}
			/>
			<Knob label="Set the progress yourself">
				<Switch
					aria-label="Manual progress"
					checked={manual}
					onCheckedChange={setManual}
				/>
			</Knob>
			<RangeKnob
				label="Progress"
				value={progress}
				min={0}
				max={1}
				step={0.01}
				onChange={setProgress}
			/>
		</GallerySection>
	);
}

type LogoSourceName = "mark" | "postgresql" | "redis" | "docker";

const LOGO_SOURCES: readonly LogoSourceName[] = [
	"mark",
	"postgresql",
	"redis",
	"docker",
];
const LOGO_FILLS: readonly LogoFill[] = [
	"gradient",
	"solid",
	"dotted",
	"hatched",
];
const LOGO_EFFECTS: readonly LogoEffect[] = ["none", "shimmer", "scan"];

function DitheredLogoDemo() {
	const [source, setSource] = useState<LogoSourceName>("mark");
	const [fill, setFill] = useState<LogoFill>("gradient");
	const [effect, setEffect] = useState<LogoEffect>("shimmer");
	const [color, setColor] = useState<Ink>("ink");
	const [matrix, setMatrix] = useState<DitherMatrix>("bayer4");
	const [original, setOriginal] = useState(false);
	const [outline, setOutline] = useState(true);
	const [cell, setCell] = useState(3);
	const [floor, setFloor] = useState(0.3);
	const [period, setPeriod] = useState(4);
	const [phase, setPhase] = useState(0);
	return (
		<GallerySection
			title="DitheredLogo"
			description="Our mark or any brand of the sprite, redrawn on a pixel grid: exact edges, a solid outline and a dithered fill, with an optional shimmer or scan line. Seams between colours keep the parts apart in one ink."
			stage={
				<DitheredLogo
					{...(source === "mark" ? {} : { brand: source as BrandSlug })}
					alt={source === "mark" ? "NebulaCtrl" : source}
					fill={fill}
					effect={effect}
					tone={original ? "original" : "mono"}
					color={color}
					matrix={matrix}
					outline={outline}
					floor={floor}
					period={period}
					phase={phase}
					cell={cell}
					className="h-40"
				/>
			}
		>
			<Knob label="Logo">
				<Choice
					label="Logo"
					value={source}
					onChange={setSource}
					options={LOGO_SOURCES}
				/>
			</Knob>
			<Knob label="Fill">
				<Choice
					label="Fill"
					value={fill}
					onChange={setFill}
					options={LOGO_FILLS}
				/>
			</Knob>
			<Knob label="Effect">
				<Choice
					label="Effect"
					value={effect}
					onChange={setEffect}
					options={LOGO_EFFECTS}
				/>
			</Knob>
			<Knob label="Ink">
				<Choice label="Ink" value={color} onChange={setColor} options={INKS} />
			</Knob>
			<Knob label="Matrix">
				<Choice
					label="Matrix"
					value={matrix}
					onChange={setMatrix}
					options={MATRICES}
				/>
			</Knob>
			<RangeKnob
				label="Cell"
				value={cell}
				min={1}
				max={6}
				step={1}
				onChange={setCell}
			/>
			<RangeKnob
				label="Floor"
				value={floor}
				min={0}
				max={1}
				step={0.05}
				onChange={setFloor}
			/>
			<RangeKnob
				label="Period"
				value={period}
				min={1}
				max={12}
				step={0.5}
				onChange={setPeriod}
			/>
			<RangeKnob
				label="Phase"
				value={phase}
				min={0}
				max={1}
				step={0.05}
				onChange={setPhase}
			/>
			<Knob label="Outline">
				<Switch
					aria-label="Outline"
					checked={outline}
					onCheckedChange={setOutline}
				/>
			</Knob>
			<Knob label="Original colours">
				<Switch
					aria-label="Original colours"
					checked={original}
					onCheckedChange={setOriginal}
				/>
			</Knob>
		</GallerySection>
	);
}

const BRANDS: readonly MarqueeLogo[] = (
	[
		"postgresql",
		"redis",
		"mysql",
		"mongodb",
		"nginx",
		"docker",
		"grafana",
		"github",
		"rust",
		"go",
		"react",
		"python",
	] as const
).map((brand) => ({ brand, alt: brand }));

function LogoMarqueeDemo() {
	const [variant, setVariant] = useState<"marquee" | "wall">("marquee");
	const [fade, setFade] = useState<"soft" | "dither" | "none">("dither");
	const [direction, setDirection] = useState<"left" | "right">("left");
	const [fill, setFill] = useState<LogoFill>("gradient");
	const [effect, setEffect] = useState<LogoEffect>("none");
	const [color, setColor] = useState<Ink>("ink");
	const [speed, setSpeed] = useState(32);
	const [pauseButton, setPauseButton] = useState(true);
	return (
		<GallerySection
			title="LogoMarquee"
			description="A looping row, or a still wall, of logos in one dithered treatment. Hover or focus the row and it coasts to a stop; the ends fade softly or in Bayer steps. Still under reduced motion."
			stageClassName="p-4"
			stage={
				<LogoMarquee
					key={variant}
					logos={variant === "wall" ? BRANDS.slice(0, 8) : BRANDS}
					label="Technologies we run"
					variant={variant}
					fade={fade}
					direction={direction}
					speed={speed}
					fill={fill}
					effect={effect}
					color={color}
					pauseButton={pauseButton}
					cell={2}
					className={cn("w-full")}
				/>
			}
		>
			<Knob label="Variant">
				<Choice
					label="Variant"
					value={variant}
					onChange={setVariant}
					options={["marquee", "wall"] as const}
				/>
			</Knob>
			<Knob label="Edge fade">
				<Choice
					label="Edge fade"
					value={fade}
					onChange={setFade}
					options={["soft", "dither", "none"] as const}
				/>
			</Knob>
			<Knob label="Direction">
				<Choice
					label="Direction"
					value={direction}
					onChange={setDirection}
					options={["left", "right"] as const}
				/>
			</Knob>
			<Knob label="Fill">
				<Choice
					label="Fill"
					value={fill}
					onChange={setFill}
					options={LOGO_FILLS}
				/>
			</Knob>
			<Knob label="Effect">
				<Choice
					label="Effect"
					value={effect}
					onChange={setEffect}
					options={LOGO_EFFECTS}
				/>
			</Knob>
			<Knob label="Ink">
				<Choice label="Ink" value={color} onChange={setColor} options={INKS} />
			</Knob>
			<RangeKnob
				label="Speed"
				value={speed}
				min={8}
				max={120}
				step={4}
				onChange={setSpeed}
			/>
			<Knob label="Pause button">
				<Switch
					aria-label="Pause button"
					checked={pauseButton}
					onCheckedChange={setPauseButton}
				/>
			</Knob>
		</GallerySection>
	);
}

/** Every image component with a few knobs. Mounted by the dev gallery route. */
export function ImagesGallery({ className }: { className?: string }) {
	return (
		<div className={cn("flex flex-col gap-10", className)}>
			<DitherImageDemo />
			<ImageCompareDemo />
			<ScrollRevealDemo />
			<DitheredLogoDemo />
			<LogoMarqueeDemo />
		</div>
	);
}
