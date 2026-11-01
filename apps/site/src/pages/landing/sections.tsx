import { CheckIcon } from "@phosphor-icons/react/dist/csr/Check";
import { ClockIcon } from "@phosphor-icons/react/dist/csr/Clock";
import type { ReactNode } from "react";
import {
	DomainsMini,
	EnginesMini,
	Replica,
	RollbackMini,
	ServersMini,
	UptimeMini,
} from "../../components/replicas/panels.tsx";
import ProductConsole from "../../islands/console.tsx";
import DeployBench from "../../islands/deploy-bench.tsx";
import { Island } from "../../islands/island.tsx";
import { CodeWell, container, gap, link } from "./shared.tsx";

/** A section's lead paragraph: 18px, ink-2. */
const lede = "m-0 max-w-[58ch] text-ink-2 text-lg";
/** The quieter second line under a lead: the limits and the doc link. */
const note = "m-0 max-w-[62ch] text-ink-3 text-sm";

/**
 * A section's head: the mono `section-head` label with its number, the title
 * and the copy. The numbers say the order of the story, nothing else.
 */
function Head({
	id,
	label,
	index,
	title,
	children,
}: {
	id: string;
	label: string;
	index: string;
	title: string;
	children: ReactNode;
}) {
	return (
		<div className="flex max-w-[46rem] flex-col gap-4">
			<p className="section-head m-0">
				<span>{label}</span>
				<span className="section-num">{index}</span>
			</p>
			<h2 id={`${id}-title`} className="display-2 m-0 text-ink-1">
				{title}
			</h2>
			{children}
		</div>
	);
}

/** 01: a release proves itself. The bench replays the two recordings. */
export function Releases() {
	return (
		<section
			id="releases"
			aria-labelledby="releases-title"
			className={`${container} ${gap} scroll-mt-20`}
		>
			<Head
				id="releases"
				label="Every release proves itself"
				index="01"
				title="A new release takes traffic only after it passes."
			>
				<p className={lede}>
					A rolling deploy starts the new container beside the old one and moves
					traffic only after its health check passes. If the check fails, the
					old release keeps serving.
				</p>
				<p className={note}>
					Services with volumes recreate instead, with a short downtime.{" "}
					<a className={link} href="/docs/operations/deployments">
						How deployments work
					</a>
				</p>
			</Head>
			<div className="mt-10">
				<Island name="deploy-bench" mode="visible" className="block">
					<DeployBench />
				</Island>
			</div>
		</section>
	);
}

/** 02: the agent and the approval page, as one replica island. */
export function Agent() {
	return (
		<section
			id="agent"
			aria-labelledby="agent-title"
			className={`${container} ${gap} scroll-mt-20`}
		>
			<Head
				id="agent"
				label="Bring your AI agent"
				index="02"
				title="Your agent does the work. Over MCP, production waits for you."
			>
				<p className={lede}>
					Your agent runs in its own client and connects over MCP or the CLI.
					Over MCP, edits stage until they are applied, and production changes
					wait for a person to approve them.
				</p>
				<p className={note}>
					The instance admin switches MCP on. The dashboard has no AI of its
					own.{" "}
					<a className={link} href="/docs/reference/mcp">
						MCP reference
					</a>
				</p>
			</Head>
			<div className="mt-10">
				<Island name="console" className="block">
					<ProductConsole />
				</Island>
			</div>
		</section>
	);
}

/** One line of a terminal: a command after the prompt, or a comment. */
function Line({ cmd, comment }: { cmd?: string; comment?: string }) {
	return (
		<span className="block whitespace-pre">
			{cmd ? (
				<>
					<span aria-hidden className="select-none text-accent-ink">
						${" "}
					</span>
					<span className="text-ink-1">{cmd}</span>
				</>
			) : (
				<span className="text-ink-4"># {comment}</span>
			)}
		</span>
	);
}

/** A ruled list under a mono label: what keeps running, and what waits. */
function Rows({
	label,
	wait = false,
	items,
}: {
	label: string;
	wait?: boolean;
	items: string[];
}) {
	const Icon = wait ? ClockIcon : CheckIcon;
	return (
		<div className="min-w-0 flex-1 basis-56">
			<p className="type-micro m-0 pb-2.5">{label}</p>
			<ul className="m-0 list-none p-0">
				{items.map((item) => (
					<li
						key={item}
						className="flex items-center gap-2.5 border-line-faint border-t py-2.5 text-ink-2 text-sm"
					>
						<Icon
							aria-hidden
							weight="bold"
							className={`size-3.5 shrink-0 ${wait ? "text-ink-4" : "text-accent-ink"}`}
						/>
						{item}
					</li>
				))}
			</ul>
		</div>
	);
}

/**
 * 03: the control plane can stop. Every line is in the FAQ and
 * docs/installing/control-plane.md: containers and the proxy keep serving,
 * the interface waits, agents reconcile on return. The terminal shows the
 * commands only; it prints no output that no recording holds.
 */
export function ControlPlane() {
	return (
		<section
			id="control-plane"
			aria-labelledby="control-plane-title"
			className={`${container} ${gap} scroll-mt-20`}
		>
			<div className="flex flex-wrap items-start gap-x-16 gap-y-10">
				<div className="min-w-0 flex-1 basis-80">
					<Head
						id="control-plane"
						label="Stop the control plane"
						index="03"
						title="Turn it off. Your apps keep serving."
					>
						<p className={lede}>
							The control plane never stops containers on your servers while it
							is down, restarts and upgrades included. When it is back, each
							agent reports what is running and the control plane reconciles it.
						</p>
						<p className={note}>
							<a className={link} href="/docs/installing/control-plane">
								The control plane
							</a>
						</p>
					</Head>
				</div>
				<div className="flex min-w-0 flex-[1.3] basis-[28rem] flex-col gap-8">
					<figure className="surface-float relative m-0 overflow-hidden rounded-xl">
						<figcaption className="flex items-center justify-between gap-3 border-line border-b px-4 py-2.5 font-mono text-ink-3 text-xs">
							<span className="text-ink-2">Terminal</span>
							<span>control plane host</span>
						</figcaption>
						<div className="p-4">
							<CodeWell label="Stopping and starting the control plane">
								<Line cmd="cd /opt/nebulactrl" />
								<Line cmd="sudo docker compose stop nebulactrl" />
								<Line cmd="curl -sI https://shop.example.com | head -1" />
								<Line comment="the shop still answers: its container and the proxy kept running" />
								<Line cmd="sudo docker compose start nebulactrl" />
								<Line comment="each agent reconnects and reports what is running" />
							</CodeWell>
						</div>
					</figure>
					<div className="flex flex-wrap gap-x-8 gap-y-6">
						<Rows
							label="Keeps running"
							items={[
								"Every container on your servers",
								"The proxy on each server",
							]}
						/>
						<Rows
							label="Waits until it is back"
							wait
							items={[
								"The dashboard and deploys",
								"Logs and metrics in the interface",
								"A backup slot that came due",
							]}
						/>
					</div>
				</div>
			</div>
		</section>
	);
}

interface Feature {
	id: string;
	title: string;
	copy: ReactNode;
	figure: ReactNode;
}

/** The six cards under 04. Each sentence is the one the docs it links to back. */
const FEATURES: Feature[] = [
	{
		id: "rollback",
		title: "Roll back",
		copy: (
			<>
				Rolling back creates a new deployment of an earlier release. It restores
				the image and configuration; databases and volumes are not rolled back.{" "}
				<a className={link} href="/docs/operations/deployments">
					Deployments
				</a>
			</>
		),
		figure: <RollbackMini />,
	},
	{
		id: "servers",
		title: "Servers dial out",
		copy: (
			<>
				A small agent opens the connection to the control plane, so a box behind
				your home router is managed like a VPS. Public sites still need ports 80
				and 443, or a Cloudflare Tunnel.{" "}
				<a className={link} href="/docs/installing/servers">
					Add a server
				</a>
			</>
		),
		figure: <ServersMini />,
	},
	{
		id: "databases",
		title: "Databases and backups",
		copy: (
			<>
				Seven engines, with Valkey as the default key value engine. Backups run
				on a schedule to S3 compatible storage or a directory, and restore into
				a new or the same database.{" "}
				<a className={link} href="/docs/operations/database-backups">
					Database backups
				</a>
			</>
		),
		figure: <EnginesMini />,
	},
	{
		id: "domains",
		title: "Domains, DNS and tailnet",
		copy: (
			<>
				A domain gets a Let's Encrypt certificate. With Cloudflare connected,
				its DNS records follow the service. Or keep a service on your tailnet,
				with no public port.{" "}
				<a className={link} href="/docs/operations/cloudflare">
					Cloudflare
				</a>
				,{" "}
				<a className={link} href="/docs/operations/service-tailnet">
					tailnet
				</a>
			</>
		),
		figure: <DomainsMini />,
	},
	{
		id: "uptime",
		title: "Uptime and status pages",
		copy: (
			<>
				Every service is probed from its own server every 30 seconds, with an
				alert when it goes down and when it recovers. A status page on your own
				hostname shows 90 days.{" "}
				<a className={link} href="/docs/operations/uptime">
					Uptime
				</a>
				,{" "}
				<a className={link} href="/docs/operations/status-pages">
					status pages
				</a>
			</>
		),
		figure: <UptimeMini />,
	},
	{
		id: "leaving",
		title: "Leaving is easy",
		copy: (
			<>
				Your services are plain Docker containers, and their data sits on your
				own disks. Uninstalling stops NebulaCtrl and leaves your containers
				running.{" "}
				<a className={link} href="/docs/installing/uninstall">
					Uninstall
				</a>
			</>
		),
		figure: (
			<Replica label="The uninstall command: it stops NebulaCtrl and your containers keep running.">
				<code className="block overflow-x-auto font-mono text-[12.5px] leading-6">
					<span className="block whitespace-pre text-ink-4">
						# stops NebulaCtrl, keeps your containers
					</span>
					<span className="block whitespace-pre text-ink-1">
						sudo bash /opt/nebulactrl/uninstall.sh
					</span>
				</code>
			</Replica>
		),
	},
];

/** 04: everything else, as six cards of one size. The nav's Features link lands here. */
export function Features() {
	return (
		<section
			id="features"
			aria-labelledby="features-title"
			className={`${container} ${gap} scroll-mt-20`}
		>
			<Head
				id="features"
				label="Comes with it"
				index="04"
				title="The rest of a platform, already in the box."
			>
				<p className={lede}>
					What you would otherwise glue together yourself, on the same servers.
				</p>
			</Head>
			<div className="mt-10 grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
				{FEATURES.map((feature) => (
					<article
						key={feature.id}
						id={feature.id}
						aria-labelledby={`${feature.id}-title`}
						className="surface-raised flex min-w-0 flex-col gap-3 rounded-xl p-5"
					>
						<div className="surface-well flex min-h-32 flex-col justify-center rounded-lg p-3.5">
							{feature.figure}
						</div>
						<h3
							id={`${feature.id}-title`}
							className="m-0 mt-1 font-semibold text-base text-ink-1"
						>
							{feature.title}
						</h3>
						<p className="m-0 text-ink-3 text-sm leading-relaxed">
							{feature.copy}
						</p>
					</article>
				))}
			</div>
		</section>
	);
}
