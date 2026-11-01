import { ExternalLinkIcon } from "lucide-react";

// Build constants. VITE_SITE_URL is the public site (apps/site) that carries the
// latest docs; unset, the note is left out rather than pointing nowhere.
const SITE_URL = import.meta.env.VITE_SITE_URL?.replace(/\/+$/, "");
const VERSION = import.meta.env.VITE_APP_VERSION?.replace(/^v/, "");

/** "Docs for v1.4.0. Latest at https://nebulactrl.example/docs": these pages match the installed version. */
export function LatestDocsNote({ className }: { className?: string }) {
	if (!SITE_URL) return null;
	const label = VERSION && VERSION !== "dev" ? `v${VERSION}` : "this build";
	return (
		<p className={className}>
			Docs for {label}. Latest at{" "}
			<a
				href={`${SITE_URL}/docs`}
				target="_blank"
				rel="noopener noreferrer"
				className="inline-flex items-center gap-1 text-accent-ink"
			>
				{SITE_URL}/docs
				<ExternalLinkIcon aria-hidden className="size-3" />
			</a>
		</p>
	);
}
