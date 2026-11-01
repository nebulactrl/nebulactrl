import { useMutation } from "@tanstack/react-query";
import { errorCode, errorMessage } from "#/api/client-config";
import { createMemberPasswordResetLink } from "#/api/gen/sdk.gen";
import type { TeamMember } from "#/api/gen/types.gen";
import { AuthNotice } from "#/components/auth/auth-notice";
import { CopyButton } from "#/components/common/copy-button";
import { Relative } from "#/components/common/relative";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import {
	Dialog,
	DialogBody,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "#/components/ui/dialog";
import { Spinner } from "#/components/ui/spinner";
import { absoluteInviteLink } from "./roles";

/** The sentence for a refused reset-link request. */
function failureText(error: unknown, name: string): string {
	return errorCode(error) === "no_password"
		? `${name} signs in through an identity provider and has no password to reset.`
		: errorMessage(error);
}

/**
 * Generates a password reset link for a member, for an owner or admin to hand
 * over. It needs no email setup: the link is shown once, works once and expires
 * after 30 minutes. Using it signs the member out of every device.
 */
export function ResetLinkDialog({
	member,
	teamId,
	onClose,
}: {
	member: TeamMember | null;
	teamId: string;
	onClose: () => void;
}) {
	const create = useMutation({
		mutationFn: async (userId: string) => {
			const { data } = await createMemberPasswordResetLink({
				path: { teamId, userId },
			});
			return data;
		},
	});
	const close = () => {
		create.reset();
		onClose();
	};
	const created = create.data;
	const link = created
		? absoluteInviteLink(created.link, window.location.origin)
		: "";

	return (
		<Dialog open={member !== null} onOpenChange={(open) => !open && close()}>
			<DialogContent size="md">
				{member ? (
					<>
						<DialogHeader>
							<DialogTitle>Reset password for {member.name}</DialogTitle>
							<DialogDescription>
								{created
									? `Send this link to ${member.name} yourself.`
									: `Creates a link that lets ${member.name} choose a new password. It works once and expires after 30 minutes. Using it signs them out of every device.`}
							</DialogDescription>
						</DialogHeader>
						{created || create.error ? (
							<DialogBody>
								{created ? (
									<>
										<div className="flex flex-wrap items-center gap-2">
											<Badge tone="warn" shape="capsule">
												Shown once
											</Badge>
											<span className="text-ink-3 text-xs">
												The link is not kept: copy it now, or generate a new
												one.
											</span>
										</div>
										<div className="surface-well flex items-start gap-3 rounded-ctl py-2.5 pr-2.5 pl-3.5">
											<code
												data-testid="reset-link"
												className="min-w-0 flex-1 select-all break-all py-1 font-mono text-ink-1 text-xs leading-[1.7]"
											>
												{link}
											</code>
											<CopyButton
												variant="chip"
												value={link}
												className="shrink-0"
											/>
										</div>
										<p className="text-ink-3 text-xs leading-relaxed">
											Expires <Relative iso={created.expiresAt} />. If{" "}
											{member.name} has two-factor authentication, they still
											need their code to sign in.
										</p>
									</>
								) : create.error ? (
									<AuthNotice tone="destructive">
										{failureText(create.error, member.name)}
									</AuthNotice>
								) : null}
							</DialogBody>
						) : null}
						<DialogFooter>
							{created ? (
								<Button variant="primary" onClick={close}>
									Done
								</Button>
							) : (
								<>
									<Button variant="ghost" onClick={close}>
										Cancel
									</Button>
									<Button
										variant="primary"
										disabled={create.isPending}
										onClick={() => create.mutate(member.userId)}
									>
										{create.isPending ? <Spinner /> : null}
										Generate link
									</Button>
								</>
							)}
						</DialogFooter>
					</>
				) : null}
			</DialogContent>
		</Dialog>
	);
}
