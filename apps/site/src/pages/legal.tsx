import { BlockCode } from "@nebulactrl/dither/interface/block-code";
import { Document } from "../components/chrome.tsx";
import { Island } from "../islands/island.tsx";
import LostSignalScene from "../islands/lost-signal-scene.tsx";
import type { SiteData } from "../server/site-data.ts";
import type { RenderContext } from "../site.ts";

const prose = "docs-prose";

export function PrivacyPage({ ctx }: { ctx: RenderContext }) {
	return (
		<Document
			ctx={ctx}
			current="privacy"
			dawn
			meta={{
				title: "Privacy | NebulaCtrl",
				description:
					"This site collects nothing: no cookies, no analytics, no forms.",
				path: "/privacy",
			}}
		>
			<main
				id="main"
				className="page-surface mx-auto max-w-[1240px] px-4 py-16 sm:px-8 md:py-24"
			>
				<div className={prose}>
					<h1>Privacy</h1>
					<p>This site collects nothing about you.</p>
					<ul>
						<li>
							No cookies, and nothing stored in your browser except what your
							browser caches for speed.
						</li>
						<li>
							No analytics, no advertising, no tracking pixels and no social
							media embeds.
						</li>
						<li>
							No forms and no accounts, so there is nothing to submit and
							nothing to leave behind.
						</li>
						<li>
							No requests to other domains: the fonts, scripts, images and
							search index are all served from this site.
						</li>
					</ul>
					<p>
						This site is hosted on Cloudflare. Like any web host, Cloudflare
						sees your IP address and the pages you request while it answers (see{" "}
						<a
							href="https://www.cloudflare.com/privacypolicy/"
							rel="noopener noreferrer"
						>
							Cloudflare's privacy policy
						</a>
						). We add nothing on top of that.
					</p>
					<h2 id="the-software">The software</h2>
					<p>
						NebulaCtrl itself, the program you install, has no telemetry and no
						usage reports. Once a day it fetches one small public file from this
						site to learn whether a newer release exists. The request is
						anonymous: it carries the program name and its version and nothing
						that identifies you or your servers, and one setting turns it off.
						Apart from that it contacts the services you configure, such as your
						Git host, container registries, Let's Encrypt and your DNS provider.{" "}
						<a href="/docs/security/threat-model">The threat model</a> and the{" "}
						<a href="/docs/installing/installer">installer page</a> list what
						talks to what.
					</p>
					<p>
						Because there is no cookie and no personal data, there is no consent
						banner on this site.
					</p>
					<p className="text-ink-3">Last updated 29 October 2026.</p>
				</div>
			</main>
		</Document>
	);
}

const GNU = "https://www.gnu.org/licenses/agpl-3.0.html";

export function LicencePage({
	ctx,
	data,
}: {
	ctx: RenderContext;
	data: SiteData;
}) {
	const hasThirdParty = data.corpus.entries.has(
		"security/third-party-licences",
	);
	return (
		<Document
			ctx={ctx}
			current="licence"
			dawn
			meta={{
				title: "Licence | NebulaCtrl",
				description:
					"NebulaCtrl is free to use, self hosted and open source, under the GNU Affero General Public License, version 3 only.",
				path: "/licence",
			}}
		>
			<main
				id="main"
				className="page-surface mx-auto max-w-[1240px] px-4 py-16 sm:px-8 md:py-24"
			>
				<div className={prose}>
					<h1>Licence</h1>
					<p>
						NebulaCtrl is free to use, self hosted and open source. Its source
						is released under the{" "}
						<strong>GNU Affero General Public License, version 3 only</strong>{" "}
						(SPDX identifier <code>AGPL-3.0-only</code>).
					</p>
					<h2 id="in-short">In short</h2>
					<ul>
						<li>
							You can run NebulaCtrl on your own servers, for yourself or for
							your company, at no cost.
						</li>
						<li>You can read, change and share the source.</li>
						<li>
							If you share a changed version, or let other people use a changed
							version over a network, you offer them the source of your changes
							under the same licence.
						</li>
						<li>There is no warranty.</li>
					</ul>
					<p>This is a summary, not legal advice. The licence text decides.</p>
					<h2 id="text">The text</h2>
					<p>
						The canonical text is at{" "}
						<a href={GNU} rel="noopener noreferrer">
							gnu.org
						</a>
						{data.licenceText ? ", and a copy of it follows." : "."}
					</p>
					{hasThirdParty ? (
						<p>
							The dependencies NebulaCtrl is built from have their own licences,
							listed on{" "}
							<a href="/docs/security/third-party-licences">
								third party licences
							</a>
							.
						</p>
					) : null}
					{data.licenceText ? (
						<details>
							<summary>Show the full licence text</summary>
							<pre className="surface-well mt-4 overflow-x-auto whitespace-pre-wrap rounded-xl p-4 font-mono text-ink-2 text-xs">
								{data.licenceText}
							</pre>
						</details>
					) : null}
					{ctx.config.githubUrl ? (
						<p>
							The source is on <a href={ctx.config.githubUrl}>GitHub</a>.
						</p>
					) : null}
				</div>
			</main>
		</Document>
	);
}

/**
 * The 404 over the Lost signal scene: static glyph noise with a
 * gap drifting slowly where the page should be. The message is real text; the
 * gap opens around it (`data-ascii-panel`) and the text is also marked quiet,
 * so the noise stays under 8% behind it even where the gap is clamped. The
 * halftone rim stays as the static floor and is all there is without script
 * or WebGL2.
 */
export function NotFoundPage({ ctx }: { ctx: RenderContext }) {
	return (
		<Document
			ctx={ctx}
			current="none"
			meta={{
				title: "Page not found | NebulaCtrl",
				description: "There is no page at this address.",
				path: "/404",
				noindex: true,
			}}
		>
			<main
				id="main"
				data-ascii-scope
				className="page-surface ht-layer ht-rim relative isolate flex min-h-[760px] items-center justify-center overflow-hidden"
			>
				<Island
					name="lost-signal-scene"
					mode="visible"
					className="scene-fade-bottom pointer-events-none absolute inset-0 z-0"
				>
					<LostSignalScene />
				</Island>
				<div className="relative z-10 w-full px-4 py-28 sm:px-8">
					<div
						data-ascii-panel
						data-ascii-quiet
						className="mx-auto flex w-full max-w-[520px] flex-col items-center text-center"
					>
						<div
							aria-hidden
							className="mb-8 w-full max-w-[400px] min-w-0 font-semibold text-ink-1"
						>
							<Island
								name="block-code"
								props={{ code: "404" }}
								className="block"
							>
								<BlockCode code="404" cell={4} depth={5} motion="glitch" />
							</Island>
						</div>
						<h1 className="m-0 font-semibold text-4xl text-ink-1 tracking-tight sm:text-5xl">
							No page at this address.
						</h1>
						<p className="m-0 mt-6 max-w-[480px] text-ink-2 text-lg">
							The link may be old, or the page may never have existed. Start
							from the home page, or search the docs.
						</p>
						<div className="mt-8 flex flex-wrap justify-center gap-3">
							<a
								href="/"
								className="btn-primary pressable rounded-lg px-4 py-2 font-semibold text-base no-underline"
							>
								Back to the home page
							</a>
							<a
								href="/docs"
								className="btn-secondary pressable rounded-lg px-4 py-2 font-semibold text-base text-ink-1 no-underline"
							>
								Read the docs
							</a>
						</div>
					</div>
				</div>
			</main>
		</Document>
	);
}
