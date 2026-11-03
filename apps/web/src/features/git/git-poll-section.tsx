import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { errorMessage } from "#/api/client-config";
import { checkServiceGitPoll, setServiceGitPoll } from "#/api/gen/sdk.gen";
import type { GitPollInputBody, GitPollView } from "#/api/gen/types.gen";
import { invalidateServices } from "#/api/invalidate";
import type { Service } from "#/api/models";
import { Button } from "#/components/ui/button";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Spinner } from "#/components/ui/spinner";
import { Switch } from "#/components/ui/switch";
import { toast } from "#/components/ui/toast";
import { useDeploymentActions } from "#/features/deployments/use-deployment-actions";
import { SettingRow } from "#/features/service-settings/fields";
import {
	canPollGit,
	POLL_OPTIONS,
	type PollChoice,
	pollChoice,
	pollDeploys,
	pollInput,
	pollStatus,
} from "./git-poll-model";

const POLL_ITEMS = POLL_OPTIONS.map((option) => ({
	value: option.value,
	label: option.label,
}));

/**
 * "Check for new commits" of a git service: the control plane reads the head
 * of the branch on a schedule, for a repository that cannot send a webhook.
 * Operational settings like the link switches: they apply immediately and are
 * never staged. Shown for any app or worker with a git source, linked or not.
 */
export function GitPollRows({ service }: { service: Service }) {
	const queryClient = useQueryClient();
	const actions = useDeploymentActions(service);
	// The answer of the last "Check now"; the service view carries the rest.
	const [checked, setChecked] = useState<GitPollView | null>(null);

	const save = useMutation({
		mutationFn: async (body: GitPollInputBody) => {
			await setServiceGitPoll({ path: { serviceId: service.id }, body });
		},
		onSuccess: async () => {
			setChecked(null);
			await invalidateServices(queryClient);
		},
		onError: (e) => toast.error("Could not save", errorMessage(e)),
	});

	const check = useMutation({
		mutationFn: async () => {
			const { data } = await checkServiceGitPoll({
				path: { serviceId: service.id },
			});
			return data;
		},
		onSuccess: async (view) => {
			setChecked(view);
			await invalidateServices(queryClient);
		},
		onError: (e) => toast.error("Could not check", errorMessage(e)),
	});

	if (!canPollGit(service)) return null;

	const saved = service.gitPoll;
	// While a save is in flight show what was asked for, so the control does not flicker back.
	const asked = save.isPending ? save.variables : undefined;
	const choice: PollChoice = asked
		? asked.enabled
			? (`${asked.intervalMinutes ?? 5}` as PollChoice)
			: "off"
		: pollChoice(saved);
	const deploys = asked ? (asked.deploy ?? true) : pollDeploys(saved);
	const off = choice === "off";
	const status = pollStatus(
		saved ?? checked,
		service.currentRelease?.source.commitSha,
	);
	const watchPaths = (service.git?.watchPaths?.length ?? 0) > 0;

	return (
		<>
			<SettingRow
				label="Check for new commits"
				htmlFor="set-git-poll"
				description="Looks at the branch on a schedule, for repositories that cannot send a webhook. Takes effect immediately; never staged."
			>
				<div className="flex flex-col gap-2">
					<Select
						value={choice}
						items={POLL_ITEMS}
						onValueChange={(next) =>
							save.mutate(
								pollInput(String(next ?? "off") as PollChoice, deploys, saved),
							)
						}
					>
						<SelectTrigger
							id="set-git-poll"
							className="w-full max-w-[360px]"
							disabled={save.isPending}
						>
							<SelectValue />
						</SelectTrigger>
						<SelectContent alignItemWithTrigger={false}>
							{POLL_OPTIONS.map((option) => (
								<SelectItem key={option.value} value={option.value}>
									{option.label}
								</SelectItem>
							))}
						</SelectContent>
					</Select>
					{status ? (
						<span
							role={status.tone === "error" ? "alert" : undefined}
							className={
								status.tone === "error"
									? "text-destructive-text text-xs leading-relaxed"
									: "type-secondary"
							}
						>
							{status.text}
						</span>
					) : null}
					<div className="flex flex-wrap gap-2">
						<Button
							variant="secondary"
							size="sm"
							disabled={check.isPending}
							onClick={() => check.mutate()}
						>
							{check.isPending ? <Spinner /> : null}
							Check now
						</Button>
						{status?.tone === "update" ? (
							<Button
								variant="primary"
								size="sm"
								disabled={actions.deploy.isPending}
								onClick={() => actions.deploy.mutate(undefined)}
							>
								{actions.deploy.isPending ? <Spinner /> : null}
								Deploy latest
							</Button>
						) : null}
					</div>
				</div>
			</SettingRow>
			<SettingRow
				label="Deploy new commits"
				description="Off: a new commit is only shown, and you deploy it yourself."
			>
				<div className="flex flex-col gap-1.5">
					<Switch
						checked={deploys}
						disabled={off || save.isPending}
						onCheckedChange={(next) =>
							save.mutate(pollInput(choice, next, saved))
						}
						aria-label="Deploy new commits automatically"
					/>
					{watchPaths ? (
						<span className="type-secondary">
							Watch paths do not filter commits found by checking.
						</span>
					) : null}
				</div>
			</SettingRow>
		</>
	);
}
