import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { errorMessage } from "#/api/client-config";
import { environmentsQuery } from "#/api/hooks";
import type { Environment } from "#/api/models";
import { ErrorState } from "#/components/common/error-state";
import { Skeleton } from "#/components/ui/skeleton";
import { useDocumentTitle } from "#/lib/hooks/use-document-title";
import { type VtKind, vt, vtAnchor } from "#/lib/transitions";

/**
 * A page that scrolls inside the shell's main area: a title row (title,
 * one-line description, actions at the right) over the page's content. The
 * root carries `data-page`, and the title is the shared element a sidebar
 * label morphs into (`nav:<page>`, see `#/lib/transitions`). A page that is
 * one entity's own page passes `morph`: its title row then takes that
 * entity's card as the shared element in both directions, instead.
 */
export function PageFrame({
	page,
	title,
	description,
	actions,
	morph,
	children,
}: {
	/** The `data-page` name: "deployments", "services". */
	page: string;
	title: string;
	description?: ReactNode;
	actions?: ReactNode;
	morph?: { kind: VtKind; id: string };
	children: ReactNode;
}) {
	return (
		<div data-page={page} className="absolute inset-0">
			<div className="absolute inset-0 overflow-y-auto overscroll-contain">
				<div className="mx-auto flex w-full max-w-[1240px] flex-col gap-5 px-5 pt-7 pb-12 sm:px-8">
					<div
						{...(morph ? vtAnchor(morph.kind, morph.id) : {})}
						className="flex flex-wrap items-end justify-between gap-x-4 gap-y-3"
					>
						<div className="flex min-w-0 flex-col gap-1.5">
							<h1
								{...(morph ? {} : vt("nav", page))}
								className="font-display font-semibold text-[24px] text-ink-1 leading-[1.1] tracking-[-0.02em]"
							>
								{title}
							</h1>
							{description ? (
								<p className="text-[13px] text-ink-3 leading-snug">
									{description}
								</p>
							) : null}
						</div>
						{actions}
					</div>
					{children}
				</div>
			</div>
		</div>
	);
}

/**
 * Resolves `$envSlug` to its environment for a page under
 * `/p/$projectId/$envSlug/…`, standing in with a skeleton, a not-found or an
 * error state until there is one, then renders the page inside its frame.
 */
export function EnvironmentPage({
	projectId,
	envSlug,
	page,
	title,
	description,
	actions,
	children,
}: {
	projectId: string;
	envSlug: string;
	page: string;
	title: string;
	description?: (environment: Environment) => ReactNode;
	actions?: (environment: Environment) => ReactNode;
	children: (environment: Environment) => ReactNode;
}) {
	const environments = useQuery(environmentsQuery(projectId));
	const environment = environments.data?.find(
		(candidate) => candidate.slug === envSlug,
	);
	useDocumentTitle([title, environment?.name]);

	let body: ReactNode;
	if (environments.isError && environments.data === undefined) {
		body = (
			<ErrorState
				frame="raised"
				title="Couldn't load this project"
				body={`${errorMessage(environments.error)} Nothing was changed; check that the control plane is running, then try again.`}
				retry={() => void environments.refetch()}
			/>
		);
	} else if (environments.isSuccess && environment === undefined) {
		body = (
			<ErrorState
				frame="raised"
				title="Environment not found"
				body={`This project has no environment called “${envSlug}”.`}
			/>
		);
	} else if (environment === undefined) {
		body = (
			<div role="status" aria-label={`Loading ${title.toLowerCase()}`}>
				<Skeleton className="h-64 rounded-card" />
			</div>
		);
	} else {
		body = children(environment);
	}

	return (
		<PageFrame
			page={page}
			title={title}
			description={environment ? description?.(environment) : undefined}
			actions={environment ? actions?.(environment) : undefined}
		>
			{body}
		</PageFrame>
	);
}
