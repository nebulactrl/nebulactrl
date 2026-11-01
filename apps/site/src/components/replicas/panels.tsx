import { ArrowRightIcon } from "@phosphor-icons/react/dist/csr/ArrowRight";
import { CheckCircleIcon } from "@phosphor-icons/react/dist/csr/CheckCircle";
import { DatabaseIcon } from "@phosphor-icons/react/dist/csr/Database";
import { HardDrivesIcon } from "@phosphor-icons/react/dist/csr/HardDrives";
import { LockSimpleIcon } from "@phosphor-icons/react/dist/csr/LockSimple";
import type { ReactNode } from "react";

/**
 * The homepage's static replicas: panels of the product and one diagram, drawn
 * in HTML with tokens only. Each is hidden from assistive tech with one
 * sentence that says what it shows (`Replica`); none holds a figure that no
 * recording or doc backs, so the labels are generic samples and the numbers are
 * left out.
 */

/** A decorative replica with its one sentence of visually hidden text. */
export function Replica({
	label,
	children,
}: {
	label: string;
	children: ReactNode;
}) {
	return (
		<>
			<p className="sr-only">{label}</p>
			<div aria-hidden="true">{children}</div>
		</>
	);
}

type ChipTone = "plain" | "live" | "accent" | "staged";

const CHIP_TONE: Record<ChipTone, string> = {
	plain: "border-line-faint bg-ink-1/6 text-ink-3",
	live: "border-status-live/30 bg-status-live-wash text-status-live-on",
	accent: "border-accent-ink/25 bg-accent-wash text-accent-ink",
	staged: "border-status-staged/30 bg-status-staged-wash text-status-staged-on",
};

/** The product's Badge: a flat mono chip. */
function Chip({
	tone = "plain",
	children,
}: {
	tone?: ChipTone;
	children: ReactNode;
}) {
	return (
		<span
			className={`inline-flex max-w-full items-center gap-1 rounded-md border px-1.5 py-[3px] font-medium font-mono text-[11px] leading-none [&>svg]:size-3 [&>svg]:shrink-0 ${CHIP_TONE[tone]}`}
		>
			{children}
		</span>
	);
}

/** A box of the dial out diagram: a name and what runs there. */
function Node({ name, sub }: { name: string; sub: string }) {
	return (
		<div className="surface-raised min-w-0 rounded-lg px-3 py-2">
			<p className="m-0 flex items-center gap-1.5 font-semibold text-[13px] text-ink-1">
				<HardDrivesIcon aria-hidden className="size-3.5 shrink-0 text-ink-3" />
				{name}
			</p>
			<p className="m-0 mt-0.5 font-mono text-[11px] text-ink-3">{sub}</p>
		</div>
	);
}

/** A dashed wire with its label and an arrow: the direction the connection is opened in. */
function Wire({ label }: { label: string }) {
	return (
		<div className="flex min-w-0 flex-1 items-center gap-1.5 font-mono text-[11px] text-ink-3">
			<span
				aria-hidden
				className="h-px min-w-3 flex-1 border-line-control border-t border-dashed"
			/>
			<span className="whitespace-nowrap">{label}</span>
			<ArrowRightIcon aria-hidden className="size-3 shrink-0" />
		</div>
	);
}

/*
 * The landing page's compact replicas, one per feature card. Each draws only
 * the inside of the card's engraved well, with tokens; the card supplies the
 * well. None holds a figure that no recording or doc backs.
 */

/** The two directions: the agent opens the connection out, visitors come in on 80 and 443. */
export function ServersMini() {
	return (
		<Replica label="A server's agent opens the connection to the control plane, so managing it needs no inbound port. Visitors of a public site reach the server's proxy on ports 80 and 443.">
			<div className="flex flex-col gap-3">
				<div className="flex items-center gap-2">
					<Node name="Server" sub="agent" />
					<Wire label="opens" />
					<Node name="Control plane" sub="outbound only" />
				</div>
				<div className="flex items-center gap-2">
					<Node name="Visitors" sub="the internet" />
					<Wire label="80 and 443" />
					<Node name="Server" sub="proxy" />
				</div>
			</div>
		</Replica>
	);
}

/** The catalog's seven engines in the order the product lists them: the default key value engine leads. */
export const ENGINES: {
	name: string;
	kind: string;
	note?: "Default" | "Opt in";
}[] = [
	{ name: "Valkey", kind: "Key value", note: "Default" },
	{ name: "PostgreSQL", kind: "Relational" },
	{ name: "MySQL", kind: "Relational" },
	{ name: "MariaDB", kind: "Relational" },
	{ name: "Redis", kind: "Key value" },
	{ name: "MongoDB", kind: "Document", note: "Opt in" },
	{ name: "ClickHouse", kind: "Analytics" },
];

export function EnginesMini() {
	return (
		<Replica label="The seven database engines: Valkey, the default, then PostgreSQL, MySQL, MariaDB, Redis, MongoDB (opt in) and ClickHouse. Backups go to S3 compatible storage or a directory.">
			<ul className="m-0 flex list-none flex-wrap gap-1.5 p-0">
				{ENGINES.map((engine) => (
					<li key={engine.name}>
						<Chip
							tone={
								engine.note === "Default"
									? "accent"
									: engine.note === "Opt in"
										? "staged"
										: "plain"
							}
						>
							<DatabaseIcon aria-hidden />
							{engine.name}
							{engine.note ? ` · ${engine.note}` : ""}
						</Chip>
					</li>
				))}
			</ul>
			<p className="m-0 mt-3 flex flex-wrap items-center gap-1.5 text-ink-3 text-xs">
				Backups to <Chip>S3 compatible storage</Chip> <Chip>a directory</Chip>
			</p>
		</Replica>
	);
}

/** One route of a service: its hostname, and what makes it reachable. */
function Route({ host, children }: { host: string; children: ReactNode }) {
	return (
		<li className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1.5">
			<span className="min-w-0 break-all font-mono text-[13px] text-ink-1">
				{host}
			</span>
			<span className="flex flex-wrap items-center gap-1.5">{children}</span>
		</li>
	);
}

export function DomainsMini() {
	return (
		<Replica label="Two routes of a service: a public domain with a certificate and a Cloudflare DNS record, and a hostname on a tailnet with no public port.">
			<ul className="m-0 flex list-none flex-col gap-3 p-0">
				<Route host="shop.example.com">
					<Chip tone="live">
						<CheckCircleIcon aria-hidden weight="fill" />
						Certificate
					</Chip>
					<Chip>Cloudflare DNS</Chip>
				</Route>
				<Route host="web.example.ts.net">
					<Chip tone="accent">
						<LockSimpleIcon aria-hidden />
						Tailnet only
					</Chip>
					<Chip>no public port</Chip>
				</Route>
			</ul>
		</Replica>
	);
}

/** The deployments list after a rollback: the rollback on top and Live, earlier releases each with the product's key. */
export function RollbackMini() {
	const key =
		"rounded-md border border-line-strong px-2 py-1 font-medium text-[12px] text-ink-1 leading-none shadow-[var(--shadow-key)]";
	return (
		<Replica label="The deployments of a service after a rollback: the rollback to ghcr.io/acme/shop:1.3 on top and live, and earlier releases, each with a Rollback to this release button.">
			<ul className="m-0 flex list-none flex-col gap-2.5 p-0">
				<li className="flex flex-wrap items-center justify-between gap-2">
					<span className="font-mono text-[13px] text-ink-1">
						ghcr.io/acme/shop:1.3
					</span>
					<Chip tone="live">Live · rollback</Chip>
				</li>
				{["1.4", "1.2"].map((version) => (
					<li
						key={version}
						className="flex flex-wrap items-center justify-between gap-2 border-line-faint border-t pt-2.5"
					>
						<span className="font-mono text-[13px] text-ink-3">
							ghcr.io/acme/shop:{version}
						</span>
						<span className={key}>Rollback to this release</span>
					</li>
				))}
			</ul>
		</Replica>
	);
}

/*
 * The public status page, drawn the way internal/statuspublic draws it
 * (status.css): one flat cell per day, 90 days ending today. A healthy day is
 * a quiet tint, so a bad day is the only loud cell; a degraded day is striped,
 * so colour is never the only difference. It is set to hide its figures, as a
 * page can, so no percentage shows.
 */

type Band = "up" | "good" | "warn" | "maint";

/** The page's own fills (status.css `.c-*`), on the site's tokens. */
const BAND: Record<Band, string> = {
	up: "color-mix(in oklab, var(--color-ok) 34%, var(--color-ink-4))",
	good: "color-mix(in oklab, var(--color-status-degraded) 50%, var(--color-ink-4))",
	warn: "url(#ub-warn)",
	maint: "var(--color-info)",
};

const DAYS = 90;
/** A cell's share of its day slot; the rest is the gap. */
const CELL_SHARE = 0.78;

interface SampleComponent {
	name: string;
	/** Days ago → band; every other day is operational. */
	days: Record<number, Band>;
}

/** The sample component: one day of 99 to 99.9% a month ago, operational otherwise. */
const WEB: SampleComponent = { name: "web", days: { 31: "good" } };

/** The degraded stripe (status.css `#p-warn`): amber with a dark diagonal. */
function StripeSprite() {
	return (
		<svg
			width="0"
			height="0"
			className="absolute"
			aria-hidden="true"
			focusable="false"
		>
			<defs>
				<pattern
					id="ub-warn"
					width="6"
					height="6"
					patternUnits="userSpaceOnUse"
					patternTransform="rotate(45)"
				>
					<rect width="6" height="6" fill="var(--color-status-degraded)" />
					<rect width="2" height="6" fill="rgb(2 4 10 / 0.38)" />
				</pattern>
			</defs>
		</svg>
	);
}

/**
 * Cells are placed in percentages, so the bar fills any width; the stripe
 * pattern stays in CSS pixels.
 */
function UptimeBar({ component }: { component: SampleComponent }) {
	const slot = 100 / DAYS;
	return (
		<>
			{/* Narrow screens keep the last 30 days: three times as wide, cropped to the end. */}
			<div className="surface-well mt-2.5 overflow-hidden rounded-[5px]">
				<svg
					shapeRendering="crispEdges"
					aria-hidden="true"
					className="-ml-[200%] block h-9 w-[300%] sm:ml-0 sm:w-full"
				>
					{Array.from({ length: DAYS }, (_, i) => {
						const daysAgo = DAYS - 1 - i;
						const band = component.days[daysAgo] ?? "up";
						return (
							<rect
								key={daysAgo}
								x={`${i * slot}%`}
								width={`${slot * CELL_SHARE}%`}
								height="100%"
								fill={BAND[band]}
							/>
						);
					})}
				</svg>
			</div>
			<div className="mt-1.5 flex justify-between text-ink-3 text-xs">
				<span className="hidden sm:inline">90 days ago</span>
				<span className="sm:hidden">30 days ago</span>
				<span>Today</span>
			</div>
		</>
	);
}

/** One component of a public status page: its name, its state and its last 90 days as flat cells. */
export function UptimeMini() {
	return (
		<Replica label="One component of a public status page: web, operational, with a bar of its last 90 days, one day a month ago slightly degraded.">
			<StripeSprite />
			<div className="flex items-center justify-between gap-3 text-[13px]">
				<span className="text-ink-1">{WEB.name}</span>
				<span className="flex items-center gap-1.5 text-status-live-on">
					<CheckCircleIcon aria-hidden className="size-3.5" />
					Operational
				</span>
			</div>
			<UptimeBar component={WEB} />
		</Replica>
	);
}
