import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useRouter } from "@tanstack/react-router";
import { ShieldAlertIcon } from "lucide-react";
import { type ReactNode, useState } from "react";
import { getMySecurityOptions } from "#/api/gen/@tanstack/react-query.gen";
import { meQuery } from "#/api/hooks";
import { Button } from "#/components/ui/button";
import { Card, CardHeader, CardTitle } from "#/components/ui/card";
import { AddPasskeyDialog } from "#/features/settings/security/passkey-dialogs";
import { passkeysSupported } from "#/lib/webauthn";
import { TotpEnrol } from "./totp-enrol";

/**
 * Stands in for the page while the current team requires two-factor
 * authentication and this account has not turned it on. The server refuses
 * everything else with 403 `two_factor_enrollment_required` until it is, so
 * this is the way forward rather than a decoration. The sidebar stays, so the
 * person can still switch to a team that does not require it.
 */
export function EnrolmentGate({ children }: { children: ReactNode }) {
	const security = useQuery({
		...getMySecurityOptions(),
		staleTime: 60_000,
		retry: false,
	});
	if (!security.data?.enrollmentNeeded) return children;
	return (
		<EnrolmentRequired
			teams={security.data.requiredByTeams ?? []}
			hasPassword={security.data.hasPassword}
			passkeysUnavailable={Boolean(security.data.passkeysUnavailable)}
		/>
	);
}

function EnrolmentRequired({
	teams,
	hasPassword,
	passkeysUnavailable,
}: {
	teams: readonly { id: string; name: string }[];
	hasPassword: boolean;
	passkeysUnavailable: boolean;
}) {
	const router = useRouter();
	const queryClient = useQueryClient();
	const [passkeyOpen, setPasskeyOpen] = useState(false);
	const refresh = () => {
		// Everything that was refused while the gate was up is allowed now:
		// load it again.
		void queryClient.invalidateQueries();
		void router.invalidate();
	};
	const me = useQuery(meQuery());
	const names = teams.map((team) => team.name).join(", ");
	return (
		<div className="mx-auto flex w-full max-w-[640px] flex-col gap-5 px-5 py-10">
			<div className="flex items-start gap-3">
				<ShieldAlertIcon
					aria-hidden="true"
					className="mt-1 size-5 shrink-0 text-status-staged"
				/>
				<div className="flex flex-col gap-1.5">
					<h1 className="type-page-title text-[22px] text-ink-1">
						Set up two-factor authentication to continue
					</h1>
					<p className="type-body text-pretty text-ink-3">
						{names
							? `${names} requires every member to sign in with a second step.`
							: "This team requires every member to sign in with a second step."}{" "}
						Nothing in the team is available to you until you turn it on.
					</p>
				</div>
			</div>
			<Card>
				<CardHeader>
					<CardTitle>Authenticator app</CardTitle>
				</CardHeader>
				<div className="px-5 py-5">
					<TotpEnrol
						account={me.data?.email ?? ""}
						doneLabel="Continue"
						onFinished={refresh}
					/>
				</div>
			</Card>
			{passkeysSupported() && !passkeysUnavailable ? (
				<Card>
					<CardHeader>
						<CardTitle>Passkey</CardTitle>
					</CardHeader>
					<div className="flex flex-col items-start gap-3 px-5 py-5">
						<p className="type-body text-pretty text-ink-3">
							Or use a passkey: a fingerprint, face, PIN or security key on this
							device. It counts as your second step.
						</p>
						<Button variant="secondary" onClick={() => setPasskeyOpen(true)}>
							Add a passkey
						</Button>
					</div>
					<AddPasskeyDialog
						open={passkeyOpen}
						onOpenChange={(open) => {
							setPasskeyOpen(open);
							if (!open) refresh();
						}}
						hasPassword={hasPassword}
						account={me.data?.email ?? ""}
					/>
				</Card>
			) : null}
		</div>
	);
}
