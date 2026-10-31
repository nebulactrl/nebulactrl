import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRouter } from "@tanstack/react-router";
import { type FormEvent, useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { listTeamsQueryKey } from "#/api/gen/@tanstack/react-query.gen";
import { removeTeamMember, updateTeam } from "#/api/gen/sdk.gen";
import type { Team } from "#/api/gen/types.gen";
import { meQuery } from "#/api/hooks";
import { useConfirm } from "#/components/common/confirm-dialog";
import { ErrorState } from "#/components/common/error-state";
import { Button } from "#/components/ui/button";
import { Field, FieldLabel } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { SettingsNotice, SettingsPlate } from "#/features/settings/section";
import { DeleteTeamDangerZone } from "./delete-team";
import { leaveTeamState, useCurrentTeam } from "./queries";
import {
	canDeleteTeam,
	canManageMembers,
	isRole,
	ROLE_LABEL,
	type Role,
} from "./roles";
import { TeamRequirementCard } from "./team-requirement-card";

/** The team's name, with its slug and your role as status text. */
function TeamNamePlate({ team, role }: { team: Team; role: Role | undefined }) {
	const queryClient = useQueryClient();
	const [name, setName] = useState(team.name);
	useEffect(() => setName(team.name), [team.name]);
	const canEdit = canManageMembers(role);

	const rename = useMutation({
		mutationFn: () =>
			updateTeam({ path: { teamId: team.id }, body: { name: name.trim() } }),
		onSuccess: async () => {
			await queryClient.invalidateQueries({ queryKey: listTeamsQueryKey() });
			toast.success("Team renamed");
		},
		onError: (error) => toast.error("Could not rename", errorMessage(error)),
	});

	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (name.trim() === "" || name.trim() === team.name || rename.isPending)
			return;
		rename.mutate();
	};

	return (
		<SettingsPlate id="team-name" title="Team name">
			{canEdit ? (
				<form onSubmit={submit} className="flex flex-wrap items-end gap-3">
					<Field className="min-w-[220px] flex-1">
						<FieldLabel htmlFor="team-rename">Name</FieldLabel>
						<Input
							id="team-rename"
							scale="lg"
							maxLength={80}
							value={name}
							onChange={(event) => setName(event.target.value)}
						/>
					</Field>
					<Button
						type="submit"
						variant="secondary"
						disabled={
							rename.isPending ||
							name.trim() === "" ||
							name.trim() === team.name
						}
					>
						{rename.isPending ? <Spinner /> : null}
						Save name
					</Button>
				</form>
			) : (
				<p className="font-medium text-ink-1 text-sm">{team.name}</p>
			)}
			<p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-ink-3 text-xs">
				<span>
					Slug <code className="type-mono text-ink-2">{team.slug}</code>
				</span>
				<span>
					Your role{" "}
					<b className="text-ink-2">
						{isRole(team.role) ? ROLE_LABEL[team.role] : team.role}
					</b>
				</span>
				{canEdit ? null : <span>Owners and admins rename the team.</span>}
			</p>
		</SettingsPlate>
	);
}

/** Leave this team. Everyone may; the server protects the last owner. */
function LeaveTeamPlate({ team }: { team: Team }) {
	const queryClient = useQueryClient();
	const router = useRouter();
	const confirm = useConfirm();
	const me = useQuery(meQuery());

	const leave = useMutation({
		mutationFn: () =>
			removeTeamMember({
				path: { teamId: team.id, userId: me.data?.id ?? "" },
			}),
		onSuccess: async () => {
			// Left the team: whatever is cached belongs to it.
			toast.success("You left the team");
			await leaveTeamState(queryClient, router);
		},
		onError: (error) => toast.error("Could not leave", errorMessage(error)),
	});

	const askLeave = () =>
		void confirm({
			title: `Leave ${team.name}?`,
			body: "You lose access to this team's projects. An owner or admin has to invite you again.",
			cta: "Leave team",
			danger: true,
			hold: false,
			onConfirm: async () => {
				await leave.mutateAsync();
			},
		});

	return (
		<SettingsPlate id="leave-team" title="Leave this team" tone="danger">
			<div className="flex flex-wrap items-center justify-between gap-4">
				<p className="min-w-0 flex-1 text-pretty text-ink-3 text-xs leading-relaxed">
					You lose access to {team.name}'s projects until someone invites you
					again.
				</p>
				<Button variant="danger" onClick={askLeave}>
					Leave team
				</Button>
			</div>
		</SettingsPlate>
	);
}

/**
 * Team settings → General: the team's name, whether two-step verification is
 * required, and the danger zone (leave, delete) last.
 */
export function GeneralSection() {
	const { team, role, pending } = useCurrentTeam();
	if (pending) return <Skeleton className="h-48 rounded-card" />;
	if (!team) {
		return (
			<ErrorState
				frame="raised"
				title="The team did not load"
				body="The control plane did not answer, so nothing was changed. Try again in a moment."
			/>
		);
	}
	return (
		<>
			<TeamNamePlate team={team} role={role} />
			{canManageMembers(role) ? (
				<TeamRequirementCard teamId={team.id} />
			) : (
				<SettingsNotice>
					Owners and admins manage the team's sign-in rules.
				</SettingsNotice>
			)}
			<LeaveTeamPlate team={team} />
			{canDeleteTeam(role) ? <DeleteTeamDangerZone team={team} /> : null}
		</>
	);
}
