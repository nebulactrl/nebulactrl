import { Link } from "@tanstack/react-router";
import { BookOpenIcon, BotIcon } from "lucide-react";
import { CopyButton } from "#/components/common/copy-button";
import { useDocumentTitle } from "#/lib/hooks/use-document-title";
import { docMeta, docNav } from "./docs-data";
import { LatestDocsNote } from "./latest-note";
import "./docs.css";

const BLURBS: Record<string, string> = {
	"Getting started":
		"What NebulaCtrl is, the ideas it is built on, and your first deploy.",
	Installing:
		"Put the control plane on a machine, keep it updated, remove it cleanly.",
	Applications:
		"Git and image sources, builds, variables, domains, previews, scaling and tasks.",
	"Databases and backups": "Managed databases, scheduled backups and restores.",
	Templates: "One-click catalogue of self-hosted apps.",
	Servers:
		"Connect hosts, how the agent and proxy work, hardening and clean-up.",
	Observability: "Logs, metrics, analytics and notifications.",
	"Teams and security":
		"Roles, sign-in methods, SSO, secret managers and the threat model.",
	"Platform services": "The services NebulaCtrl can run for itself.",
	Reference: "Every CLI command and API endpoint.",
	Troubleshooting: "When something does not work, and what is not supported.",
	Internals: "Architecture decisions and why they were made.",
};

/** /docs: the overview. Sections with their pages; the first section's pages as "Start here". */
export function DocsLanding() {
	useDocumentTitle(["Docs"]);
	const start = (docNav[0]?.items ?? []).slice(0, 3);
	return (
		<div className="mx-auto w-full max-w-[1000px] px-5 py-10 sm:px-8">
			<div className="type-micro mb-3">Documentation</div>
			<h1 className="m-0 font-display font-semibold text-[32px] text-ink-1 leading-[1.1] tracking-[-0.02em]">
				NebulaCtrl documentation
			</h1>
			<p className="mt-3 max-w-[60ch] text-[15px] text-ink-2 leading-relaxed">
				NebulaCtrl is a self-hosted, Docker-native platform-as-a-service. These
				docs ship inside the control plane you are running, so they always match
				your version.
			</p>
			<LatestDocsNote className="mt-2 text-ink-3 text-xs" />

			<div className="mt-6 grid gap-3 sm:grid-cols-3">
				{start.map((item, index) => {
					const meta = docMeta(item.slug);
					return (
						<Link
							key={item.slug}
							to="/docs/$"
							params={{ _splat: item.slug }}
							className="surface-raised flex flex-col gap-1 rounded-card px-4 py-3.5 no-underline outline-none hover:bg-surface-float focus-visible:focus-ring"
						>
							<span className="type-micro">{`Step ${index + 1}`}</span>
							<span className="font-semibold text-[14px] text-ink-1">
								{item.title}
							</span>
							<span className="text-ink-3 text-xs leading-snug">
								{meta?.description}
							</span>
						</Link>
					);
				})}
			</div>

			<AgentNote />

			<div className="mt-10 grid gap-x-8 gap-y-8 sm:grid-cols-2">
				{docNav.map((section) => (
					<section key={section.id}>
						<h2 className="m-0 mb-1 flex items-center gap-2 font-semibold text-[15px] text-ink-1">
							<BookOpenIcon aria-hidden className="size-3.5 text-ink-4" />
							{section.title}
						</h2>
						<p className="m-0 mb-2 text-ink-3 text-xs leading-snug">
							{BLURBS[section.title]}
						</p>
						<ul className="m-0 flex list-none flex-col gap-0.5 p-0">
							{section.items.slice(0, 6).map((item) => (
								<li key={item.slug}>
									<Link
										to="/docs/$"
										params={{ _splat: item.slug }}
										className="text-[13px] text-ink-2 no-underline hover:text-accent-ink"
									>
										{item.title}
									</Link>
								</li>
							))}
							{section.items.length > 6 ? (
								<li className="text-ink-4 text-xs">
									and {section.items.length - 6} more in the sidebar
								</li>
							) : null}
						</ul>
					</section>
				))}
			</div>
		</div>
	);
}

/** Where an AI agent should start: llms.txt, with the full text and raw pages one step away. */
function AgentNote() {
	const url = `${window.location.origin}/llms.txt`;
	return (
		<section
			aria-labelledby="docs-agents"
			className="surface-well mt-8 flex flex-col gap-2.5 rounded-card px-4 py-3.5"
		>
			<h2
				id="docs-agents"
				className="m-0 flex items-center gap-2 font-semibold text-[14px] text-ink-1"
			>
				<BotIcon aria-hidden className="size-4 text-ink-3" />
				For AI agents
			</h2>
			<p className="m-0 max-w-[68ch] text-[13px] text-ink-2 leading-relaxed">
				Point a coding agent at the index below. Every page also reads as plain
				markdown: add <code className="font-mono text-ink-1">.md</code> to its
				address, or use{" "}
				<a href="/llms-full.txt" className="text-accent-ink">
					llms-full.txt
				</a>{" "}
				for everything in one file. The REST API is described by the{" "}
				<a href="/api/v1/openapi.json" className="text-accent-ink">
					OpenAPI document
				</a>
				.
			</p>
			<div className="flex items-center gap-2">
				<code className="min-w-0 flex-1 truncate rounded-ctl border border-line-faint bg-surface-base px-2.5 py-1.5 font-mono text-[12px] text-ink-1">
					{url}
				</code>
				<CopyButton value={url} variant="chip">
					Copy
				</CopyButton>
			</div>
		</section>
	);
}
