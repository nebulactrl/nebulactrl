import { Document } from "../components/chrome.tsx";
import { DESCRIPTION, installCommand, type RenderContext } from "../site.ts";
import { FinalCta } from "./landing/closing.tsx";
import { Hero } from "./landing/hero.tsx";
import { Product } from "./landing/product.tsx";
import { Questions } from "./landing/questions.tsx";
import {
	Agent,
	ControlPlane,
	Features,
	Releases,
} from "./landing/sections.tsx";

/**
 * The landing page, in one order: the hero (the headline and the install
 * command), the dashboard, then the story in four numbered sections (a release
 * proves itself, the agent and the approval, the control plane can stop, the
 * six features), six questions and the install command again. Section markup lives in
 * `pages/landing/`; the figures come from `content/`, never from this file. The
 * comparison and all the questions have their own pages (`/compare`, `/faq`).
 */
export function Landing({ ctx }: { ctx: RenderContext }) {
	return (
		<Document
			ctx={ctx}
			landing
			current="landing"
			meta={{
				title:
					"NebulaCtrl: a self hosted deploy platform for your Docker servers",
				description: DESCRIPTION,
				path: "/",
				jsonLd: [softwareLd(ctx)],
			}}
		>
			<main id="main">
				<Hero ctx={ctx} />
				<Product />
				<Releases />
				<Agent />
				<ControlPlane />
				<Features />
				<Questions />
				<FinalCta command={installCommand(ctx.config.siteUrl)} />
			</main>
		</Document>
	);
}

function softwareLd(ctx: RenderContext) {
	return {
		"@context": "https://schema.org",
		"@type": "SoftwareApplication",
		name: "NebulaCtrl",
		applicationCategory: "DeveloperApplication",
		operatingSystem:
			"Linux (Debian 12 and 13, Ubuntu 22.04, 24.04 and 26.04, RHEL family 9; amd64 and arm64)",
		description: DESCRIPTION,
		url: ctx.config.siteUrl,
		license: "https://www.gnu.org/licenses/agpl-3.0.html",
		offers: { "@type": "Offer", price: "0", priceCurrency: "USD" },
	};
}
