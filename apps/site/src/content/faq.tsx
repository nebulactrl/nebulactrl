import type { ReactNode } from "react";

export interface FaqItem {
	q: string;
	a: ReactNode;
}

export interface FaqGroup {
	id: string;
	title: string;
	items: FaqItem[];
}

const a = "text-accent-ink";
const code = "font-mono text-ink-1";

/**
 * The questions people ask, in three groups, each in the order they ask them.
 * Links point at docs pages and site pages and are checked by a test.
 */
export function faqGroups(): FaqGroup[] {
	return [
		{
			id: "before-you-install",
			title: "Before you install",
			items: [
				{
					q: "Is it free?",
					a: (
						<>
							Yes. NebulaCtrl is free to run on your own servers, with no hosted
							version, no paid tier and no licence key. It is open source under
							the AGPL-3.0: if you modify it and let others use it over a
							network, you offer them your source.{" "}
							<a className={a} href="/licence">
								The licence page
							</a>{" "}
							has the details.
						</>
					),
				},
				{
					q: "What do I need?",
					a: (
						<>
							One Linux machine with systemd and root access: Debian 12 or 13,
							Ubuntu 22.04, 24.04 or 26.04 LTS, or RHEL, Rocky or AlmaLinux 9,
							on amd64 or arm64, with 1 GB of RAM for the control plane. The
							installer adds Docker 24 or newer if it is missing. Ports 80 and
							443 should be free. A domain is optional: without one the
							dashboard is plain HTTP on port 8000 until you add one.{" "}
							<a className={a} href="/docs/installing/requirements">
								Requirements
							</a>
						</>
					),
				},
				{
					q: "What does the installer change?",
					a: (
						<>
							It installs Docker if needed, applies safe daemon defaults (log
							rotation, live restore), starts Postgres on a private network and
							the control plane, sets up ufw, fail2ban and unattended security
							updates, and schedules a daily backup. Every step has a flag to
							skip it, it keeps your existing Docker settings, and it never
							changes SSH settings. To upgrade, run it again, with{" "}
							<code className={code}>--version</code> to pin a release; to go
							back, run it with the previous version. The uninstaller keeps your
							apps and data unless you ask otherwise.{" "}
							<a className={a} href="/install.sh">
								Read the script
							</a>{" "}
							before you run it, or see{" "}
							<a className={a} href="/docs/installing/installer">
								what each step does
							</a>{" "}
							and how to{" "}
							<a className={a} href="/docs/installing/upgrade">
								upgrade and roll back
							</a>
							.
						</>
					),
				},
				{
					q: "Does it need Docker Compose?",
					a: (
						<>
							The control plane runs from a Compose file that the installer
							writes, so the host needs Docker and the Compose plugin, version
							2.24 or newer. The installer adds both when they are missing. Your
							own apps do not need Compose: a service is an image or a Git
							repository. If you already have a Compose file, you can run it as
							one stack, which needs the same plugin on the server that runs it,
							or split it into separate services.{" "}
							<a className={a} href="/docs/installing/requirements">
								Requirements
							</a>
							,{" "}
							<a className={a} href="/docs/operations/compose-stacks">
								Compose stacks
							</a>
						</>
					),
				},
				{
					q: "How is it different from Coolify or Dokploy?",
					a: (
						<>
							The three are close cousins. NebulaCtrl puts your services on a
							canvas with staged changes, checks health from outside the
							container, verifies through the proxy and puts the old route back
							if that fails, and isolates environments from each other on a
							host. Coolify and Dokploy are ahead on Nixpacks, host bind mounts,
							a raw proxy config editor, community size and years in production.{" "}
							<a className={a} href="/compare">
								The comparison
							</a>{" "}
							lists both directions.
						</>
					),
				},
				{
					q: "Is it ready for production?",
					a: (
						<>
							It is young, so start with something you can afford to move. There
							is a single control plane with no automatic replica promotion, the
							Cloudflare and Tailscale paths have been tested against simulated
							APIs and not a live account, and{" "}
							<a className={a} href="/docs/operations/limitations">
								the limitations page
							</a>{" "}
							lists everything it does not do. Your apps keep running if the
							control plane goes down. The agent runs as root, because the
							Docker socket is root equivalent, and NebulaCtrl assumes a trusted
							team: roles limit what people can do in the product, not what a
							compromised host can do.{" "}
							<a className={a} href="/docs/security/threat-model">
								The threat model
							</a>{" "}
							has the rest.
						</>
					),
				},
				{
					q: "Do I need an AI client?",
					a: (
						<>
							No. An agent can use the CLI, or the MCP server, which is off
							until you enable it and create a scoped token. MCP goes through
							the same checks as the dashboard, edits stage until applied, a
							person approves any change to production, and every call is
							audited. The dashboard has no AI of its own. The docs are also
							published as{" "}
							<a className={a} href="/llms.txt">
								llms.txt
							</a>{" "}
							and as markdown per page for the agents that read them.{" "}
							<a className={a} href="/docs/reference/mcp">
								MCP reference
							</a>
						</>
					),
				},
			],
		},
		{
			id: "running-it",
			title: "Running it",
			items: [
				{
					q: "How are backups handled?",
					a: (
						<>
							Databases back up on a schedule to S3 compatible storage or a
							directory, and restore into a new or the same database. Volumes of
							ordinary services back up with restic. The control plane database
							is dumped daily. There is no point in time recovery and no
							automatic replica promotion, and a directory on the same server is
							not an off site backup; the dashboard says so wherever you choose
							it.{" "}
							<a className={a} href="/docs/operations/backups">
								Backups
							</a>
							,{" "}
							<a className={a} href="/docs/operations/backup-restore">
								control plane backup and restore
							</a>
						</>
					),
				},
				{
					q: "Can I move to another server?",
					a: (
						<>
							Yes, in two ways. To move apps, change a service's placement and
							deploy, or drain a whole server and let NebulaCtrl move every
							service to the others. Volumes are not copied, so move the data of
							databases and other stateful services yourself first. To move the
							control plane, install the same version on the new machine,
							restore the latest control plane backup with your original secret
							key, and point your address at it; agents dial that address.{" "}
							<a className={a} href="/docs/operations/draining-servers">
								Draining servers
							</a>
							,{" "}
							<a className={a} href="/docs/operations/backup-restore">
								backup and restore
							</a>
						</>
					),
				},
				{
					q: "What happens when the control plane is down?",
					a: (
						<>
							Your apps keep running and the proxy on each server keeps serving
							traffic: the control plane never stops remote containers while it
							is down, and that includes restarts and upgrades. What you lose
							until it is back is the dashboard, deploys, logs and metrics in
							the interface. When it returns, each agent reconnects, reports
							what is running and the control plane reconciles it against what
							you asked for. A backup slot that came due while it was down runs
							as soon as it is up.{" "}
							<a className={a} href="/docs/installing/control-plane">
								The control plane
							</a>
							,{" "}
							<a className={a} href="/docs/operations/servers">
								servers and the reconciler
							</a>
						</>
					),
				},
			],
		},
		{
			id: "security-and-data",
			title: "Security and data",
			items: [
				{
					q: "Who owns my data?",
					a: (
						<>
							You do. It lives in a Postgres database on your control plane host
							and in Docker volumes on your servers. Variables are encrypted
							with AES-256-GCM under a key that exists only on your host and is
							never part of a backup. Backups go to a directory or an S3 bucket
							you choose.{" "}
							<a className={a} href="/docs/security/threat-model">
								The threat model
							</a>{" "}
							lists what is protected and what is not.
						</>
					),
				},
				{
					q: "Does it send telemetry?",
					a: (
						<>
							No. NebulaCtrl sends no telemetry and no usage reports. Once a day
							it fetches a small public file from nebulactrl.com to learn
							whether a newer release exists, an anonymous request that carries
							only its version; set{" "}
							<code className={code}>NEBULACTRL_UPDATE_CHECK=false</code> to
							turn it off. Otherwise it contacts only what you configure: your
							Git host, container registries, Let's Encrypt, your DNS provider
							and the services you connect. The installer downloads Docker from
							Docker's repository and looks up the machine's public address from
							an IP lookup service; <code className={code}>--public-ip</code>{" "}
							skips that. If you turn on the request origins map, it downloads
							DB-IP's free location database. This site sets no cookies and runs
							no analytics.
						</>
					),
				},
				{
					q: "How do I back up the secret key?",
					a: (
						<>
							The installer generates one key and keeps it in{" "}
							<code className={code}>/opt/nebulactrl/.env</code> as{" "}
							<code className={code}>NEBULACTRL_SECRET_KEY</code>. Copy it into
							a password manager or vault, apart from your database backups,
							because a backup never contains it. Without the key a restored
							database opens, but every stored secret is unreadable: variables,
							Git and registry credentials, SSH keys and backup passwords. You
							enter them again. The dashboard keeps a reminder card until an
							administrator confirms the key is stored. To change the key, use{" "}
							<code className={code}>nebulactrl-server rekey</code>; never
							replace it by hand.{" "}
							<a className={a} href="/docs/installing/control-plane">
								The control plane
							</a>
							,{" "}
							<a className={a} href="/docs/security/secrets-at-rest">
								secrets at rest
							</a>
						</>
					),
				},
			],
		},
	];
}

/** Every question, in group order. */
export function faqItems(): FaqItem[] {
	return faqGroups().flatMap((group) => group.items);
}
