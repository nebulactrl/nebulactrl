import { CheckCircleIcon } from "@phosphor-icons/react/dist/csr/CheckCircle";
import { CircleNotchIcon } from "@phosphor-icons/react/dist/csr/CircleNotch";
import { CubeIcon } from "@phosphor-icons/react/dist/csr/Cube";
import { HardDrivesIcon } from "@phosphor-icons/react/dist/csr/HardDrives";
import type { CSSProperties, ReactNode, Ref } from "react";
import { TOML_LINES } from "./story.ts";
import { tokenize } from "./toml.tsx";

/** The deploy phases, in the order the app lights them. */
export const PHASES = ["Build", "Release", "Verify"] as const;

/** The step of the story a part arrives with (`--from`, read against `--t` by `.con-in`). */
function from(n: number): CSSProperties {
	return { "--from": n } as CSSProperties;
}

const key =
	"inline-flex h-9 items-center rounded-ctl px-3.5 font-medium text-sm leading-none";

const ghost = (
	<svg
		viewBox="0 0 20 24"
		width="20"
		height="24"
		aria-hidden="true"
		className="block drop-shadow-[0_2px_3px_rgb(2_4_10/0.6)]"
	>
		<path
			d="M2.5 1.8 17.2 13.6l-6.4.9 3.7 6.9-3 1.5-3.7-6.9-4.7 4.4z"
			fill="#f4f4fb"
			stroke="#0b0a14"
			strokeWidth="1.4"
			strokeLinejoin="round"
		/>
	</svg>
);

/** One call of the agent: the tool and its arguments, then what came back, under a rule. */
function Call({
	step,
	name,
	args,
	after = false,
	children,
}: {
	step: number;
	name: string;
	args: string;
	/** Shares its place with the waiting line, so it stays hidden until the approval. */
	after?: boolean;
	children: ReactNode;
}) {
	return (
		<li
			className={`con-in m-0 list-none ${after ? "con-after" : ""}`.trim()}
			style={from(step)}
		>
			<p className="m-0 flex items-baseline gap-2">
				<span
					aria-hidden
					className="size-1.5 shrink-0 translate-y-[-1px] rounded-full bg-accent-ink"
				/>
				<span className="min-w-0 break-words">
					<span className="text-ink-1">{name}</span>{" "}
					<span className="text-ink-3">{args}</span>
				</span>
			</p>
			<p className="m-0 mt-0.5 ml-[3px] break-words border-line border-l pl-4 text-ink-3">
				{children}
			</p>
		</li>
	);
}

/** The agent's window: a generic MCP client, with the file it writes and the calls it makes. */
function AgentWindow() {
	return (
		<div className="surface-well relative flex min-w-0 flex-col overflow-hidden rounded-2xl">
			<header className="flex h-11 shrink-0 items-center justify-between gap-4 border-line border-b px-4 font-mono text-[13px] text-ink-3">
				<span className="text-ink-2">agent · MCP</span>
				<span>your client</span>
			</header>
			<div className="flex flex-1 flex-col gap-5 p-4 font-mono text-[12px] text-ink-2 leading-6 sm:p-5 sm:text-[13px]">
				<div>
					<p id="con-toml-title" className="m-0 mb-2 text-ink-3">
						nebula.toml
					</p>
					<pre
						// biome-ignore lint/a11y/noNoninteractiveTabindex: a scrollable code block must be reachable by keyboard.
						tabIndex={0}
						className="m-0 overflow-x-auto rounded-lg border border-line-faint bg-white/[0.02] px-3 py-2.5 font-mono outline-none focus-visible:focus-ring"
					>
						<code>
							{TOML_LINES.map((line, i) => (
								<span
									// biome-ignore lint/suspicious/noArrayIndexKey: a fixed file; the line number is its identity
									key={i}
									className="block min-h-6 whitespace-pre"
									data-line={i}
								>
									<span>{tokenize(line)}</span>
									<i className="con-caret" />
								</span>
							))}
						</code>
					</pre>
				</div>

				<ol className="m-0 flex list-none flex-col gap-3 p-0">
					<Call
						step={1}
						name="sync_config"
						args={'{ action: "plan", environment: "production", file }'}
					>
						valid: true
					</Call>
					<Call
						step={2}
						name="sync_config"
						args={'{ action: "apply", planId }'}
					>
						staged, nothing deployed
					</Call>
					<Call
						step={3}
						name="apply_changes"
						args={'{ services: [{ service: "shop/production/web" }] }'}
					>
						confirmation_required: approve at
						https://nebula.example.com/approvals/{"{id}"}
					</Call>
					<li className="m-0 grid list-none">
						<div className="con-wait col-start-1 row-start-1 flex items-center gap-2 pl-[3px]">
							<span className="text-ink-3">Waiting for you to approve…</span>
						</div>
						<ol className="col-start-1 row-start-1 m-0 flex list-none flex-col gap-3 p-0">
							<Call
								after
								step={4}
								name="apply_changes"
								args={'{ services: [{ service: "shop/production/web" }] }'}
							>
								{
									'production: true, deployments: [{ status: "queued", strategy: "rolling" }]'
								}
							</Call>
						</ol>
					</li>
				</ol>

				<div
					className="con-in rounded-lg border border-line-faint bg-white/[0.02] px-3 py-2.5 leading-6"
					style={from(5)}
				>
					<p className="m-0 text-ink-1">
						<span aria-hidden className="select-none text-ink-4">
							${" "}
						</span>
						nebulactrl deployments status 7f3c9a1e-…
					</p>
					<p className="m-0 whitespace-pre text-ink-3">
						{
							"Deployment  7f3c9a1e-…\nStatus      succeeded\nStrategy    rolling"
						}
					</p>
				</div>
			</div>
		</div>
	);
}

/** A `Badge` of the product: a flat capsule with a status tone. */
function Badge({
	tone,
	children,
}: {
	tone: "staged" | "live";
	children: string;
}) {
	const style =
		tone === "live"
			? "border-status-live/30 bg-status-live-wash text-status-live-on"
			: "border-status-staged/30 bg-status-staged-wash text-status-staged-on";
	return (
		<span
			className={`inline-flex h-[22px] shrink-0 items-center rounded-full border px-2 font-medium text-[13px] leading-none ${style}`}
		>
			{children}
		</span>
	);
}

const EFFECTS = [
	"Pulls ghcr.io/acme/shop:1.4 and starts it.",
	"Applies the changes staged since the last deployment (configuration, variables, domains, volumes).",
	"Rolling update: the new container must pass its health check before the old one stops.",
	"Target server: agent1.",
] as const;

/** A row of the approval page's list: a label and its value. */
function KvRow({ label, children }: { label: string; children: ReactNode }) {
	return (
		<div className="flex items-center justify-between gap-4 border-line-faint border-t py-2.5 text-sm">
			<span className="text-ink-3">{label}</span>
			<span className="min-w-0 text-right text-ink-1">{children}</span>
		</div>
	);
}

/** The service's canvas card as the deploy goes: the pill, the three phases, the server. */
function ServiceCard() {
	return (
		<div className="surface-raised rounded-xl px-[13px] pt-[11px] pb-[13px] text-left">
			<div className="flex items-center gap-2.5">
				<span
					className="service-tile size-7 rounded-lg text-white"
					style={{ "--t": "var(--color-accent-fill-top)" } as CSSProperties}
				>
					<CubeIcon aria-hidden weight="bold" className="size-4" />
				</span>
				<div className="min-w-0 flex-1">
					<div className="truncate font-semibold text-ink-1 text-sm leading-[1.15]">
						web
					</div>
					<div className="mt-[3px] truncate font-mono text-[13px] text-ink-3 leading-none">
						ghcr.io/acme/shop:1.4
					</div>
				</div>
				<span className="con-pill inline-grid shrink-0">
					<span
						data-p="building"
						className="inline-flex h-[22px] items-center gap-1 rounded-full bg-status-building-wash px-2 font-semibold text-[13px] text-status-building-on"
					>
						<CircleNotchIcon aria-hidden className="size-3.5 animate-spin" />
						Building
					</span>
					<span
						data-p="deploying"
						className="inline-flex h-[22px] items-center gap-1 rounded-full bg-status-building-wash px-2 font-semibold text-[13px] text-status-building-on"
					>
						<CircleNotchIcon aria-hidden className="size-3.5 animate-spin" />
						Deploying
					</span>
					<span
						data-p="live"
						className="inline-flex h-[22px] items-center gap-1 rounded-full bg-status-live-wash px-2 font-semibold text-[13px] text-status-live-on"
					>
						<CheckCircleIcon aria-hidden weight="fill" className="size-3.5" />
						Live
					</span>
				</span>
			</div>
			<div className="mt-[11px] flex gap-[5px]">
				{PHASES.map((phase) => (
					<div
						key={phase}
						className="con-phase flex min-w-0 flex-1 flex-col gap-1"
					>
						<span className="phase-strip w-full">
							<i data-s="done" />
						</span>
						<span className="truncate text-[13px] text-ink-3 leading-none">
							{phase}
						</span>
					</div>
				))}
			</div>
			<div className="mt-[11px]">
				<span className="inline-flex h-5 max-w-[150px] items-center gap-[5px] rounded-md border border-line-faint bg-white/[0.045] px-[7px] font-mono text-[13px] text-ink-3 leading-none">
					<HardDrivesIcon aria-hidden className="size-3 shrink-0" />
					<span className="truncate">agent1</span>
				</span>
			</div>
		</div>
	);
}

/** NebulaCtrl's window: the approval page of the product, and the service it deploys. */
function ApprovalWindow() {
	return (
		<div className="surface-float relative flex min-w-0 flex-col overflow-hidden rounded-2xl">
			<header className="flex h-11 shrink-0 items-center justify-between gap-4 border-line border-b px-4 font-mono text-[13px] text-ink-3">
				<span className="text-ink-2">NebulaCtrl</span>
				<span className="truncate">nebula.example.com/approvals/…</span>
			</header>
			<div className="flex flex-1 flex-col gap-4 p-4 sm:p-5">
				<div className="con-in" style={from(3)}>
					<p className="m-0 font-display font-medium text-ink-1 text-xl leading-tight">
						Approve a production change
					</p>
					<p className="m-0 mt-1 text-ink-3 text-sm">shop / production</p>
				</div>

				<div className="con-in surface-raised rounded-xl p-4" style={from(3)}>
					<div className="flex items-start justify-between gap-3">
						<p className="m-0 min-w-0 font-semibold text-[15px] text-ink-1 leading-snug">
							Deploy web to production (shop / production)
						</p>
						<span className="inline-grid shrink-0">
							<span className="con-pending col-start-1 row-start-1 justify-self-end">
								<Badge tone="staged">Waiting for approval</Badge>
							</span>
							<span className="con-done col-start-1 row-start-1 justify-self-end">
								<Badge tone="live">Approved</Badge>
							</span>
						</span>
					</div>
					<ul className="m-0 mt-3 flex list-disc flex-col gap-1.5 pl-5 text-ink-2 text-sm leading-snug">
						{EFFECTS.map((effect) => (
							<li key={effect}>{effect}</li>
						))}
					</ul>
					<div className="mt-3">
						<KvRow label="Asked by">MCP token agent</KvRow>
						<KvRow label="Affects">
							<span className="inline-flex h-[22px] items-center rounded-full border border-line px-2 text-[13px] text-ink-2">
								service web
							</span>
						</KvRow>
					</div>
					<div className="mt-1 grid min-h-[3.5rem] items-center">
						<p className="con-pending col-start-1 row-start-1 m-0 text-ink-3 text-sm leading-snug">
							Nothing has run. The call waits until you approve it, and stays
							refused if you decline.
						</p>
						<p className="con-done col-start-1 row-start-1 m-0 text-ink-3 text-sm leading-snug">
							The agent can repeat the call now. It runs once, exactly as shown,
							and only while this request has not expired.
						</p>
					</div>
					<div className="con-keys mt-2 flex items-center justify-end gap-2">
						<span className={`${key} btn-secondary`}>Decline</span>
						<button
							type="button"
							tabIndex={-1}
							className={`${key} btn-primary pressable con-approve cursor-pointer outline-none focus-visible:focus-ring`}
						>
							Approve change
						</button>
					</div>
				</div>

				<div className="con-in" style={from(4)}>
					<ServiceCard />
				</div>
			</div>
		</div>
	);
}

/**
 * The product in one picture: an agent in its own MCP client on the left,
 * NebulaCtrl on the right. The agent writes `nebula.toml`, plans it, stages it
 * and calls `apply_changes`; production waits, so NebulaCtrl shows the approval
 * page ("Approve a production change", the plan's own summary and effects,
 * "Decline" and "Approve change"); once approved the agent repeats the call and
 * the service's card goes Building, Deploying, Live. An HTML replica, not a
 * screenshot, hidden from assistive tech with one sentence that says what it
 * shows.
 *
 * The markup is the end of the story (file written, approved, `web` live), so
 * no JavaScript and reduced motion show a meaningful frame. The `console`
 * island plays the story on `stage`, which carries its state (`data-t`, `--t`).
 * The replica never changes size: every part is laid out from the first frame
 * and only its opacity, transform and colour move.
 */
export function ConsoleWindow({ stage }: { stage?: Ref<HTMLDivElement> }) {
	return (
		<>
			<p className="sr-only">
				An AI agent, connected to NebulaCtrl over MCP from its own client,
				writes a nebula.toml, plans it and stages it. When it asks to apply the
				change to production, NebulaCtrl shows an approval page and waits for a
				person to approve. Then web deploys and goes live.
			</p>
			<div aria-hidden="true" className="con" data-boot="">
				<div
					ref={stage}
					className="con-stage relative grid gap-4 xl:grid-cols-[minmax(0,6fr)_minmax(0,5fr)]"
				>
					<AgentWindow />
					<ApprovalWindow />
					<span className="con-cursor" aria-hidden="true">
						{ghost}
					</span>
				</div>
			</div>
		</>
	);
}
