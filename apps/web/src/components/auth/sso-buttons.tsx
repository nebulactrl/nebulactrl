import { KeyRoundIcon } from "lucide-react";
import type { ReactNode } from "react";
import { Button } from "#/components/ui/button";
import { providerStartPath } from "./auth-messages";

export interface LoginProvider {
	id: string;
	name: string;
	kind: "oidc" | "github";
}

function GitHubMark() {
	return (
		<svg
			aria-hidden="true"
			viewBox="0 0 16 16"
			fill="currentColor"
			className="size-4"
		>
			<path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82a7.6 7.6 0 0 1 4 0c1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8Z" />
		</svg>
	);
}

/**
 * One full-width button per enabled single-sign-on provider. They are plain
 * links: the server redirects to the provider and back, so this is a page
 * load, not an API call. `invite` carries a team invite through the round
 * trip so a new account can be created from it. `children` are other ways in
 * that sit above the providers under the same "or" (the passkey button).
 */
export function SsoButtons({
	providers,
	invite,
	children,
}: {
	providers: readonly LoginProvider[];
	invite?: string;
	children?: ReactNode;
}) {
	if (providers.length === 0 && !children) return null;
	return (
		<div className="flex flex-col gap-2">
			<div className="flex items-center gap-3 text-ink-4 text-xs">
				<span aria-hidden className="h-px flex-1 bg-line" />
				or
				<span aria-hidden className="h-px flex-1 bg-line" />
			</div>
			{children}
			{providers.map((provider) => (
				<Button
					key={provider.id}
					variant="secondary"
					size="xl"
					className="w-full"
					nativeButton={false}
					render={<a href={providerStartPath(provider.id, invite)} />}
				>
					{provider.kind === "github" ? <GitHubMark /> : <KeyRoundIcon />}
					Continue with {provider.name}
				</Button>
			))}
		</div>
	);
}
