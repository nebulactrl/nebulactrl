import { DitherAvatar } from "@nebulactrl/dither/interface/avatar";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { cn } from "cn";
import {
	BookOpenIcon,
	DownloadIcon,
	FolderIcon,
	LogOutIcon,
	UserRoundCogIcon,
} from "lucide-react";
import { useCallback } from "react";
import { errorMessage } from "#/api/client-config";
import { logout } from "#/api/gen/sdk.gen";
import type { Me } from "#/api/models";
import { Badge } from "#/components/ui/badge";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuGroup,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "#/components/ui/dropdown-menu";
import { Skeleton } from "#/components/ui/skeleton";
import { toast } from "#/components/ui/toast";
import { useUpdateNotice } from "#/features/settings/use-system-update";
import { ROLE_LABEL } from "#/features/teams/roles";
import { userCaption, versionLabel } from "./user-card";

/** The person's role in the current team: "Owner", "Admin", "Developer" or "Viewer". */
export function roleLabel(user: Pick<Me, "principal">): string {
	const role = user.principal.role;
	return role ? ROLE_LABEL[role] : "No team";
}

/** Signs out and lands on /login; a failure leaves the session and says so. */
export function useSignOut(): () => void {
	const queryClient = useQueryClient();
	const navigate = useNavigate();
	const signOut = useMutation({
		mutationFn: () => logout(),
		onSuccess: () => {
			// Drops every cached response, including ones still in flight.
			queryClient.clear();
			void navigate({ to: "/login" });
		},
		onError: (error) => {
			toast.error(
				"Sign-out failed",
				`${errorMessage(error)} You are still signed in; try again.`,
			);
		},
	});
	const { mutate } = signOut;
	return useCallback(() => mutate(), [mutate]);
}

/**
 * The card at the foot of the sidebar: avatar, name, email. It opens the
 * person's own menu, which also names the build; collapsed, the avatar alone
 * remains.
 */
export function UserMenu({
	user,
	version,
	versionPending,
	collapsed,
}: {
	user: Me;
	/** The control plane's version, from `GET /system`. */
	version: string | undefined;
	/** `GET /system` has not answered yet: hold the version's place. */
	versionPending: boolean;
	collapsed: boolean;
}) {
	const navigate = useNavigate();
	const signOut = useSignOut();
	const name = user.name || user.email;
	// Only the person who can install the update is told about it.
	const notice = useUpdateNotice();
	const chip = user.instanceAdmin ? notice.chip : null;

	return (
		<DropdownMenu>
			<DropdownMenuTrigger
				data-nomorph
				aria-label={`Account menu for ${name}${chip ? `, ${chip}` : ""}`}
				className={cn(
					"surface-raised pressable flex min-w-0 cursor-pointer items-center gap-2.5 rounded-card text-left outline-none",
					collapsed ? "size-10 justify-center" : "w-full px-2.5 py-2",
				)}
			>
				<DitherAvatar
					aria-hidden
					name={user.email || name}
					size={26}
					className="shadow-[0_0_0_1px_rgb(2_4_10/.6)]"
				/>
				{collapsed ? null : (
					<span className="flex min-w-0 flex-col gap-[3px]">
						<span className="truncate font-semibold text-[12.5px] text-ink-1 leading-none">
							{name}
						</span>
						<span
							className={cn(
								"flex items-center gap-1 text-[11px] text-ink-3 leading-none",
								chip ? "flex-wrap gap-y-1" : "truncate",
							)}
						>
							{userCaption({
								name: user.name,
								email: user.email,
								roleLabel: roleLabel(user),
							})}
							{chip ? <Badge variant="accent">{chip}</Badge> : null}
						</span>
					</span>
				)}
			</DropdownMenuTrigger>
			<DropdownMenuContent
				align="start"
				side={collapsed ? "right" : "top"}
				sideOffset={8}
				className="w-[240px]"
			>
				<div className="mb-1.5 flex items-center gap-2.5 border-line border-b p-2.5">
					<DitherAvatar aria-hidden name={user.email || name} size="lg" />
					<div className="flex min-w-0 flex-col gap-[5px]">
						<span className="truncate font-medium text-sm leading-none">
							{name}
						</span>
						<span className="truncate font-mono text-ink-3 text-xs leading-none">
							{user.email}
						</span>
					</div>
				</div>
				{chip ? (
					<>
						<DropdownMenuGroup>
							<DropdownMenuItem
								render={<button type="button" />}
								onClick={() =>
									navigate({
										to: "/admin/$section",
										params: { section: "updates" },
									})
								}
							>
								<DownloadIcon />
								{chip}
							</DropdownMenuItem>
						</DropdownMenuGroup>
						<DropdownMenuSeparator />
					</>
				) : null}
				<DropdownMenuGroup>
					<DropdownMenuItem
						render={<button type="button" />}
						onClick={() =>
							navigate({
								to: "/account/$section",
								params: { section: "profile" },
							})
						}
					>
						<UserRoundCogIcon />
						Account settings
					</DropdownMenuItem>
					<DropdownMenuItem
						render={<button type="button" />}
						onClick={() => navigate({ to: "/docs" })}
					>
						<BookOpenIcon />
						Docs
					</DropdownMenuItem>
					<DropdownMenuItem
						render={<button type="button" />}
						onClick={() => navigate({ to: "/" })}
					>
						<FolderIcon />
						All projects
					</DropdownMenuItem>
				</DropdownMenuGroup>
				<DropdownMenuSeparator />
				<DropdownMenuGroup>
					<DropdownMenuItem render={<button type="button" />} onClick={signOut}>
						<LogOutIcon />
						Sign out
					</DropdownMenuItem>
				</DropdownMenuGroup>
				{version || versionPending ? (
					<>
						<DropdownMenuSeparator />
						<p className="flex items-center gap-1.5 px-2.5 py-1.5 text-2xs text-ink-4 leading-none">
							NebulaCtrl
							{version ? (
								<span className="font-mono">{versionLabel(version)}</span>
							) : (
								<Skeleton className="h-[9px] w-10 rounded-sm" />
							)}
						</p>
					</>
				) : null}
			</DropdownMenuContent>
		</DropdownMenu>
	);
}
