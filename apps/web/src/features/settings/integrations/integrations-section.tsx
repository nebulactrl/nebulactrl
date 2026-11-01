import { DocsLink } from "#/components/docs/docs-link";
import { GitConnectionsSection } from "#/features/git/connections-section";
import { SecretManagersSection } from "#/features/secret-managers/secret-managers-section";
import { CloudflareSection } from "./cloudflare-section";
import { TailscaleSection } from "./tailscale-section";

/** Settings → Integrations: outside services NebulaCtrl connects to. Git providers, secret managers, Cloudflare, Tailscale. */
export function IntegrationsSection() {
	return (
		<div className="flex flex-col gap-8">
			<section
				aria-labelledby="integration-git"
				className="flex flex-col gap-3"
			>
				<div className="flex flex-col gap-2">
					<h3 id="integration-git" className="section-head">
						Git providers
					</h3>
					<p className="type-secondary">
						GitHub, GitLab, Gitea, Forgejo and Bitbucket.{" "}
						<DocsLink to="operations/git-providers" />
					</p>
				</div>
				<GitConnectionsSection />
			</section>
			<section
				aria-labelledby="integration-secret-managers"
				className="flex flex-col gap-3"
			>
				<div className="flex flex-col gap-2">
					<h3 id="integration-secret-managers" className="section-head">
						Secret managers
					</h3>
					<p className="type-secondary">
						Vault, OpenBao, Infisical and Doppler. A variable points at a secret
						there instead of holding it.{" "}
						<DocsLink to="operations/secret-managers" />
					</p>
				</div>
				<SecretManagersSection />
			</section>
			<section
				aria-labelledby="integration-tailscale"
				className="flex flex-col gap-3"
			>
				<div className="flex flex-col gap-2">
					<h3 id="integration-tailscale" className="section-head">
						Tailscale
					</h3>
					<p className="type-secondary">
						Lets a server join your tailnet, so routes can reach devices that
						only exist there.{" "}
						<DocsLink to="operations/proxy-tailnet-certificates" />
					</p>
				</div>
				<TailscaleSection />
			</section>
			<section
				aria-labelledby="integration-cloudflare"
				className="flex flex-col gap-3"
			>
				<div className="flex flex-col gap-2">
					<h3 id="integration-cloudflare" className="section-head">
						Cloudflare
					</h3>
					<p className="type-secondary">
						Keeps your domains' DNS records in step with their servers. Only
						records NebulaCtrl created are ever changed.{" "}
						<DocsLink to="operations/cloudflare" />
					</p>
				</div>
				<CloudflareSection />
			</section>
		</div>
	);
}
