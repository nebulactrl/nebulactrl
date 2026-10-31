/**
 * The comparison matrix: all 100 rows, in its sections; `row` is the row
 * number. Competitor cells come from each vendor's documentation, read on 26
 * to 28 Oct 2026; the Coolify cells that its docs have not caught up with (passkeys, maintenance
 * mode, the file browser, the dashboard canvas) come from its v4.5.0 and v4.5.1
 * release notes. A "No" with a dagger means the feature was not found in their
 * docs, which is weaker than proof that it is absent. NebulaCtrl cells carry a
 * note where the answer is partial or no, and where a yes has a limit worth
 * stating; the evidence behind the others stays in the repository.
 */
export type Mark = "yes" | "partial" | "no" | "na";
export interface Cell {
	mark: Mark;
	/** A short qualifier shown under the mark. */
	note?: string;
	/** Searched in the vendor docs and not found. */
	notFound?: boolean;
}
export interface Row {
	row: string;
	label: string;
	nebula: Cell;
	coolify: Cell;
	dokploy: Cell;
	railway: Cell;
}
export interface Group {
	id: string;
	title: string;
	rows: Row[];
}
export type Product = "nebula" | "coolify" | "dokploy" | "railway";
export type Totals = Record<Mark, number>;

/** The date the matrix was last checked against the code and the vendor docs. */
export const SNAPSHOT = "2026-10-28";

const yes = (note?: string): Cell =>
	note ? { mark: "yes", note } : { mark: "yes" };
const part = (note?: string): Cell =>
	note ? { mark: "partial", note } : { mark: "partial" };
const no = (note?: string): Cell =>
	note ? { mark: "no", note } : { mark: "no" };
const nf: Cell = { mark: "no", notFound: true };
const na: Cell = { mark: "na" };

const row = (
	n: string,
	label: string,
	nebula: Cell,
	coolify: Cell,
	dokploy: Cell,
	railway: Cell,
): Row => ({ row: n, label, nebula, coolify, dokploy, railway });

export const GROUPS: Group[] = [
	{
		id: "sources-and-builds",
		title: "Sources and builds",
		rows: [
			row("1", "Generic Git, public and private", yes(), yes(), yes(), part()),
			row("2", "GitHub App", yes(), yes(), yes(), yes()),
			row(
				"3",
				"GitLab, Bitbucket and Gitea integrations",
				yes(),
				yes(),
				yes(),
				nf,
			),
			row("4", "Push webhooks and auto deploy", yes(), yes(), yes(), yes()),
			row("5", "Watch paths (monorepo)", yes(), yes(), yes(), yes()),
			row("6", "PR preview environments", yes(), yes(), yes(), yes()),
			row("7", "Dockerfile builds", yes(), yes(), yes(), yes()),
			row(
				"8",
				"Nixpacks",
				no("Not supported; Railpack is the default"),
				yes("Deprecated in 4.5"),
				yes(),
				no(),
			),
			row("9", "Railpack", yes(), part("Beta"), yes(), yes()),
			row(
				"10",
				"Cloud Native Buildpacks",
				no("No buildpack support"),
				no(),
				yes(),
				nf,
			),
			row("11", "Static site build", yes(), yes(), yes(), yes()),
			row("12", "Docker Compose stacks", yes(), yes(), yes(), part()),
			row("13", "Separate build server", yes(), yes(), yes(), na),
			row("14", "Private registry, pull and push", yes(), yes(), yes(), yes()),
		],
	},
	{
		id: "deploy",
		title: "Deploy",
		rows: [
			row("15", "Zero downtime rolling deploys", yes(), part(), part(), yes()),
			row("16", "Rollback", yes(), yes(), yes(), yes()),
			row("17", "Scheduled jobs / cron", yes(), yes(), yes(), yes()),
			row(
				"18",
				"One off command, terminal and exec",
				yes(),
				yes(),
				yes(),
				yes(),
			),
			row(
				"19",
				"Pre and post deploy commands",
				yes(),
				yes(),
				nf,
				yes("Pre deploy only"),
			),
			row("20", "Health checks", yes(), yes(), part(), yes()),
			row(
				"21",
				"Cancel a deployment, queued or running",
				yes(),
				yes(),
				part(),
				yes(),
			),
		],
	},
	{
		id: "runtime",
		title: "Runtime",
		rows: [
			row("22", "Replicas", yes(), part(), yes(), yes()),
			row("23", "Autoscaling", yes(), nf, nf, part()),
			row("24", "CPU / memory limits", yes(), yes(), yes(), yes()),
			row("25", "Persistent named volumes", yes(), yes(), yes(), yes()),
			row(
				"26",
				"Bind mounts and file mounts",
				part("Config files only, no host paths"),
				yes(),
				yes(),
				nf,
			),
			row("27", "Custom Docker options and labels", yes(), yes(), part(), na),
		],
	},
	{
		id: "networking",
		title: "Networking",
		rows: [
			row("28", "Domains + automatic TLS", yes(), yes(), yes(), yes()),
			row("29", "Generated domains", yes(), yes(), yes(), yes()),
			row("30", "Wildcard hostnames / certs", yes(), yes(), part(), yes()),
			row("31", "Custom certificate upload", yes(), part(), yes(), nf),
			row("32", "DNS-01 and DNS providers", yes(), part(), part(), na),
			row("33", "Redirects", yes(), yes(), yes(), yes()),
			row("34", "Path routing and strip prefix", yes(), yes(), yes(), nf),
			row("35", "Basic auth", yes(), yes(), yes(), nf),
			row("36", "Rate limiting", yes(), part(), part(), nf),
			row("37", "IP allow and deny lists", yes(), part(), part(), yes()),
			row("38", "Custom headers, CORS and noindex", yes(), part(), part(), nf),
			row("39", "TCP/UDP exposure", yes(), part(), yes(), part()),
			row("40", "Proxy to external upstream", yes(), part(), part(), nf),
			row(
				"41",
				"Raw proxy config editor",
				no("Typed options only, no raw editor"),
				yes(),
				yes(),
				no(),
			),
			row("42", "Private networking, same host", yes(), yes(), yes(), yes()),
			row(
				"43",
				"Private networking, cross host",
				part("Optional tailnet mesh, TCP only, one environment"),
				no(),
				part(),
				yes(),
			),
			row("44", "Cloudflare Tunnel", yes(), yes(), yes(), na),
			row("45", "Tailscale", yes(), part(), part(), na),
		],
	},
	{
		id: "data",
		title: "Data",
		rows: [
			row(
				"46",
				"Managed DB engines",
				yes(),
				yes("9 engines"),
				yes("5 engines"),
				yes(),
			),
			row("47", "Scheduled backups to S3", yes(), yes(), yes(), part()),
			row("48", "Restore from UI", yes(), yes(), yes(), yes()),
			row(
				"49",
				"Point in time recovery",
				no("Backups only, no WAL archiving"),
				no("Stated as missing in their docs"),
				nf,
				yes(),
			),
			row("50", "Public DB exposure", yes(), yes(), yes(), yes()),
			row("51", "Volume backups, not databases", yes(), yes(), yes(), yes()),
			row(
				"52",
				"Automatic replica promotion",
				no("Manual promote only, no automatic replica promotion"),
				no("Stated as missing in their docs"),
				nf,
				yes(),
			),
		],
	},
	{
		id: "operations-and-platform",
		title: "Operations and platform",
		rows: [
			row("53", "Live logs", yes(), yes(), yes(), yes()),
			row("54", "Log drains / external shipping", yes(), yes(), yes(), part()),
			row("55", "Metrics", yes(), yes(), yes(), yes()),
			row("56", "Traffic analytics", yes(), yes(), yes(), part()),
			row("57", "Notifications / alerts", yes(), yes(), yes(), part()),
			row("58", "Container file browser", yes(), yes(), yes(), yes()),
			row("59", "Multiple servers", yes(), yes(), yes(), na),
			row(
				"60",
				"Swarm / cluster mode",
				no("No cluster scheduler, by design"),
				part(),
				yes(),
				na,
			),
			row("61", "Teams / RBAC", yes(), yes(), yes(), yes()),
			row("62", "API, CLI and MCP", yes(), yes(), part(), yes()),
			row(
				"63",
				"One click templates",
				yes(),
				yes("300+ templates"),
				yes(),
				yes(),
			),
			row("64", "Environments", yes(), yes(), yes(), yes()),
			row("65", "Shared variables", yes(), yes(), yes(), yes()),
			row("66", "Variable references", yes(), part(), yes(), yes()),
			row(
				"67",
				"Encrypted secrets and external secret managers",
				yes(),
				yes(),
				yes(),
				yes(),
			),
			row("68", "Secret generation", yes(), yes(), nf, yes()),
			row("69", "Audit log", yes(), yes(), part(), part()),
			row("70", "Control plane backup", yes(), yes(), yes(), na),
			row("71", "SSO and OAuth login", yes(), yes(), part(), part()),
			row("72", "2FA and passkeys", yes(), yes(), yes(), yes()),
			row("73", "Server drain and fleet capacity", yes(), nf, nf, na),
		],
	},
	{
		id: "canvas-staging-and-safety",
		title: "Canvas, staging and safety",
		rows: [
			row(
				"74",
				"Visual project canvas with service cards and cables",
				yes(),
				part("Dashboard canvas since 4.5"),
				nf,
				yes(),
			),
			row(
				"75",
				"Staged changes with a diff, deployed together",
				yes(),
				nf,
				nf,
				yes(),
			),
			row(
				"76",
				"Guarded production deploy",
				yes("Enforced for MCP; tokens opt in"),
				nf,
				nf,
				nf,
			),
			row("77", "Per service maintenance mode", yes(), yes(), nf, nf),
			row("78", "Branded 404, 502 and 503 error pages", yes(), yes(), nf, nf),
			row("79", "Request origins map", yes(), yes(), nf, nf),
			row(
				"80",
				"MCP server: staged edits, production approval, scoped tools, off by default",
				yes(),
				yes(),
				nf,
				yes(),
			),
			row("81", "View transition navigation", yes(), na, na, na),
			row("82", "Dither visualisation kit and alert tones", yes(), na, na, na),
			row(
				"83",
				"Verify through the proxy, restore the old route on failure",
				yes(),
				nf,
				nf,
				nf,
			),
			row(
				"84",
				"Database credential rotation and in place major upgrades",
				yes(),
				no(),
				nf,
				part(),
			),
			row(
				"85",
				"Variable version history and rotation plan",
				yes(),
				nf,
				nf,
				nf,
			),
		],
	},
	{
		id: "cloudflare-and-network-isolation",
		title: "Cloudflare and network isolation",
		rows: [
			row("86", "Cloudflare DNS automation", yes(), yes(), part(), no()),
			row(
				"87",
				"Cloudflare Tunnel, managed per server",
				yes(),
				part(),
				no(),
				na,
			),
			row(
				"88",
				"Network isolation per environment",
				yes(),
				part(),
				part(),
				yes(),
			),
			row("89", "Shared networks", yes(), part(), nf, no()),
			row("90", "Egress blocking per service", yes(), nf, nf, nf),
		],
	},
	{
		id: "server-and-account-tools",
		title: "Server and account tools",
		rows: [
			row("91", "Server host shell", yes(), yes(), yes(), na),
			row("92", "Scheduled Docker cleanup", yes(), yes(), part(), na),
			row("93", "Forgot password by email", yes(), yes(), nf, na),
			row("94", ".env.example prefill", yes(), nf, nf, yes()),
		],
	},
	{
		id: "install-hardening-and-docs",
		title: "Install, hardening and docs",
		rows: [
			row(
				"95",
				"Deploy hook URL and registry push hooks",
				yes(),
				part(),
				part(),
				nf,
			),
			row("96", "One line installer", yes(), part(), part(), na),
			row("97", "Host hardening audit with Lynis", yes(), nf, part(), na),
			row("98", "Managed platform services", yes(), part(), nf, na),
			row(
				"99",
				"Machine readable docs, served by the app",
				yes(),
				part(),
				part(),
				yes(),
			),
			row("100", "Compose stack runtime safety allow list", yes(), nf, nf, na),
		],
	},
];

export const PRODUCTS: { id: Product; name: string }[] = [
	{ id: "nebula", name: "NebulaCtrl" },
	{ id: "coolify", name: "Coolify" },
	{ id: "dokploy", name: "Dokploy" },
	{ id: "railway", name: "Railway" },
];

/** Every row, in matrix order. */
export const ROWS: Row[] = GROUPS.flatMap((group) => group.rows);

function count(product: Product): Totals {
	const totals: Totals = { yes: 0, partial: 0, no: 0, na: 0 };
	for (const row of ROWS) totals[row[product].mark] += 1;
	return totals;
}

/** Counts per product over all rows, so pages and charts cannot drift from the table. */
export const TOTALS: Record<Product, Totals> = {
	nebula: count("nebula"),
	coolify: count("coolify"),
	dokploy: count("dokploy"),
	railway: count("railway"),
};

/** Every row where NebulaCtrl does not say yes. */
export const TRAILS: Row[] = ROWS.filter((row) => row.nebula.mark !== "yes");
