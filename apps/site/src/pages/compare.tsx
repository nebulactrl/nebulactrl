import { DitherProgressBar } from "@nebulactrl/dither/charts/progress-bar";
import { Document } from "../components/chrome.tsx";
import {
	type Cell,
	GROUPS,
	type Group,
	type Mark,
	PRODUCTS,
	type Product,
	ROWS,
	SNAPSHOT,
	TOTALS,
	type Totals,
	TRAILS,
} from "../content/comparison.ts";
import { installCommand, type RenderContext } from "../site.ts";
import { FinalCta } from "./landing/closing.tsx";

const container = "mx-auto max-w-[1240px] px-4 sm:px-8";
const link = "text-accent-ink";

const MARK_LABEL: Record<Mark, string> = {
	yes: "Yes",
	partial: "Partial",
	no: "No",
	na: "n/a",
};
const MARK_TEXT: Record<Mark, string> = {
	yes: "text-status-live-on",
	partial: "text-status-staged-on",
	no: "text-ink-3",
	na: "text-ink-4",
};

/** One cell of the matrix: a mark with its icon, a dagger when the feature was not found in the vendor docs, and an optional note. */
function MarkView({ cell }: { cell: Cell }) {
	return (
		<div className="flex flex-col gap-1">
			<span
				data-mark={cell.mark}
				className={`mark inline-flex items-center gap-1 font-semibold text-sm ${MARK_TEXT[cell.mark]}`}
			>
				{MARK_LABEL[cell.mark]}
				{cell.notFound ? (
					<>
						<sup aria-hidden="true">†</sup>
						<span className="sr-only"> (not found in the vendor docs)</span>
					</>
				) : null}
			</span>
			{cell.note ? (
				<span className="text-ink-3 text-xs">{cell.note}</span>
			) : null}
		</div>
	);
}

/**
 * A header cell stays in view: inside the scrolling frame on a phone, under the
 * floating nav (top-20) once the table is as tall as the page from lg up.
 */
const HEAD_CELL =
	"sticky top-0 bg-void px-4 py-3 text-left font-semibold text-sm shadow-[inset_0_-1px_0_var(--color-line)] lg:top-20";

function CompareGroup({ group }: { group: Group }) {
	return (
		<tbody id={group.id}>
			<tr>
				<th
					colSpan={5}
					scope="colgroup"
					className="bg-surface-well px-4 py-3 text-left font-semibold text-ink-1 text-sm"
				>
					<span className="sticky left-4 inline-block">{group.title}</span>
				</th>
			</tr>
			{group.rows.map((row) => (
				<tr key={row.row} className="border-line border-t align-top">
					<th
						scope="row"
						className="sticky left-0 z-10 min-w-[160px] bg-void px-4 py-3 text-left font-normal text-ink-1 text-sm md:min-w-[220px]"
					>
						{row.label}
					</th>
					<td className="min-w-[140px] bg-white/[0.03] px-4 py-3">
						<MarkView cell={row.nebula} />
					</td>
					<td className="min-w-[140px] px-4 py-3">
						<MarkView cell={row.coolify} />
					</td>
					<td className="min-w-[140px] px-4 py-3">
						<MarkView cell={row.dokploy} />
					</td>
					<td className="min-w-[140px] px-4 py-3">
						<MarkView cell={row.railway} />
					</td>
				</tr>
			))}
		</tbody>
	);
}

/** The matrix as one accessible table: a `<tbody>` per section, the capability as a sticky row header. */
function CompareTable({ groups = GROUPS }: { groups?: Group[] }) {
	return (
		<>
			<p className="m-0 mb-3 text-ink-3 text-sm lg:hidden">
				Scroll sideways to see every product. The capability names stay in view.
			</p>
			{/* biome-ignore lint/a11y/useSemanticElements: a scrollable table region, not a document section; <section> would split the page outline. */}
			<div
				role="region"
				aria-label="Capability comparison table"
				// biome-ignore lint/a11y/noNoninteractiveTabindex: a scrollable table must be reachable by keyboard.
				tabIndex={0}
				className="relative max-h-[75svh] overflow-auto rounded-2xl border border-line outline-none focus-visible:focus-ring lg:max-h-none lg:overflow-clip"
			>
				<table className="w-full min-w-[820px] border-collapse text-left">
					<caption className="sr-only">
						Capability comparison of NebulaCtrl, Coolify, Dokploy and Railway
					</caption>
					<thead>
						<tr>
							<th scope="col" className={`${HEAD_CELL} left-0 z-30 text-ink-3`}>
								Capability
							</th>
							<th scope="col" className={`${HEAD_CELL} z-20 text-ink-1`}>
								NebulaCtrl
							</th>
							<th scope="col" className={`${HEAD_CELL} z-20 text-ink-3`}>
								Coolify
							</th>
							<th scope="col" className={`${HEAD_CELL} z-20 text-ink-3`}>
								Dokploy
							</th>
							<th scope="col" className={`${HEAD_CELL} z-20 text-ink-3`}>
								Railway
							</th>
						</tr>
					</thead>
					{groups.map((group) => (
						<CompareGroup key={group.id} group={group} />
					))}
				</table>
			</div>
		</>
	);
}

/** The counts per product as dithered bars. The totals come from the matrix, so they cannot drift from the table below. */
function CompareSummary({
	totals = TOTALS,
}: {
	totals?: Record<Product, Totals>;
}) {
	const rows = Object.values(totals.nebula).reduce((sum, n) => sum + n, 0);
	return (
		<section aria-labelledby="summary" className="mt-12">
			<h2 id="summary" className="display-title m-0">
				The count, out of {rows} capabilities
			</h2>
			<div
				data-bars
				className="surface-raised mt-6 grid grid-cols-1 gap-6 rounded-2xl p-6 md:grid-cols-2"
			>
				{PRODUCTS.map(({ id, name }) => {
					const t = totals[id];
					return (
						<div key={id} className="flex flex-col gap-1">
							<DitherProgressBar
								label={name}
								parts={[
									{
										label: "Yes",
										value: t.yes,
										text: String(t.yes),
										color: "green",
									},
									{
										label: "Partial",
										value: t.partial,
										text: String(t.partial),
										color: "orange",
									},
									{
										label: "No",
										value: t.no,
										text: String(t.no),
										color: "grey",
									},
								]}
								max={rows}
								valueText={`${t.yes} of ${rows}`}
								size="lg"
								animate={false}
							/>
							<p className="m-0 text-ink-3 text-xs">
								{t.na} not applicable to this product
							</p>
						</div>
					);
				})}
			</div>
		</section>
	);
}

export function ComparePage({ ctx }: { ctx: RenderContext }) {
	const { siteUrl } = ctx.config;
	const title =
		"Coolify alternative: NebulaCtrl compared with Coolify, Dokploy and Railway";
	const description = `A row by row comparison of NebulaCtrl, Coolify, Dokploy and Railway across ${ROWS.length} capabilities, with the ${TRAILS.length} where NebulaCtrl does not say yes.`;
	return (
		<Document
			ctx={ctx}
			current="compare"
			dawn
			meta={{
				title,
				description,
				path: "/compare",
				jsonLd: [
					{
						"@context": "https://schema.org",
						"@type": "WebPage",
						name: title,
						description,
						url: `${siteUrl}/compare`,
						dateModified: SNAPSHOT,
						isPartOf: { "@type": "WebSite", name: "NebulaCtrl", url: siteUrl },
					},
					{
						"@context": "https://schema.org",
						"@type": "BreadcrumbList",
						itemListElement: [
							{
								"@type": "ListItem",
								position: 1,
								name: "NebulaCtrl",
								item: `${siteUrl}/`,
							},
							{
								"@type": "ListItem",
								position: 2,
								name: "Compare",
								item: `${siteUrl}/compare`,
							},
						],
					},
				],
			}}
		>
			<main id="main" className="page-surface pt-28">
				<div className="pb-24">
					<div className={container}>
						<h1 className="display-2 m-0 max-w-[820px] text-ink-1">
							Coolify alternative: where NebulaCtrl leads and where it trails
						</h1>
						<div className="mt-6 flex max-w-[680px] flex-col gap-4 text-ink-2">
							<p className="m-0">
								NebulaCtrl is a free, self hosted platform for your own Docker
								servers, open source under the AGPL-3.0, in the same family as
								Coolify and Dokploy. This page sets the three side by side, with
								Railway as the hosted option, across {ROWS.length} capabilities.
								Each cell says yes, partial, no or not applicable, and the table
								lists every row.
							</p>
							<p className="m-0">
								Coolify and Dokploy are ahead on Nixpacks, host bind mounts, a
								raw proxy config editor and, for Dokploy, Swarm mode. Railway
								has private networking across hosts, point in time database
								recovery and automatic database replica promotion; NebulaCtrl
								has an opt in mesh for links between servers, marked partial.
								Coolify and Dokploy also have larger communities and more years
								in production. NebulaCtrl does not say yes on {TRAILS.length}{" "}
								rows, and they are all marked below.
							</p>
						</div>
						<p className="m-0 mt-6">
							<a
								href="#install-again"
								className="btn-primary pressable inline-flex h-10 items-center rounded-lg px-4 font-semibold text-base no-underline"
							>
								Install NebulaCtrl
							</a>
						</p>
						<CompareSummary totals={TOTALS} />
						<section aria-labelledby="matrix" className="mt-12">
							<h2 id="matrix" className="display-title m-0">
								Every capability, side by side
							</h2>
							<div className="mt-6">
								<CompareTable />
							</div>
							<p className="m-0 mt-6 max-w-[680px] text-ink-3 text-sm">
								Checked against the code and each vendor's documentation on{" "}
								{SNAPSHOT}. Competitor cells come from vendor docs; † means the
								feature was searched for there and not found, which is weaker
								than proof that it is missing. Where NebulaCtrl says partial or
								no, the note says what is missing.
							</p>
							<p className="m-0 mt-4 max-w-[680px] text-ink-2 text-sm">
								Moving from another platform?{" "}
								<a className={link} href="/faq">
									The FAQ
								</a>{" "}
								covers what you need, what the installer changes and how to move
								servers, or read{" "}
								<a className={link} href="/docs/getting-started/quickstart">
									the quickstart
								</a>
								.
							</p>
						</section>
					</div>
				</div>
				<FinalCta command={installCommand(ctx.config.siteUrl)} />
			</main>
		</Document>
	);
}
