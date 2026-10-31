import { useQuery } from "@tanstack/react-query";
import { TerminalIcon } from "lucide-react";
import { getSystemOptions } from "#/api/gen/@tanstack/react-query.gen";
import { CodeBlock } from "#/components/common/code-block";

/**
 * Installing the CLI: this control plane serves it (GET /cli.sh, checked
 * against GET /download/cli/SHA256SUMS), so the one-liner names this instance.
 * Shown next to the API tokens because the first thing the CLI needs is one.
 */
export function CliInstall() {
	const system = useQuery({
		...getSystemOptions(),
		// Static for the life of the process; no polling.
		refetchInterval: false,
		staleTime: 60_000,
	});
	// With no public URL configured the server cannot name itself; the address
	// the browser reached it on is the honest fallback.
	const origin = (system.data?.publicUrl || window.location.origin).replace(
		/\/+$/,
		"",
	);
	const install = `curl -fsSL ${origin}/cli.sh | sh`;
	const login = `nebulactrl login --url ${origin} --token <token>`;
	return (
		<section
			aria-labelledby="cli-install-title"
			className="surface-well flex flex-col gap-3 rounded-card p-4"
		>
			<h3
				id="cli-install-title"
				className="m-0 flex items-center gap-2 font-medium text-ink-1 text-sm"
			>
				<TerminalIcon aria-hidden="true" className="size-4 text-ink-3" />
				Install the CLI
			</h3>
			<CodeBlock copyValue={install}>
				<pre className="whitespace-pre-wrap">{install}</pre>
			</CodeBlock>
			<p className="m-0 text-ink-3 text-xs leading-relaxed">
				Downloads the build that matches this control plane for your system
				(Linux or macOS, amd64 or arm64), checks its checksum and installs it in{" "}
				<code className="type-mono text-ink-2">/usr/local/bin</code>, or in{" "}
				<code className="type-mono text-ink-2">~/.local/bin</code> without root.
				Then sign in with a token from this page:
			</p>
			<CodeBlock copyValue={login}>
				<pre className="whitespace-pre-wrap">{login}</pre>
			</CodeBlock>
			<p className="m-0 text-ink-4 text-xs">
				Windows:{" "}
				<a
					className="text-ink-2 underline underline-offset-2"
					href={`${origin}/download/cli/windows/amd64`}
				>
					amd64
				</a>{" "}
				or{" "}
				<a
					className="text-ink-2 underline underline-offset-2"
					href={`${origin}/download/cli/windows/arm64`}
				>
					arm64
				</a>
				, then put <code className="type-mono">nebulactrl.exe</code> on your
				PATH. The checksums are at{" "}
				<code className="type-mono">/download/cli/SHA256SUMS</code>.
			</p>
		</section>
	);
}
