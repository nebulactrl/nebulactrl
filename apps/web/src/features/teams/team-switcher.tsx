import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRouter } from "@tanstack/react-router";
import { cn } from "cn";
import { CheckIcon, ChevronsUpDownIcon, PlusIcon } from "lucide-react";
import { useState } from "react";
import { errorMessage } from "#/api/client-config";
import { switchTeam } from "#/api/gen/sdk.gen";
import type { Team } from "#/api/gen/types.gen";
import { projectsQuery } from "#/api/hooks";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuGroup,
	DropdownMenuItem,
	DropdownMenuLabel,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "#/components/ui/dropdown-menu";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { useServers } from "#/features/canvas/data";
import { CreateTeamDialog } from "./create-team-dialog";
import {
	canCreateTeam,
	instanceQuery,
	leaveTeamState,
	useCurrentTeam,
} from "./queries";
import { isRole, ROLE_LABEL } from "./roles";
import { teamCaption } from "./team-caption";
import { useTeamScope } from "./team-scope";

/** The team's tile: its initial on a raised key. A team is not a face. */
function TeamTile({ name, size }: { name: string; size: number }) {
	const initial = Array.from(name.trim())[0]?.toUpperCase() ?? "T";
	return (
		<span
			aria-hidden
			style={{ width: size, height: size }}
			className="grid shrink-0 place-items-center rounded-ctl border border-line-control bg-surface-float font-semibold text-accent-ink shadow-tile"
		>
			<span
				style={{ fontSize: Math.round(size * 0.46) }}
				className="leading-none"
			>
				{initial}
			</span>
		</span>
	);
}

function roleText(role: string): string {
	return isRole(role) ? ROLE_LABEL[role] : role;
}

/**
 * The sidebar header: which team the console is acting in and a menu to
 * change it or make another. Everything below the header (projects, servers)
 * belongs to that team; the team's own settings are in the sidebar. Collapsed,
 * the team's avatar alone remains.
 */
export function TeamSwitcher({ collapsed }: { collapsed: boolean }) {
	const queryClient = useQueryClient();
	const router = useRouter();
	const { teams, team, pending } = useCurrentTeam();
	const scope = useTeamScope();
	const instance = useQuery(instanceQuery());
	const projects = useQuery(projectsQuery());
	const servers = useServers();
	const [creating, setCreating] = useState(false);
	const [switchingTo, setSwitchingTo] = useState<string | undefined>();

	const change = useMutation({
		mutationFn: async (target: Team) => {
			await switchTeam({ path: { teamId: target.id } });
			return target;
		},
		onMutate: (target) => setSwitchingTo(target.id),
		onSuccess: async (target) => {
			// The cache holds the previous team's projects, servers and tokens:
			// the screen waits on a loader until the new team's lists are in.
			await leaveTeamState(queryClient, router);
			toast.success(`Switched to ${target.name}`);
		},
		onError: (error) =>
			toast.error("Could not switch team", errorMessage(error)),
		onSettled: () => setSwitchingTo(undefined),
	});

	const name = team?.name ?? "Team";
	// What the header shows while a switch is under way is the previous
	// team's: hide it until the new team's lists are in.
	const loading = (pending && !team) || scope.switching;
	const caption = teamCaption({
		role: team ? roleText(team.role) : undefined,
		projects: projects.data?.length,
		servers: servers.data?.length,
	});

	const avatar = <TeamTile name={name} size={collapsed ? 32 : 28} />;

	return (
		<>
			<DropdownMenu>
				<DropdownMenuTrigger
					data-nomorph
					aria-label={`Team: ${name}. Switch team`}
					title={collapsed ? name : undefined}
					className={cn(
						"pressable flex min-w-0 cursor-pointer items-center gap-2.5 text-left outline-none",
						collapsed
							? "size-10 justify-center rounded-card"
							: "surface-well w-full rounded-card px-2.5 py-2",
					)}
				>
					{avatar}
					{collapsed ? null : (
						<>
							<span className="flex min-w-0 flex-1 flex-col gap-[3px]">
								<b className="truncate font-semibold text-[12.5px] text-ink-1 leading-none">
									{loading ? "…" : name}
								</b>
								{loading ? (
									<Skeleton className="h-[11px] w-24 rounded-sm" />
								) : (
									<span
										className="truncate text-[11px] text-ink-3 leading-none"
										title={caption}
									>
										{caption}
									</span>
								)}
							</span>
							<ChevronsUpDownIcon
								aria-hidden
								className="size-3.5 shrink-0 text-ink-3"
							/>
						</>
					)}
				</DropdownMenuTrigger>
				<DropdownMenuContent align="start" sideOffset={6} className="w-[248px]">
					<DropdownMenuGroup>
						<DropdownMenuLabel>Teams</DropdownMenuLabel>
						{teams.map((t) => (
							<DropdownMenuItem
								render={<button type="button" />}
								key={t.id}
								aria-current={t.current ? "true" : undefined}
								disabled={change.isPending}
								className="gap-2.5 py-2"
								onClick={() => {
									if (!t.current) change.mutate(t);
								}}
							>
								<TeamTile name={t.name} size={20} />
								<span className="flex min-w-0 flex-1 flex-col gap-1">
									<span className="truncate leading-tight">{t.name}</span>
									<span className="text-2xs text-ink-3 leading-tight">
										{t.deletion
											? t.deletion.state === "deleting"
												? "Deleting…"
												: "Deletion failed"
											: `${roleText(t.role)} · ${t.memberCount} ${t.memberCount === 1 ? "member" : "members"}`}
									</span>
								</span>
								{switchingTo === t.id ? (
									<Spinner aria-label="Switching" />
								) : t.current ? (
									<CheckIcon aria-hidden className="size-3 text-primary" />
								) : null}
							</DropdownMenuItem>
						))}
					</DropdownMenuGroup>
					{canCreateTeam(instance.data) ? (
						<>
							<DropdownMenuSeparator />
							<DropdownMenuGroup>
								<DropdownMenuItem
									render={<button type="button" />}
									className="gap-2.5"
									onClick={() => setCreating(true)}
								>
									<PlusIcon />
									Create team
								</DropdownMenuItem>
							</DropdownMenuGroup>
						</>
					) : null}
				</DropdownMenuContent>
			</DropdownMenu>
			<CreateTeamDialog open={creating} onOpenChange={setCreating} />
		</>
	);
}
