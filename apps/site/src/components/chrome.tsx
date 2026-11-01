import { NebulaMark } from "@nebulactrl/dither/images/nebula-mark-g";
import { ArrowUpIcon } from "@phosphor-icons/react/dist/csr/ArrowUp";
import type { ReactNode } from "react";
import { Island } from "../islands/island.tsx";
import MenuToggle from "../islands/menu-toggle.tsx";
import { FactChips } from "../pages/landing/shared.tsx";
import { DESCRIPTION, type RenderContext, SITE_NAME } from "../site.ts";

export type NavId =
	| "landing"
	| "compare"
	| "faq"
	| "docs"
	| "privacy"
	| "licence"
	| "none";

interface Meta {
	title: string;
	description?: string;
	/** Path with a leading slash; the canonical URL is SITE_URL plus this. */
	path: string;
	noindex?: boolean;
	ogType?: "website" | "article";
	jsonLd?: unknown[];
}

const THEME_COLOR = "#121417";

/** The one inline script of every page: the no-js to js class swap. Its sha256 is in public/_headers. */
export const NO_JS_SNIPPET =
	"document.documentElement.className=document.documentElement.className.replace('no-js','js')";

/** A complete HTML document. React 19 hoists nothing here: the head is written out. */
export function Document({
	ctx,
	meta,
	current,
	landing = false,
	floating = true,
	dawn = false,
	children,
}: {
	ctx: RenderContext;
	meta: Meta;
	current: NavId;
	landing?: boolean;
	/** The top bar is fixed over the page. The docs keep it in the flow so their sticky bar sits at the top. */
	floating?: boolean;
	/** A faint halftone dawn at the top of the page, behind the header area (`.ht-dawn`, `site.css`): the docs, compare, FAQ and legal pages. The home page has its own floors under its nebulas, and the 404 its rim. */
	dawn?: boolean;
	children: ReactNode;
}) {
	const { config, assets } = ctx;
	const url =
		`${config.siteUrl}${meta.path === "/" ? "" : meta.path}` || config.siteUrl;
	const canonical = meta.path === "/" ? `${config.siteUrl}/` : url;
	const description = meta.description ?? DESCRIPTION;
	const ogImage = `${config.siteUrl}/og.png`;
	return (
		<html lang="en" className="no-js">
			<head>
				<meta charSet="utf-8" />
				<meta name="viewport" content="width=device-width, initial-scale=1" />
				<title>{meta.title}</title>
				<meta name="description" content={description} />
				<meta name="color-scheme" content="dark" />
				<meta name="theme-color" content={THEME_COLOR} />
				{meta.noindex ? (
					<meta name="robots" content="noindex" />
				) : (
					<link rel="canonical" href={canonical} />
				)}
				<link rel="icon" href="/favicon.svg" type="image/svg+xml" />
				<link rel="icon" href="/favicon.ico" sizes="any" />
				<meta property="og:site_name" content={SITE_NAME} />
				<meta property="og:type" content={meta.ogType ?? "website"} />
				<meta property="og:title" content={meta.title} />
				<meta property="og:description" content={description} />
				<meta property="og:url" content={canonical} />
				<meta property="og:image" content={ogImage} />
				<meta property="og:image:width" content="1200" />
				<meta property="og:image:height" content="630" />
				<meta
					property="og:image:alt"
					content="The NebulaCtrl canvas: three live services and the cables between them."
				/>
				<meta name="twitter:card" content="summary_large_image" />
				<meta name="twitter:title" content={meta.title} />
				<meta name="twitter:description" content={description} />
				<meta name="twitter:image" content={ogImage} />
				<link
					rel="alternate"
					type="text/plain"
					href="/llms.txt"
					title="llms.txt"
				/>
				{assets.fonts.map((href) => (
					<link
						key={href}
						rel="preload"
						href={href}
						as="font"
						type="font/woff2"
						crossOrigin="anonymous"
					/>
				))}

				<script
					// Sets the js class before first paint so JS-only controls never flash.
					// The Content-Security-Policy in public/_headers allows it by hash.
					// biome-ignore lint/security/noDangerouslySetInnerHtml: a constant string
					dangerouslySetInnerHTML={{ __html: NO_JS_SNIPPET }}
				/>
				{landing && assets.inlineCss ? (
					// biome-ignore lint/security/noDangerouslySetInnerHtml: our own built stylesheet
					<style dangerouslySetInnerHTML={{ __html: assets.inlineCss }} />
				) : (
					assets.css.map((href) => (
						<link key={href} rel="stylesheet" href={href} />
					))
				)}
				{meta.jsonLd?.map((data, i) => (
					<script
						// biome-ignore lint/suspicious/noArrayIndexKey: static list
						key={i}
						type="application/ld+json"
						// biome-ignore lint/security/noDangerouslySetInnerHtml: JSON built from our own data, "<" escaped
						dangerouslySetInnerHTML={{
							__html: JSON.stringify(data).replaceAll("<", "\\u003c"),
						}}
					/>
				))}
			</head>
			<body id="top" className={dawn ? "ht-dawn" : undefined}>
				<a
					href="#main"
					className="btn-primary sr-only z-50 rounded-lg px-4 py-2 font-semibold text-sm focus:not-sr-only focus:fixed focus:top-2 focus:left-2"
				>
					Skip to content
				</a>
				<SiteHeader ctx={ctx} current={current} floating={floating} />
				{children}
				<SiteFooter ctx={ctx} />
				<script type="module" src={assets.clientJs} />
			</body>
		</html>
	);
}

/** The NebulaCtrl mark, variant G: embossed over a dithered shadow from 24 px, solid below and in forced colours. */
function LogoMark({ size = 28 }: { size?: number }) {
	return <NebulaMark size={size} className="block" />;
}

/** 1234 as "1.2k", 87 as "87". */
function compactCount(n: number): string {
	return n >= 1000
		? `${(n / 1000).toFixed(1).replace(/\.0$/, "")}k`
		: String(n);
}

function navLinks(ctx: RenderContext, current: NavId) {
	const links: {
		href: string;
		label: string;
		current?: boolean;
		/** A count shown after the label, such as the stars. */
		suffix?: string;
	}[] = [
		{ href: "/#features", label: "Features" },
		{ href: "/compare", label: "Compare", current: current === "compare" },
		{ href: "/docs", label: "Docs", current: current === "docs" },
		{ href: "/faq", label: "FAQ", current: current === "faq" },
	];
	if (ctx.config.githubUrl)
		links.push({
			href: ctx.config.githubUrl,
			label: "GitHub",
			...(ctx.config.githubStars
				? { suffix: compactCount(ctx.config.githubStars) }
				: {}),
		});
	return links;
}

function SiteHeader({
	ctx,
	current,
	floating,
}: {
	ctx: RenderContext;
	current: NavId;
	floating: boolean;
}) {
	const links = navLinks(ctx, current);
	const linkClass =
		"nav-link px-3 py-2 text-ink-2 text-sm no-underline aria-[current=page]:text-ink-1";
	return (
		<header
			className={`site-header z-40 ${floating ? "fixed inset-x-0 top-0" : "relative"}`}
		>
			<div className="site-bar relative z-10">
				<div className="mx-auto flex h-16 max-w-[1240px] items-center gap-2 px-4 sm:px-8">
					<a
						href="/"
						className="flex items-center gap-2.5 text-ink-1 no-underline"
						aria-label={`${SITE_NAME}, home`}
					>
						<LogoMark size={24} />
						<span className="font-semibold text-base tracking-tight">
							{SITE_NAME}
						</span>
					</a>
					<nav aria-label="Main" className="ml-6 hidden items-center md:flex">
						{links.map((link) => (
							<a
								key={link.href}
								href={link.href}
								className={linkClass}
								{...(link.current ? { "aria-current": "page" as const } : {})}
							>
								{link.label}
								{link.suffix ? (
									<span className="ml-1.5 rounded-md bg-surface-well px-1.5 py-0.5 text-ink-3 text-xs tabular-nums">
										{link.suffix}
									</span>
								) : null}
							</a>
						))}
					</nav>
					<a
						href={current === "landing" ? "#install" : "/#install"}
						data-install-link
						className="btn-primary pressable ml-auto inline-flex h-9 items-center rounded-lg px-3.5 font-semibold text-sm no-underline"
					>
						Install
					</a>
					<div className="js-only md:hidden">
						<Island
							name="menu"
							props={{ controls: "site-menu", label: "menu" }}
						>
							<MenuToggle controls="site-menu" label="menu" />
						</Island>
					</div>
				</div>
			</div>
			<nav
				id="site-menu"
				data-open="false"
				aria-label="Main, mobile"
				className="menu-overlay pointer-events-auto md:hidden"
			>
				<ul className="m-0 flex list-none flex-col gap-2 p-0">
					{links.map((link, i) => (
						<li key={link.href}>
							<a
								href={link.href}
								style={{ "--i": i } as React.CSSProperties}
								className="block py-2 font-semibold text-3xl text-ink-1 tracking-tight no-underline"
								{...(link.current ? { "aria-current": "page" as const } : {})}
							>
								{link.label}
							</a>
						</li>
					))}
				</ul>
			</nav>
		</header>
	);
}

function SiteFooter({ ctx }: { ctx: RenderContext }) {
	const col = "m-0 flex list-none flex-col gap-2 p-0 text-sm";
	const link = "text-ink-2 no-underline hover:text-ink-1 hover:underline";
	return (
		<footer>
			<div className="mx-auto grid max-w-[1240px] gap-8 px-4 py-12 sm:px-8 md:grid-cols-[2fr_1fr_1fr_1fr]">
				<div className="flex max-w-xs flex-col gap-3">
					<a
						href="/"
						className="flex items-center gap-2 text-ink-1 no-underline"
					>
						<LogoMark size={24} />
						<span className="font-semibold">{SITE_NAME}</span>
					</a>
					<p className="m-0 text-ink-3 text-sm">
						Free, self hosted and open source under AGPL-3.0-only. No cookies
						and no analytics on this site.
					</p>
					<FactChips version={ctx.config.version} />
					<p className="m-0 text-ink-3 text-sm">
						<a className="text-ink-2 hover:text-ink-1" href="/compare">
							Compared row by row
						</a>{" "}
						with other self hosted deploy platforms.
					</p>
				</div>
				<nav aria-label="Product">
					<h2 className="mb-3 font-semibold text-ink-1 text-sm">Product</h2>
					<ul className={col}>
						<li>
							<a className={link} href="/docs">
								Docs
							</a>
						</li>
						<li>
							<a className={link} href="/docs/getting-started/quickstart">
								Quickstart
							</a>
						</li>
						<li>
							<a className={link} href="/docs/reference/api">
								API reference
							</a>
						</li>
						<li>
							<a className={link} href="/docs/reference/cli">
								CLI reference
							</a>
						</li>
						<li>
							<a className={link} href="/compare">
								Compare
							</a>
						</li>
						<li>
							<a className={link} href="/faq">
								FAQ
							</a>
						</li>
					</ul>
				</nav>
				<nav aria-label="For machines">
					<h2 className="mb-3 font-semibold text-ink-1 text-sm">
						For machines
					</h2>
					<ul className={col}>
						<li>
							<a className={link} href="/install.sh">
								install.sh
							</a>
						</li>
						<li>
							<a className={link} href="/llms.txt">
								llms.txt
							</a>
						</li>
						<li>
							<a className={link} href="/llms-full.txt">
								llms-full.txt
							</a>
						</li>
						<li>
							<a className={link} href="/api/v1/openapi.json">
								OpenAPI
							</a>
						</li>
					</ul>
				</nav>
				<nav aria-label="Legal">
					<h2 className="mb-3 font-semibold text-ink-1 text-sm">Legal</h2>
					<ul className={col}>
						<li>
							<a className={link} href="/privacy">
								Privacy
							</a>
						</li>
						<li>
							<a className={link} href="/licence">
								Licence
							</a>
						</li>
						{ctx.config.githubUrl ? (
							<li>
								<a className={link} href={ctx.config.githubUrl}>
									Source on GitHub
								</a>
							</li>
						) : null}
					</ul>
				</nav>
			</div>
			<div className="border-line-faint border-t">
				<div className="mx-auto flex max-w-[1240px] items-center justify-between gap-4 px-4 py-4 text-ink-3 text-xs sm:px-8">
					<span>NebulaCtrl is free and open source under AGPL-3.0-only.</span>
					<a
						href="#top"
						className="-my-3 inline-flex min-h-11 items-center gap-1 py-3 text-ink-2 no-underline hover:text-ink-1"
					>
						<ArrowUpIcon aria-hidden className="size-3" />
						Back to top
					</a>
				</div>
			</div>
		</footer>
	);
}
