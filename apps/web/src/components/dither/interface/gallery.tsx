import {
	DitherAvatar,
	DitherAvatarGroup,
	type DitherAvatarStatus,
} from "@nebulactrl/dither/interface/avatar";
import {
	DitherButton,
	type DitherButtonVariant,
} from "@nebulactrl/dither/interface/button";
import {
	DitherCard,
	type DitherCardGlow,
	type DitherCardProps,
} from "@nebulactrl/dither/interface/card";
import {
	DitherDivider,
	type DitherDividerProps,
} from "@nebulactrl/dither/interface/divider";
import {
	DitherEmptyState,
	type EmptyStateIllustration,
} from "@nebulactrl/dither/interface/empty-state";
import {
	type DitherBloom,
	type DitherFillVariant,
	FILL_VARIANTS,
} from "@nebulactrl/dither/interface/fill";
import { DitherLoader } from "@nebulactrl/dither/interface/loader";
import {
	LOADER_SHAPES,
	type LoaderSize,
} from "@nebulactrl/dither/interface/loader-math";
import { DitherNotFound } from "@nebulactrl/dither/interface/not-found";
import {
	DitherSkeleton,
	type DitherSkeletonProps,
} from "@nebulactrl/dither/interface/skeleton";
import { cn } from "cn";
import type * as React from "react";
import { useState } from "react";
import { Input } from "#/components/ui/input";
import { SectionLabel } from "#/components/ui/section-label";
import { Segmented } from "#/components/ui/segmented";
import { Switch } from "#/components/ui/switch";

/** `{ value, label }` options for a `Segmented`, one per value. */
function opts<T extends string>(values: readonly T[]) {
	return values.map((value) => ({ value, label: value }));
}

const VARIANTS = [
	{ value: "primary", label: "primary" },
	{ value: "filled", label: "filled" },
	{ value: "secondary", label: "secondary" },
	{ value: "outline", label: "outline" },
	{ value: "ghost", label: "ghost" },
	{ value: "danger", label: "danger" },
] as const satisfies readonly { value: DitherButtonVariant; label: string }[];

const SIZES = [
	{ value: "sm", label: "sm" },
	{ value: "default", label: "md" },
	{ value: "lg", label: "lg" },
] as const;
type Size = (typeof SIZES)[number]["value"];

const COLORS = [
	"none",
	"blue",
	"purple",
	"green",
	"pink",
	"orange",
	"red",
	"grey",
] as const;
type ColorChoice = (typeof COLORS)[number];
const paint = (choice: ColorChoice) => (choice === "none" ? undefined : choice);

const BLOOMS = ["off", "low", "high"] as const satisfies readonly DitherBloom[];

const FINISHES = [
	{ value: "all", label: "all" },
	{ value: "edge", label: "edge" },
	{ value: "grain", label: "grain" },
	{ value: "bloom", label: "bloom" },
	{ value: "none", label: "none" },
] as const;

const GLOWS = [
	"none",
	"top-left",
	"top-right",
	"bottom-left",
	"bottom-right",
	"top",
	"bottom",
] as const;
type GlowChoice = (typeof GLOWS)[number];

const RULE_VARIANTS = [
	{ value: "fade", label: "fade" },
	{ value: "solid", label: "solid" },
	{ value: "dots", label: "dots" },
] as const;

const RULE_FILLS = ["default", ...FILL_VARIANTS] as const;

const AVATAR_SIZES = [
	{ value: "sm", label: "sm" },
	{ value: "default", label: "md" },
	{ value: "lg", label: "lg" },
] as const;

const AVATAR_SHAPES = ["circle", "square"] as const;
const STATUSES = [
	"none",
	"online",
	"away",
	"busy",
	"offline",
] as const satisfies readonly (DitherAvatarStatus | "none")[];

const COUNTS = [
	{ value: "2", label: "2" },
	{ value: "4", label: "4" },
	{ value: "7", label: "7" },
	{ value: "12", label: "12" },
] as const;

const SKELETON_SHAPES = ["block", "text", "circle"] as const;
const SKELETON_ANIMATIONS = ["sweep", "pulse", "none"] as const;
const SKELETON_FILLS = ["default", ...FILL_VARIANTS] as const;

const LOADER_SIZES = [
	"sm",
	"md",
	"lg",
] as const satisfies readonly LoaderSize[];

const ILLUSTRATIONS = [
	"box",
	"search",
	"folder",
	"planet",
	"rack",
	"radar",
	"key",
	"network",
	"bell",
] as const satisfies readonly EmptyStateIllustration[];

const SAMPLE_NAMES = [
	"Jane Doe",
	"Ada Lovelace",
	"Grace Hopper",
	"Linus T",
	"deploy-bot",
	"Margaret H",
	"Alan Turing",
	"Katherine J",
	"Dennis R",
	"Barbara L",
	"Ken T",
	"Radia P",
];

const MEMBERS: readonly {
	name: string;
	email: string;
	role: string;
	status: DitherAvatarStatus;
}[] = [
	{
		email: "ada@acme.dev",
		name: "Ada Lovelace",
		role: "Owner",
		status: "online",
	},
	{
		email: "alan@acme.dev",
		name: "Alan Turing",
		role: "Admin",
		status: "busy",
	},
	{
		email: "grace@acme.dev",
		name: "Grace Hopper",
		role: "Member",
		status: "away",
	},
	{ email: "deploy-bot", name: "deploy-bot", role: "Bot", status: "offline" },
];

function Knob({
	label,
	children,
}: {
	label: string;
	children: React.ReactNode;
}) {
	return (
		<div className="flex items-center gap-2 text-ink-3 text-xs">
			<span className="font-mono text-2xs text-ink-4 uppercase tracking-[0.06em]">
				{label}
			</span>
			{children}
		</div>
	);
}

function Section({
	title,
	knobs,
	children,
}: {
	title: string;
	knobs?: React.ReactNode;
	children: React.ReactNode;
}) {
	return (
		<section className="flex flex-col gap-3">
			<SectionLabel aside={knobs}>{title}</SectionLabel>
			<div className="surface-well flex flex-col gap-4 rounded-card p-5">
				{children}
			</div>
		</section>
	);
}

function ButtonSection() {
	const [variant, setVariant] = useState<DitherButtonVariant>("primary");
	const [size, setSize] = useState<Size>("default");
	const [fill, setFill] = useState<DitherFillVariant>("gradient");
	const [color, setColor] = useState<ColorChoice>("none");
	const [bloom, setBloom] = useState<DitherBloom>("off");
	const [loading, setLoading] = useState(false);
	const [disabled, setDisabled] = useState(false);
	const shared = {
		variant,
		size,
		fill,
		color: paint(color),
		bloom,
		disabled,
	} as const;
	return (
		<Section
			title="Button"
			knobs={
				<>
					<Knob label="variant">
						<Segmented
							aria-label="Button variant"
							value={variant}
							onValueChange={setVariant}
							options={VARIANTS}
						/>
					</Knob>
					<Knob label="size">
						<Segmented
							aria-label="Button size"
							value={size}
							onValueChange={setSize}
							options={SIZES}
						/>
					</Knob>
					<Knob label="fill">
						<Segmented
							aria-label="Button fill"
							value={fill}
							onValueChange={setFill}
							options={opts(FILL_VARIANTS)}
						/>
					</Knob>
					<Knob label="color">
						<Segmented
							aria-label="Button colour"
							value={color}
							onValueChange={setColor}
							options={opts(COLORS)}
						/>
					</Knob>
					<Knob label="bloom">
						<Segmented
							aria-label="Button bloom"
							value={bloom}
							onValueChange={setBloom}
							options={opts(BLOOMS)}
						/>
					</Knob>
					<Knob label="loading">
						<Switch
							aria-label="Loading"
							size="sm"
							checked={loading}
							onCheckedChange={setLoading}
						/>
					</Knob>
					<Knob label="disabled">
						<Switch
							aria-label="Disabled"
							size="sm"
							checked={disabled}
							onCheckedChange={setDisabled}
						/>
					</Knob>
				</>
			}
		>
			<div className="flex flex-wrap items-center gap-3">
				<DitherButton {...shared} loading={loading}>
					Deploy
				</DitherButton>
				<DitherButton {...shared} loading={loading}>
					Restart service
				</DitherButton>
				<DitherButton
					{...shared}
					size={
						size === "default" ? "icon" : size === "sm" ? "icon-sm" : "icon-lg"
					}
					aria-label="Add"
				>
					+
				</DitherButton>
				<DitherButton {...shared} asChild>
					<a href="#button">As a link</a>
				</DitherButton>
				<span className="text-ink-4 text-xs">
					Hover brightens the highlight; press drops it and sinks the key.
				</span>
			</div>
			<div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
				{FILL_VARIANTS.flatMap((texture) =>
					(["blue", "green", "orange", "pink"] as const).map((hue) => (
						<DitherButton
							key={`${texture}-${hue}`}
							variant="filled"
							fill={texture}
							color={hue}
						>
							{texture}
						</DitherButton>
					)),
				)}
				{(["blue", "green", "orange", "pink"] as const).map((hue) => (
					<DitherButton
						key={`outline-${hue}`}
						variant="outline"
						color={hue}
						fill="dotted"
					>
						outline
					</DitherButton>
				))}
			</div>
		</Section>
	);
}

function AvatarSection() {
	const [name, setName] = useState("Jane Doe");
	const [size, setSize] = useState<"sm" | "default" | "lg">("default");
	const [count, setCount] = useState("7");
	const [fill, setFill] = useState<DitherFillVariant>("gradient");
	const [shape, setShape] = useState<"circle" | "square">("circle");
	const [color, setColor] = useState<ColorChoice>("none");
	const [status, setStatus] = useState<(typeof STATUSES)[number]>("none");
	const [ring, setRing] = useState(false);
	const [bloom, setBloom] = useState<DitherBloom>("off");
	const people = SAMPLE_NAMES.slice(0, Number(count)).map((n) => ({ name: n }));
	return (
		<Section
			title="Avatar"
			knobs={
				<>
					<Knob label="size">
						<Segmented
							aria-label="Avatar size"
							value={size}
							onValueChange={setSize}
							options={AVATAR_SIZES}
						/>
					</Knob>
					<Knob label="fill">
						<Segmented
							aria-label="Avatar fill"
							value={fill}
							onValueChange={setFill}
							options={opts(FILL_VARIANTS)}
						/>
					</Knob>
					<Knob label="shape">
						<Segmented
							aria-label="Avatar shape"
							value={shape}
							onValueChange={setShape}
							options={opts(AVATAR_SHAPES)}
						/>
					</Knob>
					<Knob label="color">
						<Segmented
							aria-label="Avatar colour"
							value={color}
							onValueChange={setColor}
							options={opts(COLORS)}
						/>
					</Knob>
					<Knob label="status">
						<Segmented
							aria-label="Avatar status"
							value={status}
							onValueChange={setStatus}
							options={opts(STATUSES)}
						/>
					</Knob>
					<Knob label="bloom">
						<Segmented
							aria-label="Avatar bloom"
							value={bloom}
							onValueChange={setBloom}
							options={opts(BLOOMS)}
						/>
					</Knob>
					<Knob label="ring">
						<Switch
							aria-label="Ring"
							size="sm"
							checked={ring}
							onCheckedChange={setRing}
						/>
					</Knob>
					<Knob label="people">
						<Segmented
							aria-label="Group size"
							value={count}
							onValueChange={setCount}
							options={COUNTS}
						/>
					</Knob>
				</>
			}
		>
			<div className="flex flex-wrap items-center gap-4">
				<DitherAvatar
					name={name}
					size={size}
					fill={fill}
					shape={shape}
					color={paint(color)}
					status={status === "none" ? undefined : status}
					ring={ring}
					bloom={bloom}
				/>
				<DitherAvatar
					name={name}
					size={64}
					fill={fill}
					shape={shape}
					color={paint(color)}
					bloom={bloom}
				/>
				<Input
					aria-label="Name"
					value={name}
					onChange={(event) => setName(event.target.value)}
					className="w-48"
				/>
				<span className="text-ink-4 text-xs">
					Same name, same face. Always mirrored.
				</span>
			</div>
			<div className="flex flex-wrap items-center gap-6">
				{SAMPLE_NAMES.slice(0, 6).map((n) => (
					<DitherAvatar key={n} name={n} size="lg" fill={fill} shape={shape} />
				))}
			</div>
			<div className="flex flex-wrap items-center gap-8">
				<DitherAvatarGroup
					people={people}
					max={4}
					size={size}
					aria-label="Viewers"
				/>
				<DitherAvatarGroup
					max={6}
					size={22}
					outline="var(--color-surface-well)"
				>
					{SAMPLE_NAMES.map((n) => (
						<DitherAvatar key={n} name={n} fill="solid" />
					))}
				</DitherAvatarGroup>
			</div>
			<ul className="divide-line flex w-full max-w-md flex-col divide-y rounded-card border border-line">
				{MEMBERS.map((member, index) => (
					<li key={member.email} className="flex items-center gap-3 px-4 py-3">
						<DitherAvatar
							name={member.email}
							label={member.name}
							size={36}
							status={member.status}
							fill={member.role === "Bot" ? "hatched" : "gradient"}
							color={member.role === "Bot" ? "grey" : undefined}
							ring={index === 0}
						/>
						<div className="flex min-w-0 flex-1 flex-col">
							<span className="truncate font-medium text-sm">
								{member.name}
							</span>
							<span className="truncate font-mono text-ink-4 text-xs">
								{member.email}
							</span>
						</div>
						<span className="text-ink-4 text-xs">{member.role}</span>
					</li>
				))}
			</ul>
		</Section>
	);
}

function CardSection() {
	const [finish, setFinish] =
		useState<NonNullable<DitherCardProps["finish"]>>("all");
	const [glow, setGlow] = useState<GlowChoice>("top-left");
	const [fill, setFill] = useState<DitherFillVariant>("gradient");
	const [color, setColor] = useState<ColorChoice>("none");
	const [interactive, setInteractive] = useState(true);
	const [rim, setRim] = useState(true);
	return (
		<Section
			title="Card"
			knobs={
				<>
					<Knob label="finish">
						<Segmented
							aria-label="Card finish"
							value={finish}
							onValueChange={setFinish}
							options={FINISHES}
						/>
					</Knob>
					<Knob label="glow">
						<Segmented
							aria-label="Card glow"
							value={glow}
							onValueChange={setGlow}
							options={opts(GLOWS)}
						/>
					</Knob>
					<Knob label="fill">
						<Segmented
							aria-label="Card glow fill"
							value={fill}
							onValueChange={setFill}
							options={opts(FILL_VARIANTS)}
						/>
					</Knob>
					<Knob label="color">
						<Segmented
							aria-label="Card glow colour"
							value={color}
							onValueChange={setColor}
							options={opts(COLORS)}
						/>
					</Knob>
					<Knob label="hover">
						<Switch
							aria-label="Interactive"
							size="sm"
							checked={interactive}
							onCheckedChange={setInteractive}
						/>
					</Knob>
					<Knob label="rim">
						<Switch
							aria-label="Rim"
							size="sm"
							checked={rim}
							onCheckedChange={setRim}
						/>
					</Knob>
				</>
			}
		>
			<div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
				{(["raised", "float", "pop"] as const).map((variant) => (
					<DitherCard
						key={variant}
						variant={variant}
						finish={finish}
						{...(glow === "none"
							? {}
							: {
									glow,
									fill,
									color: paint(color),
									interactive,
									rim,
									reach: 140,
								})}
						className="h-32 p-4"
					>
						<span className="font-semibold text-sm">{variant}</span>
						<span className="text-ink-3 text-xs">The text stays crisp.</span>
					</DitherCard>
				))}
			</div>
			<div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
				{(
					[
						["top", "blue", "gradient"],
						["top-right", "green", "gradient"],
						["bottom-left", "orange", "hatched"],
					] as const satisfies readonly (readonly [
						DitherCardGlow,
						string,
						DitherFillVariant,
					])[]
				).map(([position, hue, texture]) => (
					<DitherCard
						key={position}
						finish="none"
						glow={position}
						color={hue}
						fill={texture}
						matrix={texture === "gradient" ? "blue-noise" : "bayer4"}
						cell={texture === "gradient" ? 2 : 3}
						reach={130}
						interactive
						className={cn(
							"min-h-40 px-5",
							position.startsWith("bottom") ? "justify-start" : "justify-end",
						)}
					>
						<span className="font-mono text-2xs text-ink-4">{position}</span>
						<span className="font-semibold text-sm">
							{hue}, {texture}
						</span>
					</DitherCard>
				))}
			</div>
		</Section>
	);
}

function DividerSection() {
	const [variant, setVariant] =
		useState<NonNullable<DitherDividerProps["variant"]>>("fade");
	const [fill, setFill] = useState<(typeof RULE_FILLS)[number]>("default");
	const [color, setColor] = useState<ColorChoice>("none");
	const [labelled, setLabelled] = useState(false);
	const texture = fill === "default" ? undefined : fill;
	return (
		<Section
			title="Divider"
			knobs={
				<>
					<Knob label="style">
						<Segmented
							aria-label="Divider style"
							value={variant}
							onValueChange={setVariant}
							options={RULE_VARIANTS}
						/>
					</Knob>
					<Knob label="fill">
						<Segmented
							aria-label="Divider fill"
							value={fill}
							onValueChange={setFill}
							options={opts(RULE_FILLS)}
						/>
					</Knob>
					<Knob label="color">
						<Segmented
							aria-label="Divider colour"
							value={color}
							onValueChange={setColor}
							options={opts(COLORS)}
						/>
					</Knob>
					<Knob label="label">
						<Switch
							aria-label="Show label"
							size="sm"
							checked={labelled}
							onCheckedChange={setLabelled}
						/>
					</Knob>
				</>
			}
		>
			<DitherDivider
				variant={variant}
				fill={texture}
				color={paint(color)}
				label={labelled ? "or" : undefined}
			/>
			<div className="flex h-12 items-center gap-4 text-ink-3 text-xs">
				Build
				<DitherDivider
					orientation="vertical"
					variant={variant}
					fill={texture}
					color={paint(color)}
				/>
				Deploy
				<DitherDivider
					orientation="vertical"
					variant={variant}
					fill={texture}
					color={paint(color)}
				/>
				Verify
			</div>
			<div className="flex h-20 items-stretch rounded-card border border-line px-2 py-3">
				{[
					["Requests", "1.28M"],
					["p95 latency", "184ms"],
					["Error rate", "0.04%"],
				].map(([label, value], index) => (
					<div key={label} className="contents">
						{index > 0 ? (
							<DitherDivider
								orientation="vertical"
								color="ink"
								strength={0.4}
								thickness={1}
							/>
						) : null}
						<div className="flex flex-1 flex-col justify-center px-4">
							<span className="text-ink-4 text-xs">{label}</span>
							<span className="font-semibold text-lg tabular-nums">
								{value}
							</span>
						</div>
					</div>
				))}
			</div>
		</Section>
	);
}

function SkeletonSection() {
	const [shape, setShape] = useState<(typeof SKELETON_SHAPES)[number]>("block");
	const [animation, setAnimation] =
		useState<NonNullable<DitherSkeletonProps["animation"]>>("sweep");
	const [fill, setFill] = useState<(typeof SKELETON_FILLS)[number]>("default");
	const [color, setColor] = useState<ColorChoice>("none");
	const texture = fill === "default" ? undefined : fill;
	return (
		<Section
			title="Skeleton"
			knobs={
				<>
					<Knob label="shape">
						<Segmented
							aria-label="Skeleton shape"
							value={shape}
							onValueChange={setShape}
							options={opts(SKELETON_SHAPES)}
						/>
					</Knob>
					<Knob label="animation">
						<Segmented
							aria-label="Skeleton animation"
							value={animation}
							onValueChange={setAnimation}
							options={opts(SKELETON_ANIMATIONS)}
						/>
					</Knob>
					<Knob label="fill">
						<Segmented
							aria-label="Skeleton fill"
							value={fill}
							onValueChange={setFill}
							options={opts(SKELETON_FILLS)}
						/>
					</Knob>
					<Knob label="color">
						<Segmented
							aria-label="Skeleton colour"
							value={color}
							onValueChange={setColor}
							options={opts(COLORS)}
						/>
					</Knob>
				</>
			}
		>
			<div
				aria-busy="true"
				className="flex w-full max-w-sm flex-col items-start gap-3"
			>
				<span className="sr-only">Loading</span>
				<DitherSkeleton
					shape={shape}
					animation={animation}
					fill={texture}
					color={paint(color)}
					className={shape === "block" ? "h-16 w-full" : ""}
				/>
			</div>
			<div aria-busy="true" className="flex items-center gap-3">
				<span className="sr-only">Loading profile</span>
				<DitherSkeleton shape="circle" size={40} animation={animation} />
				<div className="flex flex-1 flex-col gap-2">
					<DitherSkeleton animation={animation} className="h-3.5 w-1/3" />
					<DitherSkeleton
						animation={animation}
						delay={0.1}
						className="h-3 w-2/3"
					/>
				</div>
			</div>
			<div aria-busy="true" className="flex flex-col gap-2">
				<span className="sr-only">Loading members</span>
				{[0, 1, 2, 3].map((row) => (
					<div key={row} className="flex items-center gap-3">
						<DitherSkeleton
							shape="circle"
							size={28}
							animation={animation}
							delay={row * 0.12}
						/>
						<DitherSkeleton
							animation={animation}
							delay={row * 0.12}
							className="h-3"
							style={{ width: `${48 + ((row * 17) % 30)}%` }}
						/>
						<DitherSkeleton
							animation={animation}
							delay={row * 0.12 + 0.06}
							className="ml-auto h-3 w-12"
						/>
					</div>
				))}
			</div>
		</Section>
	);
}

function LoaderSection() {
	const [color, setColor] = useState<ColorChoice>("none");
	const [bloom, setBloom] = useState<DitherBloom>("off");
	const [speed, setSpeed] = useState("1");
	return (
		<Section
			title="Loader"
			knobs={
				<>
					<Knob label="color">
						<Segmented
							aria-label="Loader colour"
							value={color}
							onValueChange={setColor}
							options={opts(COLORS)}
						/>
					</Knob>
					<Knob label="bloom">
						<Segmented
							aria-label="Loader bloom"
							value={bloom}
							onValueChange={setBloom}
							options={opts(BLOOMS)}
						/>
					</Knob>
					<Knob label="speed">
						<Segmented
							aria-label="Loader speed"
							value={speed}
							onValueChange={setSpeed}
							options={opts(["0.5", "1", "2"] as const)}
						/>
					</Knob>
				</>
			}
		>
			{LOADER_SHAPES.map((shape) => (
				<div key={shape} className="flex items-center gap-8">
					<span className="w-12 shrink-0 font-mono text-ink-4 text-xs">
						{shape}
					</span>
					{LOADER_SIZES.map((size) => (
						<DitherLoader
							key={size}
							variant={shape}
							size={size}
							color={paint(color)}
							bloom={bloom}
							speed={Number(speed)}
							label={`Loading, ${shape} ${size}`}
						/>
					))}
				</div>
			))}
			<div className="flex flex-wrap items-center gap-6 text-ink-2 text-sm">
				<span className="inline-flex items-center gap-2">
					<DitherLoader size={12} color={paint(color)} /> 12px
				</span>
				<span className="inline-flex items-center gap-2">
					<DitherLoader color={paint(color)} /> 16px
				</span>
				<span className="inline-flex items-center gap-2">
					<DitherLoader size={24} color={paint(color)} /> 24px
				</span>
				<DitherButton variant="secondary" disabled>
					<DitherLoader size={14} label="Deploying" />
					Deploying
				</DitherButton>
			</div>
			<div className="surface-base relative flex h-56 overflow-hidden rounded-card border border-line">
				<DitherLoader
					variant="page"
					label="Loading project"
					color={paint(color)}
					speed={Number(speed)}
				/>
			</div>
		</Section>
	);
}

function CompositionSection() {
	const [backdrop, setBackdrop] = useState(true);
	const [illustration, setIllustration] =
		useState<(typeof ILLUSTRATIONS)[number]>("box");
	const [color, setColor] = useState<ColorChoice>("none");
	const [animate, setAnimate] = useState(true);
	const [fill, setFill] = useState<DitherFillVariant>("gradient");
	const [code, setCode] = useState("404");
	const [depth, setDepth] = useState("4");
	return (
		<Section
			title="Empty state and not found"
			knobs={
				<>
					<Knob label="object">
						<Segmented
							aria-label="Illustration"
							value={illustration}
							onValueChange={setIllustration}
							options={opts(ILLUSTRATIONS)}
						/>
					</Knob>
					<Knob label="color">
						<Segmented
							aria-label="Colour"
							value={color}
							onValueChange={setColor}
							options={opts(COLORS)}
						/>
					</Knob>
					<Knob label="fill">
						<Segmented
							aria-label="Digit fill"
							value={fill}
							onValueChange={setFill}
							options={opts(FILL_VARIANTS)}
						/>
					</Knob>
					<Knob label="code">
						<Segmented
							aria-label="Status code"
							value={code}
							onValueChange={setCode}
							options={opts(["404", "410", "500"] as const)}
						/>
					</Knob>
					<Knob label="depth">
						<Segmented
							aria-label="Digit depth"
							value={depth}
							onValueChange={setDepth}
							options={opts(["0", "2", "4", "8"] as const)}
						/>
					</Knob>
					<Knob label="animate">
						<Switch
							aria-label="Animate"
							size="sm"
							checked={animate}
							onCheckedChange={setAnimate}
						/>
					</Knob>
					<Knob label="backdrop">
						<Switch
							aria-label="Backdrop"
							size="sm"
							checked={backdrop}
							onCheckedChange={setBackdrop}
						/>
					</Knob>
				</>
			}
		>
			<DitherEmptyState
				backdrop={backdrop}
				illustration={illustration}
				color={paint(color)}
				animate={animate}
				titleAs="h4"
				title="Add your first service"
				description="A service is anything you run: an image, a repo, a database."
				action={<DitherButton>Create service</DitherButton>}
				secondaryAction={
					<DitherButton variant="secondary">Read the docs</DitherButton>
				}
			/>
			<DitherNotFound
				backdrop={backdrop}
				titleAs="h2"
				code={code}
				color={paint(color)}
				fill={fill}
				depth={Number(depth)}
				animate={animate}
				detail="ref 7f3a-e21c · 14:02 UTC"
				actions={
					<>
						<DitherButton variant="secondary">Go to projects</DitherButton>
						<DitherButton variant="ghost">Go back</DitherButton>
					</>
				}
			/>
		</Section>
	);
}

/** Every interface piece with a few knobs. Mounted by the dev gallery route. */
export function InterfaceGallery({ className }: { className?: string }) {
	return (
		<div className={cn("flex flex-col gap-8", className)}>
			<ButtonSection />
			<AvatarSection />
			<CardSection />
			<DividerSection />
			<SkeletonSection />
			<LoaderSection />
			<CompositionSection />
		</div>
	);
}
