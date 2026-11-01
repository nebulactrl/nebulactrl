import { DITHER_COLOR_NAMES } from "@nebulactrl/dither/lib/colors";
import type {
	DissolveBy,
	DissolveDirection,
} from "@nebulactrl/dither/text/dissolve";
import {
	type DissolveFill,
	type DissolveMatrix,
	DissolveText,
} from "@nebulactrl/dither/text/dissolve-text";
import {
	DitherNumber,
	type DitherNumberEffect,
	type DitherNumberMatrix,
	type DitherNumberSize,
} from "@nebulactrl/dither/text/dither-number";
import {
	DitherText,
	type DitherTextBloom,
} from "@nebulactrl/dither/text/dither-text";
import type { TextFill } from "@nebulactrl/dither/text/fills";
import type { ScrambleMode } from "@nebulactrl/dither/text/scramble";
import {
	SCRAMBLE_HEX,
	SCRAMBLE_MONO,
	ScrambleText,
	type ScrambleTrigger,
} from "@nebulactrl/dither/text/scramble-text";
import { cn } from "cn";
import { useState } from "react";
import { Button } from "#/components/ui/button";
import { Input } from "#/components/ui/input";
import { Segmented } from "#/components/ui/segmented";
import { Switch } from "#/components/ui/switch";
import { GallerySection, Knob, RangeKnob } from "./gallery-parts";

const COLORS = ["ink", ...DITHER_COLOR_NAMES] as const;
type ColorName = (typeof COLORS)[number];
const colorOptions = COLORS.map((v) => ({ value: v, label: v }));

type TextMatrix = "bayer2" | "bayer4" | "bayer8" | "blue-noise";
const MATRICES: readonly TextMatrix[] = [
	"bayer2",
	"bayer4",
	"bayer8",
	"blue-noise",
];

type DirectionName = "down" | "up" | "left" | "right" | "45°";

type PaletteName = "off" | "blue-purple" | "orange-red" | "green-ink";
const PALETTES = {
	"blue-purple": ["transparent", "blue", "purple"],
	"orange-red": ["transparent", "orange", "red"],
	"green-ink": ["transparent", "green", "ink"],
} as const;

function DitherTextDemo() {
	const [text, setText] = useState("Dither\nEverything");
	const [fill, setFill] = useState<TextFill>("gradient");
	const [direction, setDirection] = useState<DirectionName>("down");
	const [color, setColor] = useState<ColorName>("ink");
	const [palette, setPalette] = useState<PaletteName>("off");
	const [matrix, setMatrix] = useState<TextMatrix>("bayer4");
	const [cell, setCell] = useState(3);
	const [scale, setScale] = useState(3);
	const [from, setFrom] = useState(1);
	const [to, setTo] = useState(0.4);
	const [speed, setSpeed] = useState(1);
	const [bloom, setBloom] = useState<DitherTextBloom>("off");
	const [animate, setAnimate] = useState(true);
	return (
		<GallerySection
			title="DitherText"
			description="A display headline whose letters are filled with a dither. The text stays real: select it, copy it, read it."
			stage={
				<DitherText
					as="h2"
					fill={fill}
					direction={direction === "45°" ? 45 : direction}
					color={color}
					palette={palette === "off" ? undefined : PALETTES[palette]}
					matrix={matrix}
					cell={cell}
					scale={scale}
					from={from}
					to={to}
					speed={speed}
					bloom={bloom}
					animate={animate}
					className="text-center font-semibold text-7xl leading-[0.95] tracking-tight"
				>
					{text}
				</DitherText>
			}
		>
			<Knob label="Text">
				<Input
					aria-label="Text"
					value={text.replace(/\n/g, "↵")}
					onChange={(event) => setText(event.target.value.replace(/↵/g, "\n"))}
				/>
			</Knob>
			<Knob label="Fill">
				<Segmented<TextFill>
					aria-label="Fill"
					value={fill}
					onValueChange={setFill}
					options={(
						["gradient", "noise", "dotted", "hatched", "solid"] as const
					).map((v) => ({ value: v, label: v }))}
				/>
			</Knob>
			<Knob label="Direction">
				<Segmented<DirectionName>
					aria-label="Direction"
					value={direction}
					onValueChange={setDirection}
					options={(["down", "up", "left", "right", "45°"] as const).map(
						(v) => ({ value: v, label: v }),
					)}
				/>
			</Knob>
			<Knob label="Colour">
				<Segmented<ColorName>
					aria-label="Colour"
					value={color}
					onValueChange={setColor}
					options={colorOptions}
				/>
			</Knob>
			<Knob label="Palette">
				<Segmented<PaletteName>
					aria-label="Palette"
					value={palette}
					onValueChange={setPalette}
					options={(
						["off", "blue-purple", "orange-red", "green-ink"] as const
					).map((v) => ({ value: v, label: v }))}
				/>
			</Knob>
			<Knob label="Matrix">
				<Segmented<TextMatrix>
					aria-label="Matrix"
					value={matrix}
					onValueChange={setMatrix}
					options={MATRICES.map((v) => ({ value: v, label: v }))}
				/>
			</Knob>
			<Knob label="Bloom">
				<Segmented<DitherTextBloom>
					aria-label="Bloom"
					value={bloom}
					onValueChange={setBloom}
					options={(["off", "low", "high"] as const).map((v) => ({
						value: v,
						label: v,
					}))}
				/>
			</Knob>
			<RangeKnob
				label="Cell"
				value={cell}
				min={2}
				max={6}
				step={1}
				onChange={setCell}
			/>
			<RangeKnob
				label="Noise scale"
				value={scale}
				min={1}
				max={8}
				step={0.5}
				onChange={setScale}
			/>
			<RangeKnob
				label="From"
				value={from}
				min={0}
				max={1}
				step={0.05}
				onChange={setFrom}
			/>
			<RangeKnob
				label="To"
				value={to}
				min={0}
				max={1}
				step={0.05}
				onChange={setTo}
			/>
			<RangeKnob
				label="Speed"
				value={speed}
				min={0}
				max={4}
				step={0.25}
				onChange={setSpeed}
			/>
			<Knob label="Drift">
				<Switch
					aria-label="Drift"
					checked={animate}
					onCheckedChange={setAnimate}
				/>
			</Knob>
		</GallerySection>
	);
}

function DissolveTextDemo() {
	const [by, setBy] = useState<DissolveBy>("char");
	const [direction, setDirection] = useState<DissolveDirection>("start");
	const [fill, setFill] = useState<DissolveFill>("solid");
	const [matrix, setMatrix] = useState<DissolveMatrix>("bayer4");
	const [color, setColor] = useState<ColorName>("ink");
	const [accent, setAccent] = useState<ColorName | "none">("ink");
	const [speed, setSpeed] = useState(1);
	const [show, setShow] = useState(true);
	const [run, setRun] = useState(0);
	return (
		<GallerySection
			title="DissolveText"
			description="Text that dissolves in through Bayer thresholds by character, word or line, and back out."
			stage={
				<DissolveText
					as="h2"
					by={by}
					direction={direction}
					fill={fill}
					matrix={matrix}
					color={color}
					accent={accent}
					speed={speed}
					show={show}
					replayKey={run}
					trigger="mount"
					className="max-w-xl text-center font-semibold text-4xl tracking-tight"
				>
					{"Arrives one dot\nat a time"}
				</DissolveText>
			}
		>
			<Knob label="By">
				<Segmented<DissolveBy>
					aria-label="Split by"
					value={by}
					onValueChange={setBy}
					options={(["char", "word", "line"] as const).map((v) => ({
						value: v,
						label: v,
					}))}
				/>
			</Knob>
			<Knob label="Order">
				<Segmented<DissolveDirection>
					aria-label="Order"
					value={direction}
					onValueChange={setDirection}
					options={(["start", "end", "center", "edges", "random"] as const).map(
						(v) => ({ value: v, label: v }),
					)}
				/>
			</Knob>
			<Knob label="Settles into">
				<Segmented<DissolveFill>
					aria-label="Settles into"
					value={fill}
					onValueChange={setFill}
					options={(["solid", "gradient", "dotted", "hatched"] as const).map(
						(v) => ({ value: v, label: v }),
					)}
				/>
			</Knob>
			<Knob label="Matrix">
				<Segmented<DissolveMatrix>
					aria-label="Matrix"
					value={matrix}
					onValueChange={setMatrix}
					options={(["bayer2", "bayer4", "bayer8"] as const).map((v) => ({
						value: v,
						label: v,
					}))}
				/>
			</Knob>
			<Knob label="Colour">
				<Segmented<ColorName>
					aria-label="Colour"
					value={color}
					onValueChange={setColor}
					options={colorOptions}
				/>
			</Knob>
			<Knob label="Accent">
				<Segmented<ColorName | "none">
					aria-label="Accent"
					value={accent}
					onValueChange={setAccent}
					options={[{ value: "none", label: "none" }, ...colorOptions]}
				/>
			</Knob>
			<RangeKnob
				label="Speed"
				value={speed}
				min={0.25}
				max={4}
				step={0.25}
				onChange={setSpeed}
			/>
			<Knob label="Show">
				<Switch aria-label="Show" checked={show} onCheckedChange={setShow} />
			</Knob>
			<Button
				variant="secondary"
				size="md"
				onClick={() => setRun((n) => n + 1)}
			>
				Replay
			</Button>
		</GallerySection>
	);
}

type CharacterSet = "text" | "mono" | "hex";
const CHARACTER_SETS: Record<CharacterSet, string | undefined> = {
	text: undefined,
	mono: SCRAMBLE_MONO,
	hex: SCRAMBLE_HEX,
};
type Caret = "none" | "running" | "always";

function ScrambleTextDemo() {
	const phrases = [
		"Every frame accounted for",
		"Deploys land in seconds",
		"Rollbacks never touch data",
	];
	const [index, setIndex] = useState(0);
	const [mode, setMode] = useState<ScrambleMode>("dither");
	const [trigger, setTrigger] = useState<ScrambleTrigger>("hover");
	const [color, setColor] = useState<ColorName>("ink");
	const [characters, setCharacters] = useState<CharacterSet>("text");
	const [caret, setCaret] = useState<Caret>("none");
	const [speed, setSpeed] = useState(1);
	const [run, setRun] = useState(0);
	const hint: Record<ScrambleTrigger, string> = {
		hover: "hover or focus the phrase",
		mount: "click to replay",
		"in-view": "click to replay",
		view: "click to replay",
		change: "press Next phrase",
	};
	return (
		<GallerySection
			title="ScrambleText"
			description="Text that resolves character by character through dithered blocks and glyphs, with no layout shift. Change the phrase and only the letters that differ move."
			stage={
				<div className="flex flex-col items-center gap-4">
					<button
						type="button"
						className="rounded-ctl px-3 py-1.5 outline-none focus-visible:focus-ring"
						onClick={() => setRun((n) => n + 1)}
					>
						<ScrambleText
							key={`${trigger}-${mode}-${caret}-${characters}-${run}`}
							as="span"
							mode={mode}
							trigger={trigger}
							color={color}
							characters={CHARACTER_SETS[characters]}
							caret={caret}
							speed={speed}
							aria-live="polite"
							className="font-semibold text-3xl tracking-tight"
						>
							{phrases[index] ?? ""}
						</ScrambleText>
					</button>
					<p className="font-mono text-ink-3 text-xs">{hint[trigger]}</p>
				</div>
			}
		>
			<Knob label="Mode">
				<Segmented<ScrambleMode>
					aria-label="Mode"
					value={mode}
					onValueChange={setMode}
					options={(["dither", "blocks", "glyphs"] as const).map((v) => ({
						value: v,
						label: v,
					}))}
				/>
			</Knob>
			<Knob label="Trigger">
				<Segmented<ScrambleTrigger>
					aria-label="Trigger"
					value={trigger}
					onValueChange={setTrigger}
					options={(["hover", "mount", "in-view", "change"] as const).map(
						(v) => ({ value: v, label: v }),
					)}
				/>
			</Knob>
			<Knob label="Characters">
				<Segmented<CharacterSet>
					aria-label="Characters"
					value={characters}
					onValueChange={setCharacters}
					options={(["text", "mono", "hex"] as const).map((v) => ({
						value: v,
						label: v,
					}))}
				/>
			</Knob>
			<Knob label="Caret">
				<Segmented<Caret>
					aria-label="Caret"
					value={caret}
					onValueChange={setCaret}
					options={(["none", "running", "always"] as const).map((v) => ({
						value: v,
						label: v,
					}))}
				/>
			</Knob>
			<Knob label="Colour">
				<Segmented<ColorName>
					aria-label="Colour"
					value={color}
					onValueChange={setColor}
					options={colorOptions}
				/>
			</Knob>
			<RangeKnob
				label="Speed"
				value={speed}
				min={0.25}
				max={4}
				step={0.25}
				onChange={setSpeed}
			/>
			<Button
				variant="secondary"
				size="md"
				onClick={() => setIndex((i) => (i + 1) % phrases.length)}
			>
				Next phrase
			</Button>
		</GallerySection>
	);
}

type NumberFormatName = "plain" | "currency" | "percent" | "compact";

const FORMATS: Record<NumberFormatName, Intl.NumberFormatOptions> = {
	plain: {},
	currency: { style: "currency", currency: "USD", maximumFractionDigits: 0 },
	percent: {
		style: "percent",
		minimumFractionDigits: 1,
		maximumFractionDigits: 1,
	},
	compact: { notation: "compact", maximumFractionDigits: 1 },
};

function DitherNumberDemo() {
	const [value, setValue] = useState(128406);
	const [effect, setEffect] = useState<DitherNumberEffect>("roll");
	const [format, setFormat] = useState<NumberFormatName>("plain");
	const [size, setSize] = useState<DitherNumberSize>("lg");
	const [matrix, setMatrix] = useState<DitherNumberMatrix>("bayer4");
	const [color, setColor] = useState<ColorName>("ink");
	const [accent, setAccent] = useState<ColorName | "none">("ink");
	const [unit, setUnit] = useState(false);
	const shown = format === "percent" ? (value % 1000) / 1000 : value;
	return (
		<GallerySection
			title="DitherNumber"
			description="An animated number for KPI tiles. Tabular digits; only the digits that change move, left to right. Static under reduced motion."
			stage={
				<div className="flex flex-col items-center gap-2">
					<DitherNumber
						key={`${effect}-${format}-${size}-${matrix}`}
						value={shown}
						format={FORMATS[format]}
						locales="en-US"
						effect={effect}
						size={size}
						matrix={matrix}
						color={color === "ink" ? undefined : color}
						accent={accent}
						prefix={unit ? "~" : undefined}
						suffix={unit ? " ms" : undefined}
						affixClassName="font-normal text-[0.5em] text-ink-3"
						className={cn("font-semibold text-ink-1")}
					/>
					<p className="font-mono text-ink-3 text-xs">p95 requests per hour</p>
				</div>
			}
		>
			<Knob label="Effect">
				<Segmented<DitherNumberEffect>
					aria-label="Effect"
					value={effect}
					onValueChange={setEffect}
					options={(["roll", "resolve", "tick"] as const).map((v) => ({
						value: v,
						label: v,
					}))}
				/>
			</Knob>
			<Knob label="Format">
				<Segmented<NumberFormatName>
					aria-label="Format"
					value={format}
					onValueChange={setFormat}
					options={(["plain", "currency", "percent", "compact"] as const).map(
						(v) => ({ value: v, label: v }),
					)}
				/>
			</Knob>
			<Knob label="Size">
				<Segmented<DitherNumberSize>
					aria-label="Size"
					value={size}
					onValueChange={setSize}
					options={(["sm", "md", "lg", "xl", "display"] as const).map((v) => ({
						value: v,
						label: v,
					}))}
				/>
			</Knob>
			<Knob label="Matrix">
				<Segmented<DitherNumberMatrix>
					aria-label="Matrix"
					value={matrix}
					onValueChange={setMatrix}
					options={(["bayer2", "bayer4", "bayer8"] as const).map((v) => ({
						value: v,
						label: v,
					}))}
				/>
			</Knob>
			<Knob label="Colour">
				<Segmented<ColorName>
					aria-label="Colour"
					value={color}
					onValueChange={setColor}
					options={colorOptions}
				/>
			</Knob>
			<Knob label="Accent">
				<Segmented<ColorName | "none">
					aria-label="Accent"
					value={accent}
					onValueChange={setAccent}
					options={[{ value: "none", label: "none" }, ...colorOptions]}
				/>
			</Knob>
			<Knob label="Prefix and suffix">
				<Switch
					aria-label="Prefix and suffix"
					checked={unit}
					onCheckedChange={setUnit}
				/>
			</Knob>
			<div className="flex gap-2">
				<Button
					variant="secondary"
					size="md"
					onClick={() =>
						setValue((v) => v + Math.round(Math.random() * 900) + 50)
					}
				>
					Grow
				</Button>
				<Button
					variant="secondary"
					size="md"
					onClick={() =>
						setValue((v) =>
							Math.max(0, v - Math.round(Math.random() * 900) - 50),
						)
					}
				>
					Shrink
				</Button>
				<Button
					variant="ghost"
					size="md"
					onClick={() => setValue(Math.round(Math.random() * 999999))}
				>
					Random
				</Button>
			</div>
		</GallerySection>
	);
}

/** Every text component with a few knobs. Mounted by the dev gallery route. */
export function TextGallery({ className }: { className?: string }) {
	return (
		<div className={cn("flex flex-col gap-10", className)}>
			<DitherTextDemo />
			<DissolveTextDemo />
			<ScrambleTextDemo />
			<DitherNumberDemo />
		</div>
	);
}
