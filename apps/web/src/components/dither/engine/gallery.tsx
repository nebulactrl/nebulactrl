import {
	DITHER_MATRICES,
	type DitherMatrix,
} from "@nebulactrl/dither/lib/bayer";
import {
	type DrawMethod,
	drawDitherCanvas,
	rasterizeField,
	type ToneField,
} from "@nebulactrl/dither/lib/canvas";
import { DITHER_COLOR_NAMES } from "@nebulactrl/dither/lib/colors";
import { DIFFUSION_METHODS } from "@nebulactrl/dither/lib/diffuse";
import {
	canvasToBitmap,
	download,
	exportSVG,
	MAX_PNG_SIDE,
	toCSS,
	toPNG,
	toSVG,
} from "@nebulactrl/dither/lib/export";
import { DitherShader } from "@nebulactrl/dither/lib/gl";
import {
	contextBudget,
	MAX_LIVE_CONTEXTS,
} from "@nebulactrl/dither/lib/gl/context-budget";
import { crispCellSizes, snapCell } from "@nebulactrl/dither/lib/gl/layout";
import { MOTION } from "@nebulactrl/dither/lib/motion";
import { DitherPattern } from "@nebulactrl/dither/lib/pattern";
import { toRGB, useDitherPalette } from "@nebulactrl/dither/lib/tokens";
import { type RefObject, useEffect, useMemo, useRef, useState } from "react";
import { Button } from "#/components/ui/button";
import { Segmented } from "#/components/ui/segmented";
import { Switch } from "#/components/ui/switch";
import { GallerySection, Knob, RangeKnob } from "../text/gallery-parts";
import {
	DRIFT_GLSL,
	GLOW_GLSL,
	glowTone,
	RAMP_GLSL,
	RIPPLES_GLSL,
	SPECTRUM_COLOR_GLSL,
	SPECTRUM_FIELD_GLSL,
	sphereTone,
} from "./demo-fields";

const matrixOptions = DITHER_MATRICES.map((value) => ({
	value,
	label: value,
}));

/** A canvas-2D dither that redraws when its box or inputs change. */
function CanvasDither({
	field,
	cell,
	method,
	levels,
	palette,
	canvasRef,
	className,
}: {
	field: ToneField;
	cell: number;
	method: DrawMethod;
	levels?: number;
	palette?: readonly string[];
	canvasRef?: RefObject<HTMLCanvasElement | null>;
	className?: string;
}) {
	const box = useRef<HTMLDivElement>(null);
	const own = useRef<HTMLCanvasElement>(null);
	const target = canvasRef ?? own;
	const paletteKey = palette?.join("|");
	// biome-ignore lint/correctness/useExhaustiveDependencies: paletteKey stands for palette.
	useEffect(() => {
		const host = box.current;
		const canvas = target.current;
		if (!host || !canvas) return;
		const ctx = canvas.getContext("2d");
		if (!ctx) return;
		const draw = () =>
			drawDitherCanvas(ctx, field, {
				width: host.clientWidth,
				height: host.clientHeight,
				cell,
				method,
				...(levels ? { levels } : {}),
				...(palette ? { palette } : {}),
			});
		draw();
		const observer = new ResizeObserver(draw);
		observer.observe(host);
		return () => observer.disconnect();
	}, [field, cell, method, levels, paletteKey, target]);
	return (
		<div ref={box} className={className ?? "absolute inset-0"}>
			<canvas ref={target} className="absolute top-0 left-0 block" />
		</div>
	);
}

function Swatch({ name, hex }: { name: string; hex?: string | undefined }) {
	return (
		<div className="flex flex-col gap-1">
			<div
				className="h-10 rounded-ctl border border-line"
				style={{ background: `var(--dither-${name})` }}
			/>
			<span className="font-mono text-2xs text-ink-2">{name}</span>
			{hex ? (
				<span className="font-mono text-2xs text-ink-4 tabular-nums">
					{hex}
				</span>
			) : null}
		</div>
	);
}

type PaletteChoice = "two" | "named" | "four" | "literal" | "spectrum";
const PALETTE_CHOICES: Record<PaletteChoice, readonly string[]> = {
	two: ["paper", "ink"],
	named: ["paper", "blue"],
	four: ["paper", "blue", "purple", "pink"],
	literal: ["transparent", "oklch(0.72 0.15 272)"],
	spectrum: ["paper", "ink"],
};

function PalettesSection() {
	const palette = useDitherPalette();
	const [choice, setChoice] = useState<PaletteChoice>("four");
	const [matrix, setMatrix] = useState<DitherMatrix>("bayer4");
	const spectrum = choice === "spectrum";
	return (
		<GallerySection
			title="Palettes"
			description="Colours come from tokens: the base ink and paper, and the named blue … grey, which map onto our status and accent tokens. A palette is a ramp from paper to ink; a colorField gives every dot its own hue."
			stage={
				<div className="flex w-full flex-col gap-5">
					<div className="grid grid-cols-5 gap-2 sm:grid-cols-9">
						<Swatch name="paper" hex={palette.paper} />
						<Swatch name="ink" hex={palette.ink} />
						{DITHER_COLOR_NAMES.map((name) => (
							<Swatch key={name} name={name} hex={palette.named[name]} />
						))}
					</div>
					<div className="relative h-32 overflow-hidden rounded-panel border border-line">
						<DitherShader
							field={spectrum ? SPECTRUM_FIELD_GLSL : RAMP_GLSL}
							{...(spectrum ? { colorField: SPECTRUM_COLOR_GLSL } : {})}
							palette={PALETTE_CHOICES[choice]}
							matrix={matrix}
							staticTime={3}
							className="absolute inset-0"
						/>
					</div>
				</div>
			}
		>
			<Knob label="palette">
				<Segmented<PaletteChoice>
					aria-label="Palette"
					font="sans"
					value={choice}
					onValueChange={setChoice}
					options={[
						{ value: "two", label: "2" },
						{ value: "named", label: "named" },
						{ value: "four", label: "4" },
						{ value: "literal", label: "oklch" },
						{ value: "spectrum", label: "hues" },
					]}
				/>
			</Knob>
			<Knob label="matrix">
				<Segmented<DitherMatrix>
					aria-label="Matrix"
					value={matrix}
					onValueChange={setMatrix}
					options={matrixOptions}
				/>
			</Knob>
			<p className="text-ink-3 text-xs">
				<code className="font-mono">
					palette={"{"}[
					{PALETTE_CHOICES[choice].map((c) => `"${c}"`).join(", ")}]{"}"}
				</code>
				{spectrum ? " with a colorField using spectrum()" : ""}
			</p>
		</GallerySection>
	);
}

function MatricesSection() {
	const [levels, setLevels] = useState(6);
	return (
		<GallerySection
			title="Matrices and levels"
			description="Every threshold matrix draws the same ramp. Bayer tiles read as print; blue noise has no visible grid. Fewer levels give visible bands."
			stage={
				<div className="flex w-full flex-col gap-3">
					{DITHER_MATRICES.map((matrix) => (
						<figure key={matrix} className="flex flex-col gap-1">
							<div className="relative h-12 overflow-hidden rounded-ctl">
								<DitherShader
									field={RAMP_GLSL}
									matrix={matrix}
									levels={levels}
									animate={false}
									className="absolute inset-0"
								/>
							</div>
							<figcaption className="font-mono text-2xs text-ink-3">
								{matrix}
							</figcaption>
						</figure>
					))}
				</div>
			}
		>
			<RangeKnob
				label="levels"
				value={levels}
				min={2}
				max={17}
				step={1}
				onChange={setLevels}
			/>
		</GallerySection>
	);
}

function PixelScaleSection() {
	const [cell, setCell] = useState(3);
	const [dpr, setDpr] = useState(1);
	useEffect(() => {
		setDpr(window.devicePixelRatio || 1);
	}, []);
	const snapped = useMemo(() => snapCell(cell, dpr), [cell, dpr]);
	return (
		<GallerySection
			title="Pixel scale"
			description="One cell is a whole number of device pixels, so neighbours never alternate between two sizes and the grid stays crisp at any DPR."
			stage={
				<div className="relative h-56 w-full overflow-hidden rounded-panel border border-line">
					<DitherShader
						field={GLOW_GLSL}
						cell={cell}
						animate={false}
						className="absolute inset-0"
					/>
				</div>
			}
		>
			<RangeKnob
				label="cell (css px)"
				value={cell}
				min={1}
				max={12}
				step={0.5}
				onChange={setCell}
			/>
			<dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 font-mono text-ink-2 text-xs tabular-nums">
				<dt className="text-ink-3">device ratio</dt>
				<dd>{dpr}</dd>
				<dt className="text-ink-3">snapped</dt>
				<dd>
					{snapped.cellDev} device px = {snapped.cellCss.toFixed(2)} css px
				</dd>
				<dt className="text-ink-3">crisp sizes</dt>
				<dd>{crispCellSizes(dpr, 8).join(", ") || "none"}</dd>
			</dl>
			<p className="text-ink-3 text-xs">
				<code className="font-mono">--dither-cell</code> sets the default for a
				whole section; the <code className="font-mono">cell</code> prop wins.
			</p>
		</GallerySection>
	);
}

const PATTERN_LEVELS = [0.125, 0.25, 0.5, 0.75, 1] as const;

function DrawPathsSection() {
	const [cell, setCell] = useState(3);
	const [matrix, setMatrix] = useState<DitherMatrix>("bayer4");
	return (
		<GallerySection
			title="Three ways to draw"
			description="The same glow, drawn by WebGL (animated or full-bleed), by canvas 2D (drawDitherCanvas, for exports and workers) and as an SVG pattern (DitherPattern, for charts and blocks)."
			stageClassName="p-4"
			stage={
				<div className="grid w-full gap-3 sm:grid-cols-3">
					<figure className="flex flex-col gap-1.5">
						<div className="relative h-40 overflow-hidden rounded-ctl border border-line">
							<DitherShader
								field={GLOW_GLSL}
								cell={cell}
								matrix={matrix}
								animate={false}
								className="absolute inset-0"
							/>
						</div>
						<figcaption className="font-mono text-2xs text-ink-3">
							DitherShader · WebGL
						</figcaption>
					</figure>
					<figure className="flex flex-col gap-1.5">
						<div className="relative h-40 overflow-hidden rounded-ctl border border-line">
							<CanvasDither field={glowTone} cell={cell} method={matrix} />
						</div>
						<figcaption className="font-mono text-2xs text-ink-3">
							drawDitherCanvas · canvas 2D
						</figcaption>
					</figure>
					<figure className="flex flex-col gap-1.5">
						<svg
							viewBox="0 0 200 160"
							className="h-40 w-full rounded-ctl border border-line"
							role="img"
							aria-label="Five bars filled at 12, 25, 50, 75 and 100 percent ink"
						>
							<defs>
								{PATTERN_LEVELS.map((level) => (
									<DitherPattern
										key={level}
										id={`engine-bar-${level}`}
										level={level}
										cell={cell}
										matrix={matrix}
										color="ink"
									/>
								))}
							</defs>
							{PATTERN_LEVELS.map((level, index) => (
								<rect
									key={level}
									x={index * 40 + 4}
									y={160 - level * 150}
									width={32}
									height={level * 150}
									fill={`url(#engine-bar-${level})`}
								/>
							))}
						</svg>
						<figcaption className="font-mono text-2xs text-ink-3">
							DitherPattern · SVG
						</figcaption>
					</figure>
				</div>
			}
		>
			<RangeKnob
				label="cell"
				value={cell}
				min={1}
				max={8}
				step={1}
				onChange={setCell}
			/>
			<Knob label="matrix">
				<Segmented<DitherMatrix>
					aria-label="Matrix"
					value={matrix}
					onValueChange={setMatrix}
					options={matrixOptions}
				/>
			</Knob>
		</GallerySection>
	);
}

type DiffusionChoice = DitherMatrix | (typeof DIFFUSION_METHODS)[number];
const DIFFUSION_CHOICES: readonly DiffusionChoice[] = [
	"bayer4",
	"blue-noise",
	...DIFFUSION_METHODS,
];

const SIZE = 96;

function ToneCanvas({
	method,
	levels,
}: {
	method: DiffusionChoice;
	levels: number;
}) {
	const ref = useRef<HTMLCanvasElement>(null);
	const palette = useDitherPalette();
	useEffect(() => {
		const canvas = ref.current;
		const ctx = canvas?.getContext("2d");
		if (!canvas || !ctx) return;
		const raster = rasterizeField(sphereTone, {
			cols: SIZE,
			rows: SIZE,
			method,
			levels,
			palette: [toRGB(palette.paper), toRGB(palette.ink)],
		});
		canvas.width = raster.width;
		canvas.height = raster.height;
		ctx.putImageData(
			new ImageData(raster.data, raster.width, raster.height),
			0,
			0,
		);
	}, [method, levels, palette]);
	return (
		<canvas
			ref={ref}
			className="aspect-square w-full rounded-ctl border border-line [image-rendering:pixelated]"
			role="img"
			aria-label={`A lit sphere dithered with ${method}`}
		/>
	);
}

function DiffusionSection() {
	const [levels, setLevels] = useState(2);
	return (
		<GallerySection
			title="Error diffusion"
			description="Ordered matrices decide each cell alone and animate calmly. Diffusion rounds each cell and passes the error on, so it suits stills. diffuseTones() works on any tone grid, not only photos."
			stageClassName="p-4"
			stage={
				<div className="grid w-full grid-cols-2 gap-3 sm:grid-cols-5">
					{DIFFUSION_CHOICES.map((method) => (
						<figure key={method} className="flex flex-col gap-1.5">
							<ToneCanvas method={method} levels={levels} />
							<figcaption className="font-mono text-2xs text-ink-3">
								{method}
							</figcaption>
						</figure>
					))}
				</div>
			}
		>
			<RangeKnob
				label="levels"
				value={levels}
				min={2}
				max={8}
				step={1}
				onChange={setLevels}
			/>
		</GallerySection>
	);
}

function MotionSection() {
	const [animate, setAnimate] = useState(true);
	const [speed, setSpeed] = useState(1);
	const [fps, setFps] = useState(24);
	const [stillTime, setStillTime] = useState(4);
	const [flood, setFlood] = useState(false);
	const host = useRef<HTMLDivElement>(null);
	const [state, setState] = useState("fallback");
	const [budget, setBudget] = useState({ live: 0, waiting: 0 });

	useEffect(() => {
		const el = host.current?.querySelector<HTMLElement>(
			"[data-slot=dither-shader]",
		);
		if (!el) return;
		const read = () => setState(el.dataset.state ?? "fallback");
		read();
		const observer = new MutationObserver(read);
		observer.observe(el, { attributes: true, attributeFilter: ["data-state"] });
		return () => observer.disconnect();
	}, []);

	useEffect(() => {
		const read = () =>
			setBudget({ live: contextBudget.live, waiting: contextBudget.waiting });
		read();
		const id = window.setInterval(read, 400);
		return () => window.clearInterval(id);
	}, []);

	return (
		<GallerySection
			title="Motion and performance"
			description="Dithers drift, not perform: capped at 30 fps, paused off-screen and in hidden tabs, one still frame under reduced motion. At most eight WebGL contexts live at once; the rest show their fallback until a slot frees."
			stage={
				<div className="flex w-full flex-col gap-3">
					<div
						ref={host}
						data-testid="dither-motion-host"
						className="relative h-44 overflow-hidden rounded-panel border border-line"
					>
						<DitherShader
							field={DRIFT_GLSL}
							animate={animate}
							speed={speed}
							fps={fps}
							stillTime={stillTime}
							className="absolute inset-0"
						/>
					</div>
					{flood ? (
						<div className="grid grid-cols-6 gap-1.5">
							{Array.from({ length: 12 }, (_, i) => (
								<div
									// biome-ignore lint/suspicious/noArrayIndexKey: fixed list of identical demo tiles
									key={i}
									className="relative h-12 overflow-hidden rounded-ctl border border-line"
								>
									<DitherShader
										field={RAMP_GLSL}
										animate={false}
										className="absolute inset-0"
									/>
								</div>
							))}
						</div>
					) : null}
				</div>
			}
		>
			<Knob label="animate">
				<Switch
					aria-label="Animate"
					checked={animate}
					onCheckedChange={setAnimate}
				/>
			</Knob>
			<RangeKnob
				label="speed"
				value={speed}
				min={0}
				max={3}
				step={0.1}
				onChange={setSpeed}
			/>
			<RangeKnob
				label="fps (max 30)"
				value={fps}
				min={1}
				max={30}
				step={1}
				onChange={setFps}
			/>
			<RangeKnob
				label="stillTime (s)"
				value={stillTime}
				min={0}
				max={20}
				step={0.5}
				onChange={setStillTime}
			/>
			<Knob label="mount twelve more shaders">
				<Switch
					aria-label="Mount twelve shaders"
					checked={flood}
					onCheckedChange={setFlood}
				/>
			</Knob>
			<dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 font-mono text-ink-2 text-xs tabular-nums">
				<dt className="text-ink-3">data-state</dt>
				<dd>{state}</dd>
				<dt className="text-ink-3">contexts</dt>
				<dd data-testid="dither-contexts">
					{budget.live} live / {MAX_LIVE_CONTEXTS} max, {budget.waiting} waiting
				</dd>
				<dt className="text-ink-3">MOTION</dt>
				<dd>
					{MOTION.fast} · {MOTION.base} · {MOTION.slow}
				</dd>
			</dl>
		</GallerySection>
	);
}

function ExportSection() {
	const canvasRef = useRef<HTMLCanvasElement>(null);
	const [cell, setCell] = useState(4);
	const [level, setLevel] = useState(0.25);
	const [css, setCss] = useState("");
	const [svgBytes, setSvgBytes] = useState<number | null>(null);

	const rule = useMemo(
		() =>
			toCSS({
				level,
				cell,
				mode: "mask",
				ink: "--dither-ink",
				selector: ".dither",
			}),
		[level, cell],
	);
	const shortRule = rule.replace(
		/url\("data:[^"]+"\)/g,
		'url("data:image/svg+xml,…")',
	);

	const exportPng = async () => {
		const canvas = canvasRef.current;
		if (!canvas) return;
		download({
			data: await toPNG(canvas, { scale: cell }),
			filename: "dither.png",
		});
	};
	const exportSvg = () => {
		const canvas = canvasRef.current;
		if (!canvas) return;
		const svg = toSVG(canvasToBitmap(canvas), { cell });
		setSvgBytes(svg.length);
		download({ data: svg, filename: "dither.svg", type: "image/svg+xml" });
	};
	const exportPattern = () => {
		const svg = exportSVG(
			{
				kind: "pattern",
				fill: { tone: level, color: "#9aa4ff", cell },
				width: 320,
				height: 160,
			},
			{ title: `Dither at ${Math.round(level * 100)} percent` },
		);
		download({ data: svg, filename: "pattern.svg", type: "image/svg+xml" });
	};
	const exportCss = () => {
		setCss(rule);
		download({ data: rule, filename: "dither.css", type: "text/css" });
	};

	return (
		<GallerySection
			title="Export"
			description="Any dither can leave as a PNG (pixel-exact, capped at 16,384 px), a compact SVG (cells as short strokes) or a CSS rule (one tile, repeated and masked). Strings run on the server; PNG needs a canvas."
			stage={
				<div className="flex w-full flex-col gap-3">
					<div className="relative h-40 overflow-hidden rounded-panel border border-line">
						<CanvasDither
							field={glowTone}
							cell={cell}
							method="bayer4"
							canvasRef={canvasRef}
						/>
					</div>
					<pre className="max-h-40 overflow-auto whitespace-pre-wrap break-all rounded-ctl bg-surface-well p-3 font-mono text-2xs text-ink-2">
						{css ? shortRule : "Press “CSS rule” to see the tile."}
					</pre>
				</div>
			}
		>
			<RangeKnob
				label="cell"
				value={cell}
				min={1}
				max={8}
				step={1}
				onChange={setCell}
			/>
			<RangeKnob
				label="tile level"
				value={level}
				min={0}
				max={1}
				step={0.0625}
				onChange={setLevel}
			/>
			<div className="flex flex-wrap gap-2">
				<Button variant="secondary" size="md" onClick={exportPng}>
					PNG
				</Button>
				<Button variant="secondary" size="md" onClick={exportSvg}>
					SVG
				</Button>
				<Button variant="secondary" size="md" onClick={exportPattern}>
					Pattern SVG
				</Button>
				<Button variant="secondary" size="md" onClick={exportCss}>
					CSS rule
				</Button>
			</div>
			<p className="text-ink-3 text-xs">
				PNG longest side limit: {MAX_PNG_SIDE.toLocaleString()} px.
				{svgBytes !== null
					? ` Last SVG: ${(svgBytes / 1024).toFixed(1)} KB.`
					: ""}
			</p>
		</GallerySection>
	);
}

function WriteYourOwnSection() {
	const [rings, setRings] = useState(46);
	const [strength, setStrength] = useState(0.85);
	const [dropX, setDropX] = useState(0.5);
	const [dropY, setDropY] = useState(0.42);
	return (
		<GallerySection
			title="Write your own field"
			description="One GLSL function returns a tone from 0 to 1; DitherShader does the rest. Uniforms become props without a recompile. Both float field(uv, t) and float field(uv, res, t) are accepted. See WRITE-YOUR-OWN.md."
			stage={
				<div className="relative h-56 w-full overflow-hidden rounded-panel border border-line">
					<DitherShader
						field={RIPPLES_GLSL}
						palette={["paper", "blue"]}
						matrix="bayer8"
						stillTime={4}
						uniforms={{
							demoDrop: [dropX, dropY],
							demoRings: rings,
							demoStrength: strength,
						}}
						className="absolute inset-0"
					/>
				</div>
			}
		>
			<RangeKnob
				label="rings"
				value={rings}
				min={10}
				max={90}
				step={1}
				onChange={setRings}
			/>
			<RangeKnob
				label="strength"
				value={strength}
				min={0.1}
				max={1}
				step={0.05}
				onChange={setStrength}
			/>
			<RangeKnob
				label="drop x"
				value={dropX}
				min={0}
				max={1}
				step={0.05}
				onChange={setDropX}
			/>
			<RangeKnob
				label="drop y"
				value={dropY}
				min={0}
				max={1}
				step={0.05}
				onChange={setDropY}
			/>
		</GallerySection>
	);
}

/** The engine pages of the docs, one live section each. */
export function EngineGallery() {
	return (
		<div className="flex flex-col gap-10">
			<PalettesSection />
			<MatricesSection />
			<PixelScaleSection />
			<DrawPathsSection />
			<DiffusionSection />
			<MotionSection />
			<ExportSection />
			<WriteYourOwnSection />
		</div>
	);
}
