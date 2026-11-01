import { useQuery } from "@tanstack/react-query";
import { errorMessage } from "#/api/client-config";
import { environmentsQuery } from "#/api/hooks";
import { ErrorState } from "#/components/common/error-state";
import { EnvironmentPage } from "#/components/shell/page-frame";
import { Skeleton } from "#/components/ui/skeleton";
import { PreviewsList } from "./previews-list";
import { PreviewSettingsForm } from "./settings-form";
import { usePreviewSettings } from "./use-previews";

function SectionHeading({
	id,
	title,
	description,
}: {
	id: string;
	title: string;
	description: string;
}) {
	return (
		<div className="flex flex-col gap-2">
			<h2 id={id} className="section-head">
				{title}
			</h2>
			<p className="type-secondary text-ink-3">{description}</p>
		</div>
	);
}

function SettingsBody({ projectId }: { projectId: string }) {
	const settings = usePreviewSettings(projectId);
	const environments = useQuery(environmentsQuery(projectId));
	if (settings.isError && settings.data === undefined) {
		return (
			<ErrorState
				frame="raised"
				title="Couldn't load the preview settings"
				body={errorMessage(settings.error)}
				retry={() => void settings.refetch()}
			/>
		);
	}
	if (settings.data === undefined || environments.data === undefined) {
		return <Skeleton className="h-72 rounded-card" />;
	}
	return (
		<PreviewSettingsForm
			projectId={projectId}
			settings={settings.data}
			environments={environments.data}
		/>
	);
}

/**
 * The project's pull-request previews: the open ones with their status and a
 * delete action, then the settings that decide how they are made.
 */
export function PreviewsPage({
	projectId,
	envSlug,
}: {
	projectId: string;
	envSlug: string;
}) {
	return (
		<EnvironmentPage
			projectId={projectId}
			envSlug={envSlug}
			page="previews"
			title="Previews"
			description={() =>
				"A copy of the project for every open pull request, with its own URLs and a fresh database."
			}
		>
			{() => (
				<div className="flex flex-col gap-10">
					<section
						aria-labelledby="previews-open"
						className="flex min-w-0 flex-col gap-4"
					>
						<SectionHeading
							id="previews-open"
							title="Open previews"
							description="Deleted automatically when the pull request closes or merges, or after the idle period."
						/>
						<PreviewsList projectId={projectId} />
					</section>
					<section
						aria-labelledby="previews-settings"
						className="flex min-w-0 flex-col gap-4"
					>
						<SettingsBody projectId={projectId} />
					</section>
				</div>
			)}
		</EnvironmentPage>
	);
}
