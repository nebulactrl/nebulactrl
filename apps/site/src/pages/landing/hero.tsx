import { ArrowRightIcon } from "@phosphor-icons/react/dist/csr/ArrowRight";
import type { CSSProperties } from "react";
import HeroEclipse from "../../islands/hero-eclipse.tsx";
import { Island } from "../../islands/island.tsx";
import { installCommand, type RenderContext } from "../../site.ts";
import { container, InstallWell, link } from "./shared.tsx";

/** Where a part sits in the hero's load sequence: 70 ms after the one before it. */
function order(i: number): CSSProperties {
	return { "--i": i } as CSSProperties;
}

/**
 * The hero: the headline in two tones, one subline, the install command with a
 * line on what it does, and two links. Behind it is the page's one scene, the
 * Eclipse (a pixel-dithered ringed planet), over a static halftone floor that
 * is the whole picture without script or WebGL2. The copy column is the
 * scene's panel and its quiet region: the planet takes the free side of the
 * window and the field is cleared behind the text (under 8% contrast).
 *
 * With a script and no reduced motion the parts arrive once in a row (`.hl` in
 * `site.css`); the server markup is the final state.
 */
export function Hero({ ctx }: { ctx: RenderContext }) {
	const command = installCommand(ctx.config.siteUrl);
	return (
		<section
			id="top-screen"
			data-ascii-scope
			className="relative isolate overflow-hidden"
		>
			<div
				aria-hidden
				className="ht-layer ht-hero pointer-events-none absolute inset-0 -z-20"
			/>
			<Island
				name="hero-eclipse"
				mode="visible"
				className="hero-eclipse pointer-events-none absolute inset-0 -z-10"
			>
				<HeroEclipse />
			</Island>
			{/* On a phone the copy fills the width: the bottom padding is the stage the planet rises on. */}
			<div
				className={`${container} relative pt-36 pb-[max(8rem,70vw)] md:pt-48 md:pb-44`}
			>
				<div className="w-fit max-w-[40rem]" data-ascii-panel data-ascii-quiet>
					<h1 className="hl display-1 m-0" style={order(0)}>
						<span className="block text-ink-1">Deploy to servers you own.</span>{" "}
						<span className="block text-ink-3">Stay in control.</span>
					</h1>
					<p
						className="hl m-0 mt-6 max-w-[44ch] text-ink-2 text-lg md:mt-8"
						style={order(1)}
					>
						A free deploy platform for your Docker servers: Git or image,
						verified releases, databases and backups. Bring your AI agent over
						MCP or the CLI if you like.
					</p>
					<div className="hl mt-8" style={order(2)}>
						<InstallWell command={command} id="install" compact />
						<p className="m-0 mt-3 text-ink-3 text-sm">
							Paste it on a Linux server as root.{" "}
							<a className={link} href="/install.sh">
								Read the script
							</a>{" "}
							or see{" "}
							<a className={link} href="/docs/installing/installer">
								what it changes
							</a>{" "}
							first.
						</p>
					</div>
					<div
						className="hl mt-7 flex flex-wrap items-center gap-2"
						style={order(3)}
					>
						<a
							href="/docs"
							className="btn-secondary pressable inline-flex h-10 items-center rounded-lg px-4 font-medium text-sm no-underline"
						>
							Read the docs
						</a>
						<a
							href="/compare"
							className="btn-secondary pressable inline-flex h-10 items-center gap-2 rounded-lg px-4 font-medium text-sm no-underline"
						>
							How it compares
							<ArrowRightIcon aria-hidden className="size-4" />
						</a>
					</div>
				</div>
			</div>
		</section>
	);
}
