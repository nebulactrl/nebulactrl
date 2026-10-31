import { DitherAvatar } from "@nebulactrl/dither/interface/avatar";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useRouter } from "@tanstack/react-router";
import { type FormEvent, useState } from "react";
import { errorCode, errorMessage } from "#/api/client-config";
import { previewInviteOptions } from "#/api/gen/@tanstack/react-query.gen";
import { acceptInvite } from "#/api/gen/sdk.gen";
import { meQuery } from "#/api/hooks";
import { AuthNotice } from "#/components/auth/auth-notice";
import { AuthCard } from "#/components/auth/auth-screen";
import { Button } from "#/components/ui/button";
import { Field, FieldDescription, FieldLabel } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Spinner } from "#/components/ui/spinner";
import { useDocumentTitle } from "#/lib/hooks/use-document-title";
import { isRole, ROLE_LABEL } from "./roles";

const MIN_PASSWORD = 10;

/**
 * The page an invite link opens, on the auth screen. Signed in: one button
 * joins the team. Signed out: create the account (the invite fixes the email
 * when it was sent to one address) or sign in first.
 */
export function InvitePage({
	token,
	signedIn,
}: {
	token: string;
	signedIn: boolean;
}) {
	const router = useRouter();
	const queryClient = useQueryClient();
	useDocumentTitle(["Join a team"]);
	const preview = useQuery({
		...previewInviteOptions({ path: { token } }),
		retry: false,
	});
	const [name, setName] = useState("");
	const [email, setEmail] = useState("");
	const [password, setPassword] = useState("");

	const accept = useMutation({
		mutationFn: () =>
			acceptInvite({
				path: { token },
				body: signedIn
					? {}
					: {
							name: name.trim(),
							password,
							...(preview.data?.email ? {} : { email: email.trim() }),
						},
			}),
		onSuccess: async () => {
			queryClient.clear();
			await queryClient.invalidateQueries({ queryKey: meQuery().queryKey });
			await router.invalidate();
			await router.navigate({ to: "/" });
		},
	});

	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (accept.isPending) return;
		accept.mutate();
	};

	if (preview.isPending) {
		return (
			<AuthCard>
				<div className="flex items-center gap-2 text-ink-3 text-sm">
					<Spinner /> Checking the invite…
				</div>
			</AuthCard>
		);
	}
	if (!preview.data) {
		const code = errorCode(preview.error);
		const body =
			code === "invite_expired"
				? "This invite has expired. Ask the person who invited you for a new one."
				: code === "invite_used"
					? "This invite has already been used. Ask for a new one, or sign in if you already joined."
					: "This invite link is not valid. It may have been revoked or mistyped.";
		return (
			<AuthCard>
				<h1 className="type-page-title text-[24px] text-ink-1">
					Invite unavailable
				</h1>
				<AuthNotice tone="destructive">{body}</AuthNotice>
				<Button
					render={<Link to={signedIn ? "/" : "/login"} />}
					nativeButton={false}
					variant="secondary"
				>
					{signedIn ? "Go to projects" : "Go to sign in"}
				</Button>
			</AuthCard>
		);
	}

	const invite = preview.data;
	const role = isRole(invite.role) ? ROLE_LABEL[invite.role] : invite.role;
	const failure = accept.error
		? errorCode(accept.error) === "account_exists"
			? "An account with that email already exists. Sign in, then open this link again."
			: errorMessage(accept.error)
		: undefined;
	const ready =
		signedIn ||
		(name.trim() !== "" &&
			password.length >= MIN_PASSWORD &&
			(invite.email || email.trim() !== ""));

	return (
		<AuthCard>
			<form onSubmit={submit} className="flex flex-col gap-4">
				<div className="mt-1.5 mb-1 flex flex-col gap-2">
					{invite.invitedBy ? (
						<div className="flex items-center gap-2.5">
							<DitherAvatar
								aria-hidden
								name={invite.invitedBy}
								size="lg"
								className="shadow-[0_0_0_1px_rgb(2_4_10/.6)]"
							/>
							<span className="type-body text-ink-3">
								<b className="text-ink-2">{invite.invitedBy}</b> invited you
							</span>
						</div>
					) : null}
					<h1 className="type-page-title text-[24px] text-ink-1">
						Join {invite.teamName}
					</h1>
					<span className="type-body text-pretty text-ink-3">
						You are invited as <b className="text-ink-2">{role}</b>.
					</span>
				</div>
				{failure ? <AuthNotice tone="destructive">{failure}</AuthNotice> : null}
				{signedIn ? null : (
					<>
						<Field>
							<FieldLabel htmlFor="invite-name">Your name</FieldLabel>
							<Input
								id="invite-name"
								scale="xl"
								autoComplete="name"
								autoFocus
								required
								value={name}
								onChange={(e) => setName(e.target.value)}
							/>
						</Field>
						<Field>
							<FieldLabel htmlFor="invite-email">Email</FieldLabel>
							<Input
								id="invite-email"
								scale="xl"
								type="email"
								autoComplete="username"
								required
								readOnly={Boolean(invite.email)}
								value={invite.email ?? email}
								onChange={(e) => setEmail(e.target.value)}
							/>
						</Field>
						<Field>
							<FieldLabel htmlFor="invite-password">Password</FieldLabel>
							<Input
								id="invite-password"
								scale="xl"
								type="password"
								autoComplete="new-password"
								minLength={MIN_PASSWORD}
								required
								value={password}
								onChange={(e) => setPassword(e.target.value)}
							/>
							<FieldDescription>
								At least {MIN_PASSWORD} characters.
							</FieldDescription>
						</Field>
					</>
				)}
				<Button
					type="submit"
					size="xl"
					className="w-full"
					disabled={!ready || accept.isPending}
				>
					{accept.isPending ? <Spinner /> : null}
					{signedIn ? `Join ${invite.teamName}` : "Create account and join"}
				</Button>
				{signedIn ? null : (
					<p className="text-center text-ink-3 text-xs">
						Already have an account?{" "}
						<Link
							to="/login"
							search={{ returnTo: `/invite/${token}` }}
							className="text-accent-ink underline"
						>
							Sign in
						</Link>
					</p>
				)}
			</form>
		</AuthCard>
	);
}
