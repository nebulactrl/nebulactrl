import { CaretDownIcon } from "@phosphor-icons/react/dist/csr/CaretDown";
import { isValidElement, type ReactNode } from "react";
import { Document } from "../components/chrome.tsx";
import { type FaqItem, faqGroups, faqItems } from "../content/faq.tsx";
import { installCommand, type RenderContext } from "../site.ts";
import { FinalCta } from "./landing/closing.tsx";

const container = "mx-auto max-w-[1240px] px-4 sm:px-8";

/** One question as a disclosure row. No press scale on the row: a full width row would shift its text. */
function FaqRow({ item, open }: { item: FaqItem; open: boolean }) {
	return (
		<details
			className="faq-item border-line border-b first:border-t"
			{...(open ? { open: true } : {})}
		>
			<summary className="faq-summary flex items-center justify-between gap-4 py-4 font-semibold text-base text-ink-1 outline-none focus-visible:focus-ring">
				{item.q}
				<CaretDownIcon
					aria-hidden
					weight="bold"
					className="faq-chevron size-4 shrink-0 text-ink-3"
				/>
			</summary>
			<div className="max-w-[680px] pb-6 text-base text-ink-2 leading-relaxed">
				{item.a}
			</div>
		</details>
	);
}

/**
 * An answer as plain text, for the structured data. Read off the element tree
 * rather than out of rendered markup, so no tag stripping or entity decoding
 * is involved: what the author wrote is what comes out.
 */
export function plain(node: ReactNode): string {
	return textOf(node).replace(/\s+/g, " ").trim();
}

function textOf(node: ReactNode): string {
	if (typeof node === "string" || typeof node === "number") return String(node);
	if (Array.isArray(node)) return node.map(textOf).join("");
	if (isValidElement<{ children?: ReactNode }>(node)) {
		return textOf(node.props.children);
	}
	return "";
}

function faqLd(items: FaqItem[]) {
	return {
		"@context": "https://schema.org",
		"@type": "FAQPage",
		mainEntity: items.map((item) => ({
			"@type": "Question",
			name: item.q,
			acceptedAnswer: { "@type": "Answer", text: plain(item.a) },
		})),
	};
}

export function FaqPage({ ctx }: { ctx: RenderContext }) {
	const { siteUrl } = ctx.config;
	const groups = faqGroups();
	const description =
		"Answers about cost, requirements, the installer, backups, moving servers, the secret key and what happens when the control plane is down.";
	return (
		<Document
			ctx={ctx}
			current="faq"
			dawn
			meta={{
				title: "FAQ: questions about NebulaCtrl before and after you install",
				description,
				path: "/faq",
				jsonLd: [
					faqLd(faqItems()),
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
								name: "FAQ",
								item: `${siteUrl}/faq`,
							},
						],
					},
				],
			}}
		>
			<main id="main" className="page-surface pt-28">
				<div className={`${container} pb-24`}>
					<h1 className="display-2 m-0 max-w-[820px] text-ink-1">
						Questions people ask about NebulaCtrl
					</h1>
					<p className="m-0 mt-6 max-w-[680px] text-ink-2">
						What it costs, what it needs, what it changes on your server and
						what happens when something goes wrong. The{" "}
						<a className="text-accent-ink" href="/docs">
							docs
						</a>{" "}
						go deeper on every answer.
					</p>
					<div className="mt-12 flex flex-col gap-16">
						{groups.map((group, g) => (
							<section
								key={group.id}
								aria-labelledby={group.id}
								className="grid grid-cols-1 gap-8 lg:grid-cols-12 lg:gap-12"
							>
								<div className="lg:col-span-4">
									<h2
										id={group.id}
										className="display-title m-0 lg:sticky lg:top-24"
									>
										{group.title}
									</h2>
								</div>
								<div className="flex min-w-0 flex-col lg:col-span-8">
									{group.items.map((item, i) => (
										<FaqRow
											key={item.q}
											item={item}
											open={g === 0 && i === 0}
										/>
									))}
								</div>
							</section>
						))}
					</div>
				</div>
				<FinalCta command={installCommand(ctx.config.siteUrl)} />
			</main>
		</Document>
	);
}
