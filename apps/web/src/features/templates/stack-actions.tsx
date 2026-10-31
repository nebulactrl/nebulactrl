import { useQuery } from "@tanstack/react-query";
import { CircleArrowUpIcon, RocketIcon, Trash2Icon } from "lucide-react";
import type { Service } from "#/api/models";
import { useProjectContext } from "#/components/shell/use-project-context";
import { Spinner } from "#/components/ui/spinner";
import { openStagedReview } from "#/features/deploy-button";
import { useStagedActions } from "#/features/staged/use-staged";
import { instanceOfGroup } from "./lib";
import { instancesQuery } from "./queries";
import { templatesActions } from "./store";

const BUTTON =
	"nodrag grid size-6 shrink-0 cursor-pointer place-items-center rounded-sm text-muted-foreground transition-[color,background-color] duration-80 hover:bg-muted hover:text-foreground focus-visible:focus-ring disabled:cursor-not-allowed disabled:opacity-45 disabled:hover:bg-transparent disabled:hover:text-muted-foreground";

/**
 * The header actions of a canvas group that is an installed template stack:
 * review a newer template revision (only while there is one), deploy what is
 * staged in it, or delete the whole stack. Renders nothing for an ordinary
 * group.
 */
export function StackActions({
	groupId,
	groupName,
	members,
}: {
	groupId: string;
	groupName: string;
	members: readonly Service[];
}) {
	const environment = useProjectContext()?.environment;
	const instances = useQuery({
		...instancesQuery(environment?.id ?? ""),
		enabled: environment !== undefined,
	});
	const { deploy } = useStagedActions(environment?.id ?? "", undefined);
	const instance = instanceOfGroup(instances.data ?? [], groupId);
	if (!environment || !instance) return null;

	const staged = members.filter((member) => member.hasStagedChanges);
	const label =
		staged.length === 0
			? "Deploy stack: nothing is staged in it"
			: `Deploy stack (${staged.length} staged)`;

	const updateLabel = `Update available: ${instance.templateVersion || `revision ${instance.currentRevision}`} → ${instance.currentVersion || `revision ${instance.currentRevision}`}`;

	return (
		<>
			{instance.updateAvailable ? (
				<button
					type="button"
					aria-label={`${updateLabel}, ${groupName}`}
					title={updateLabel}
					onClick={() => templatesActions.requestUpdate(instance)}
					className={`${BUTTON} text-accent-ink hover:text-accent-ink`}
				>
					<CircleArrowUpIcon aria-hidden className="size-3.5" />
				</button>
			) : null}
			<button
				type="button"
				aria-label={`${label}: ${groupName}`}
				title={label}
				disabled={staged.length === 0 || deploy.isPending}
				onClick={() => {
					const serviceIds = staged.map((member) => member.id);
					// Production's one confirmation is the guarded key in the review sheet.
					if (environment.isProduction) openStagedReview();
					else deploy.mutate({ serviceIds });
				}}
				className={BUTTON}
			>
				{deploy.isPending ? (
					<Spinner className="size-3.5" />
				) : (
					<RocketIcon aria-hidden className="size-3.5" />
				)}
			</button>
			<button
				type="button"
				aria-label={`Delete stack ${groupName}`}
				title="Delete stack"
				onClick={() => templatesActions.requestDelete(instance)}
				className={BUTTON}
			>
				<Trash2Icon aria-hidden className="size-3.5" />
			</button>
		</>
	);
}
