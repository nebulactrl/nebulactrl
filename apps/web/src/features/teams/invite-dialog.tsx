import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useState } from "react";
import { errorMessage } from "#/api/client-config";
import { listTeamInvitesQueryKey } from "#/api/gen/@tanstack/react-query.gen";
import { createTeamInvite } from "#/api/gen/sdk.gen";
import type { CreatedInvite } from "#/api/gen/types.gen";
import { projectsQuery } from "#/api/hooks";
import { CopyButton } from "#/components/common/copy-button";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { CheckLabel } from "#/components/ui/check-label";
import {
	Dialog,
	DialogBody,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "#/components/ui/dialog";
import {
	Field,
	FieldDescription,
	FieldError,
	FieldLabel,
} from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { OptionCard, OptionCardGroup } from "#/components/ui/option-card";
import { Segmented } from "#/components/ui/segmented";
import { Spinner } from "#/components/ui/spinner";
import {
	absoluteInviteLink,
	assignableRoles,
	DEFAULT_INVITE_EXPIRY,
	describeDelivery,
	INVITE_EXPIRIES,
	ROLE_LABEL,
	ROLE_SUMMARY,
	type Role,
	supportsProjectRestriction,
} from "./roles";

/**
 * Invite people to the team: by email (one person, mailed when the team or the
 * instance can send email) or as a link anyone holding it can use until it
 * expires or runs out. The link is shown once, with a copy button.
 */
export function InviteDialog({
	open,
	onOpenChange,
	teamId,
	actorRole,
}: {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	teamId: string;
	actorRole: Role | undefined;
}) {
	const queryClient = useQueryClient();
	const roles = assignableRoles(actorRole);
	const projects = useQuery({ ...projectsQuery(), enabled: open });
	const [kind, setKind] = useState<"email" | "link">("email");
	const [email, setEmail] = useState("");
	const [role, setRole] = useState<Role>("developer");
	const [restricted, setRestricted] = useState<ReadonlySet<string>>(new Set());
	const [uses, setUses] = useState("1");
	const [expiry, setExpiry] = useState(DEFAULT_INVITE_EXPIRY);
	const [created, setCreated] = useState<CreatedInvite | null>(null);

	useEffect(() => {
		if (!open) return;
		setKind("email");
		setEmail("");
		setRole("developer");
		setRestricted(new Set());
		setUses("1");
		setExpiry(DEFAULT_INVITE_EXPIRY);
		setCreated(null);
	}, [open]);

	const create = useMutation({
		mutationFn: async () => {
			const days = INVITE_EXPIRIES.find((e) => e.id === expiry)?.days;
			const maxUses = Number.parseInt(uses, 10);
			const { data } = await createTeamInvite({
				path: { teamId },
				body: {
					role,
					...(kind === "email" ? { email: email.trim() } : {}),
					...(kind === "link" && Number.isFinite(maxUses) ? { maxUses } : {}),
					...(days ? { expiresInDays: days } : {}),
					...(supportsProjectRestriction(role) && restricted.size > 0
						? { projectIds: [...restricted] }
						: {}),
				},
			});
			return data;
		},
		onSuccess: async (data) => {
			setCreated(data);
			await queryClient.invalidateQueries({
				queryKey: listTeamInvitesQueryKey({ path: { teamId } }),
			});
		},
	});

	const valid = kind === "link" || email.trim() !== "";
	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (!valid || create.isPending) return;
		create.mutate();
	};

	const toggle = (id: string, on: boolean) => {
		const next = new Set(restricted);
		if (on) next.add(id);
		else next.delete(id);
		setRestricted(next);
	};

	const link = created
		? absoluteInviteLink(created.link, window.location.origin)
		: "";

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent size="md">
				{created ? (
					<>
						<DialogHeader>
							<DialogTitle>
								{created.invite.email
									? `Invite for ${created.invite.email}`
									: "Invite link ready"}
							</DialogTitle>
							<DialogDescription>
								{describeDelivery(
									created.delivery,
									created.invite.email,
									created.emailError,
								)}
							</DialogDescription>
						</DialogHeader>
						<DialogBody>
							<div className="flex flex-wrap items-center gap-2">
								<Badge tone="warn" shape="capsule">
									Shown once
								</Badge>
								<span className="text-ink-3 text-xs">
									The link is not kept: copy it now, or create a new invite.
								</span>
							</div>
							<div className="surface-well flex items-start gap-3 rounded-ctl py-2.5 pr-2.5 pl-3.5">
								<code
									data-testid="invite-link"
									className="min-w-0 flex-1 select-all break-all py-1 font-mono text-ink-1 text-xs leading-[1.7]"
								>
									{link}
								</code>
								<CopyButton variant="chip" value={link} className="shrink-0" />
							</div>
							<p className="text-ink-3 text-xs leading-relaxed">
								Joins as{" "}
								<b className="text-ink-2">{ROLE_LABEL[created.invite.role]}</b>.{" "}
								{created.invite.email
									? "It works once, for that address only."
									: `It works ${created.invite.maxUses === 1 ? "once" : `${created.invite.maxUses} times`} and expires ${new Date(created.invite.expiresAt).toLocaleDateString()}.`}
							</p>
						</DialogBody>
						<DialogFooter>
							<Button variant="primary" onClick={() => onOpenChange(false)}>
								Done
							</Button>
						</DialogFooter>
					</>
				) : (
					<form onSubmit={submit} className="contents">
						<DialogHeader>
							<DialogTitle>Invite people</DialogTitle>
							<DialogDescription>
								They join this team with the role you pick. New people create an
								account from the invite; people who already have one sign in.
							</DialogDescription>
						</DialogHeader>
						<DialogBody>
							<Segmented
								aria-label="Invite by"
								font="sans"
								density="regular"
								value={kind}
								onValueChange={setKind}
								options={[
									{ value: "email", label: "By email" },
									{ value: "link", label: "Share a link" },
								]}
							/>
							{kind === "email" ? (
								<Field>
									<FieldLabel htmlFor="invite-email">Email address</FieldLabel>
									<Input
										id="invite-email"
										scale="lg"
										type="email"
										autoFocus
										autoComplete="off"
										value={email}
										onChange={(event) => setEmail(event.target.value)}
									/>
									<FieldDescription>
										One person. Mailed through this team's email channel or the
										instance's SMTP relay; otherwise you get a link to send
										yourself.
									</FieldDescription>
								</Field>
							) : (
								<div className="grid gap-3 sm:grid-cols-2">
									<Field>
										<FieldLabel htmlFor="invite-uses">
											People who can use it
										</FieldLabel>
										<Input
											id="invite-uses"
											scale="lg"
											inputMode="numeric"
											value={uses}
											onChange={(event) =>
												setUses(event.target.value.replace(/\D/g, ""))
											}
										/>
									</Field>
									<Field>
										<FieldLabel id="invite-expiry-label">Expires in</FieldLabel>
										<div>
											<Segmented
												aria-label="Expiry"
												font="sans"
												density="regular"
												value={expiry}
												onValueChange={setExpiry}
												options={INVITE_EXPIRIES.map((e) => ({
													value: e.id,
													label: e.label,
												}))}
											/>
										</div>
									</Field>
								</div>
							)}
							<Field>
								<FieldLabel id="invite-role-label">Role</FieldLabel>
								<OptionCardGroup
									value={role}
									onValueChange={(next) => {
										if (roles.includes(next as Role)) setRole(next as Role);
									}}
									aria-labelledby="invite-role-label"
									className="grid grid-cols-2"
								>
									{roles.map((r) => (
										<OptionCard
											key={r}
											value={r}
											title={ROLE_LABEL[r]}
											description={ROLE_SUMMARY[r]}
											className="min-w-0"
										/>
									))}
								</OptionCardGroup>
							</Field>
							{supportsProjectRestriction(role) ? (
								<Field>
									<FieldLabel id="invite-projects-label">Projects</FieldLabel>
									<fieldset
										aria-labelledby="invite-projects-label"
										className="surface-well m-0 flex max-h-32 min-w-0 flex-col gap-2 overflow-y-auto overscroll-contain rounded-card p-3"
									>
										{projects.data && projects.data.length > 0 ? (
											projects.data.map((project) => (
												<CheckLabel
													key={project.id}
													className="gap-2.5 text-sm"
													checked={restricted.has(project.id)}
													onCheckedChange={(on) => toggle(project.id, on)}
												>
													{project.name}
												</CheckLabel>
											))
										) : (
											<span className="text-ink-3 text-xs">
												{projects.isPending
													? "Loading projects…"
													: "No projects yet."}
											</span>
										)}
									</fieldset>
									<FieldDescription>
										{restricted.size === 0
											? "No project selected: they can see every project."
											: `Limited to ${restricted.size} ${restricted.size === 1 ? "project" : "projects"}; the rest answer 404.`}
									</FieldDescription>
								</Field>
							) : null}
							{create.isError ? (
								<FieldError>{errorMessage(create.error)}</FieldError>
							) : null}
						</DialogBody>
						<DialogFooter>
							<Button
								type="button"
								variant="ghost"
								onClick={() => onOpenChange(false)}
							>
								Cancel
							</Button>
							<Button
								type="submit"
								variant={valid ? "primary" : "secondary"}
								disabled={!valid || create.isPending}
							>
								{create.isPending ? <Spinner /> : null}
								{kind === "email" ? "Send invite" : "Create link"}
							</Button>
						</DialogFooter>
					</form>
				)}
			</DialogContent>
		</Dialog>
	);
}
