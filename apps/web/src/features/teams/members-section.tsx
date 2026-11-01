import { DitherAvatar } from "@nebulactrl/dither/interface/avatar";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { LinkIcon, MailPlusIcon } from "lucide-react";
import { useState } from "react";
import { errorMessage } from "#/api/client-config";
import {
	listTeamInvitesOptions,
	listTeamInvitesQueryKey,
	listTeamMembersOptions,
	listTeamMembersQueryKey,
	listTeamsQueryKey,
} from "#/api/gen/@tanstack/react-query.gen";
import {
	deleteTeamInvite,
	removeTeamMember,
	updateTeamMember,
} from "#/api/gen/sdk.gen";
import type { TeamInvite, TeamMember } from "#/api/gen/types.gen";
import { useConfirm } from "#/components/common/confirm-dialog";
import { EmptyState } from "#/components/common/empty-state";
import { ErrorState } from "#/components/common/error-state";
import { Relative } from "#/components/common/relative";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Skeleton } from "#/components/ui/skeleton";
import {
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "#/components/ui/table";
import { toast } from "#/components/ui/toast";
import { SettingsPlate } from "#/features/settings/section";
import { InviteDialog } from "./invite-dialog";
import { ProjectAccessDialog } from "./project-access-dialog";
import { useCurrentTeam } from "./queries";
import { ResetLinkDialog } from "./reset-link-dialog";
import {
	assignableRoles,
	canChangeMember,
	canManageMembers,
	describeProjectAccess,
	isRole,
	ROLE_LABEL,
	ROLE_SUMMARY,
	type Role,
	supportsProjectRestriction,
} from "./roles";

function MemberRoleSelect({
	member,
	actorRole,
	onChange,
}: {
	member: TeamMember;
	actorRole: Role | undefined;
	onChange: (role: Role) => void;
}) {
	const current = isRole(member.role) ? member.role : "viewer";
	const editable = canChangeMember(actorRole, current);
	if (!editable) {
		return <span className="text-ink-2 text-xs">{ROLE_LABEL[current]}</span>;
	}
	const options = assignableRoles(actorRole).map((role) => ({
		value: role,
		label: ROLE_LABEL[role],
	}));
	return (
		<Select
			value={current}
			items={options}
			onValueChange={(next) => {
				if (isRole(next) && next !== current) onChange(next);
			}}
		>
			<SelectTrigger size="sm" aria-label={`Role of ${member.name}`}>
				<SelectValue />
			</SelectTrigger>
			<SelectContent alignItemWithTrigger={false} align="start">
				{options.map((option) => (
					<SelectItem key={option.value} value={option.value}>
						<span className="flex flex-col gap-0.5">
							<span>{option.label}</span>
							<span className="max-w-[260px] whitespace-normal text-2xs text-ink-3">
								{ROLE_SUMMARY[option.value]}
							</span>
						</span>
					</SelectItem>
				))}
			</SelectContent>
		</Select>
	);
}

function MemberRow({
	member,
	actorRole,
	onRole,
	onProjects,
	onReset,
	onRemove,
}: {
	member: TeamMember;
	actorRole: Role | undefined;
	onRole: (member: TeamMember, role: Role) => void;
	onProjects: (member: TeamMember) => void;
	onReset: (member: TeamMember) => void;
	onRemove: (member: TeamMember) => void;
}) {
	const role = isRole(member.role) ? member.role : "viewer";
	const manageable = canChangeMember(actorRole, role);
	const access = describeProjectAccess(role, member.projectIds);
	return (
		<TableRow>
			<TableCell pin="start">
				<span className="flex min-w-0 items-center gap-2.5">
					<DitherAvatar
						aria-hidden
						name={member.email}
						label={member.name}
						size={28}
					/>
					<span className="flex min-w-0 flex-col gap-0.5">
						<span className="flex items-center gap-1.5 font-medium text-ink-1">
							<span className="truncate">{member.name || member.email}</span>
							{member.you ? <Badge>you</Badge> : null}
						</span>
						<span className="truncate text-2xs text-ink-3">{member.email}</span>
					</span>
				</span>
			</TableCell>
			<TableCell label="Role">
				<MemberRoleSelect
					member={member}
					actorRole={actorRole}
					onChange={(next) => onRole(member, next)}
				/>
			</TableCell>
			<TableCell label="Projects" className="text-xs">
				{manageable && supportsProjectRestriction(role) ? (
					<Button
						variant="ghost"
						size="sm"
						aria-label={`Project access of ${member.name}: ${access}`}
						onClick={() => onProjects(member)}
					>
						{access}
					</Button>
				) : (
					<span className="text-ink-3">{access}</span>
				)}
			</TableCell>
			<TableCell label="Joined" className="text-ink-3 text-xs">
				<Relative iso={member.joinedAt} />
			</TableCell>
			<TableCell className="w-px">
				<span className="flex items-center gap-1">
					{manageable && !member.you ? (
						<>
							<Button
								variant="ghost"
								size="sm"
								aria-label={`Reset the password of ${member.name}`}
								onClick={() => onReset(member)}
							>
								Reset password
							</Button>
							<Button
								variant="ghost"
								size="sm"
								className="text-destructive-text"
								onClick={() => onRemove(member)}
							>
								Remove
							</Button>
						</>
					) : null}
				</span>
			</TableCell>
		</TableRow>
	);
}

function InviteRow({
	invite,
	onRevoke,
}: {
	invite: TeamInvite;
	onRevoke: (invite: TeamInvite) => void;
}) {
	const role = isRole(invite.role) ? invite.role : "viewer";
	return (
		<TableRow>
			<TableCell pin="start">
				<span className="flex min-w-0 items-center gap-2.5">
					{invite.email ? (
						<DitherAvatar aria-hidden name={invite.email} size={28} />
					) : (
						<span
							aria-hidden
							className="grid size-7 shrink-0 place-items-center rounded-full border border-line text-ink-3"
						>
							<LinkIcon className="size-3.5" />
						</span>
					)}
					<span className="flex min-w-0 flex-col gap-0.5">
						<span className="truncate font-medium text-ink-1">
							{invite.email ?? "Link invite"}
						</span>
						<span className="font-mono text-2xs text-ink-4">
							nci_{invite.prefix}…
						</span>
					</span>
				</span>
			</TableCell>
			<TableCell label="Role" className="text-ink-2 text-xs">
				{ROLE_LABEL[role]}
				{invite.projectIds ? (
					<span className="text-ink-3">
						{" "}
						· {describeProjectAccess(role, invite.projectIds)}
					</span>
				) : null}
			</TableCell>
			<TableCell label="Used" className="text-ink-3 text-xs">
				{invite.useCount} / {invite.maxUses}
			</TableCell>
			<TableCell label="Expires" className="text-ink-3 text-xs">
				{invite.expired ? (
					<Badge tone="warn" lamp shape="capsule">
						expired
					</Badge>
				) : (
					<Relative iso={invite.expiresAt} />
				)}
			</TableCell>
			<TableCell label="Email" className="text-ink-3 text-xs">
				{invite.email ? (invite.emailSent ? "sent" : "not sent") : "–"}
			</TableCell>
			<TableCell className="w-px">
				<Button
					variant="ghost"
					size="sm"
					className="text-destructive-text"
					onClick={() => onRevoke(invite)}
				>
					Revoke
				</Button>
			</TableCell>
		</TableRow>
	);
}

/**
 * Team settings → Members: who is in the team with which role and project access,
 * and (for owners and admins) the pending invites. The server decides what each
 * role may do; the controls shown here are only the ones that can succeed.
 */
export function MembersSection() {
	const queryClient = useQueryClient();
	const confirm = useConfirm();
	const { team, role, pending } = useCurrentTeam();
	const teamId = team?.id ?? "";
	const canManage = canManageMembers(role);
	const members = useQuery({
		...listTeamMembersOptions({ path: { teamId } }),
		enabled: teamId !== "",
	});
	const invites = useQuery({
		...listTeamInvitesOptions({ path: { teamId } }),
		enabled: teamId !== "" && canManage,
	});
	const [inviting, setInviting] = useState(false);
	const [editing, setEditing] = useState<TeamMember | null>(null);
	const [resetting, setResetting] = useState<TeamMember | null>(null);

	const refresh = async () => {
		await Promise.all([
			queryClient.invalidateQueries({
				queryKey: listTeamMembersQueryKey({ path: { teamId } }),
			}),
			queryClient.invalidateQueries({ queryKey: listTeamsQueryKey() }),
		]);
	};

	const changeRole = useMutation({
		mutationFn: ({ member, next }: { member: TeamMember; next: Role }) =>
			updateTeamMember({
				path: { teamId, userId: member.userId },
				body: { role: next },
			}),
		onSuccess: async (_, { member, next }) => {
			toast.success(`${member.name} is now ${ROLE_LABEL[next]}`);
			await refresh();
		},
		onError: (error) =>
			toast.error("Could not change the role", errorMessage(error)),
	});

	const remove = useMutation({
		mutationFn: (member: TeamMember) =>
			removeTeamMember({ path: { teamId, userId: member.userId } }),
		onSuccess: async (_, member) => {
			toast.success(
				`Removed ${member.name}`,
				"Their API tokens in this team stopped working.",
			);
			await refresh();
		},
	});

	const revoke = useMutation({
		mutationFn: (invite: TeamInvite) =>
			deleteTeamInvite({ path: { teamId, inviteId: invite.id } }),
		onSuccess: async () => {
			toast.success("Invite revoked", "Its link no longer works.");
			await queryClient.invalidateQueries({
				queryKey: listTeamInvitesQueryKey({ path: { teamId } }),
			});
		},
		onError: (error) =>
			toast.error("Could not revoke the invite", errorMessage(error)),
	});

	const askRemove = (member: TeamMember) =>
		void confirm({
			title: `Remove ${member.name}?`,
			body: `${member.name} loses access to this team and their API tokens in it stop working. Their account stays.`,
			cta: "Remove member",
			danger: true,
			hold: false,
			onConfirm: async () => {
				await remove.mutateAsync(member);
			},
		});

	if (pending || members.isPending) {
		return <Skeleton className="h-48 rounded-card" />;
	}
	if (!team || (members.isError && !members.data)) {
		return (
			<ErrorState
				frame="raised"
				title="Members did not load"
				body="The control plane did not answer, so nothing was changed. Try again in a moment."
				retry={() => void members.refetch()}
			/>
		);
	}

	const rows = members.data?.items ?? [];
	const pendingInvites = invites.data?.items ?? [];

	return (
		<>
			<SettingsPlate
				id="members"
				title={`${rows.length} ${rows.length === 1 ? "member" : "members"}`}
				flush
				actions={
					canManage ? (
						<Button variant="primary" onClick={() => setInviting(true)}>
							<MailPlusIcon />
							Invite people
						</Button>
					) : null
				}
			>
				{rows.length === 0 ? (
					<EmptyState
						illustration="network"
						title="Nobody here yet"
						body="Invite people by email or with a link."
					/>
				) : (
					<Table stack>
						<TableHeader>
							<TableRow>
								<TableHead>Member</TableHead>
								<TableHead>Role</TableHead>
								<TableHead>Projects</TableHead>
								<TableHead>Joined</TableHead>
								<TableHead className="w-px" />
							</TableRow>
						</TableHeader>
						<TableBody>
							{rows.map((member) => (
								<MemberRow
									key={member.userId}
									member={member}
									actorRole={role}
									onRole={(m, next) => changeRole.mutate({ member: m, next })}
									onProjects={setEditing}
									onReset={setResetting}
									onRemove={askRemove}
								/>
							))}
						</TableBody>
					</Table>
				)}
				{canManage ? null : (
					<p className="px-5 py-3 text-ink-3 text-xs">
						Owners and admins manage members and invites.
					</p>
				)}
			</SettingsPlate>

			{canManage ? (
				<SettingsPlate id="invites" title="Pending invites" flush>
					{invites.isPending ? (
						<Skeleton className="m-5 h-20 rounded-card" />
					) : pendingInvites.length === 0 ? (
						<p className="px-5 py-4 text-ink-3 text-xs">
							No pending invites. A link works until it expires; an email invite
							works only for its address.
						</p>
					) : (
						<Table stack>
							<TableHeader>
								<TableRow>
									<TableHead>Invite</TableHead>
									<TableHead>Role</TableHead>
									<TableHead>Used</TableHead>
									<TableHead>Expires</TableHead>
									<TableHead>Email</TableHead>
									<TableHead className="w-px" />
								</TableRow>
							</TableHeader>
							<TableBody>
								{pendingInvites.map((invite) => (
									<InviteRow
										key={invite.id}
										invite={invite}
										onRevoke={(i) => revoke.mutate(i)}
									/>
								))}
							</TableBody>
						</Table>
					)}
				</SettingsPlate>
			) : null}

			<InviteDialog
				open={inviting}
				onOpenChange={setInviting}
				teamId={team.id}
				actorRole={role}
			/>
			<ProjectAccessDialog
				teamId={team.id}
				member={editing}
				onOpenChange={(open) => {
					if (!open) setEditing(null);
				}}
			/>
			<ResetLinkDialog
				teamId={team.id}
				member={resetting}
				onClose={() => setResetting(null)}
			/>
		</>
	);
}
