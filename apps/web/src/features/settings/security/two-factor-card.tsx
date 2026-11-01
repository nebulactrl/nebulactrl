import { ShieldCheckIcon } from "lucide-react";
import { useState } from "react";
import type { SecurityBody } from "#/api/gen/types.gen";
import { recoveryCodesLevel } from "#/components/auth/auth-messages";
import { TotpEnrolDialog } from "#/components/auth/totp-enrol";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import {
	Card,
	CardDescription,
	CardFooter,
	CardHeader,
	CardTitle,
} from "#/components/ui/card";
import {
	DisableTwoFactorDialog,
	RegenerateCodesDialog,
} from "./two-factor-dialogs";

/** Two-step verification: status, recovery codes left, turn on / replace codes / turn off. */
export function TwoFactorCard({
	security,
	account,
	primary,
}: {
	security: SecurityBody;
	account: string;
	/** This card owns the page's one primary key (see `securityPrimary`). */
	primary: boolean;
}) {
	const [enrolOpen, setEnrolOpen] = useState(false);
	const [regenOpen, setRegenOpen] = useState(false);
	const [disableOpen, setDisableOpen] = useState(false);
	const level = recoveryCodesLevel(security.recoveryCodesLeft);
	const passkeyCount = (security.passkeys ?? []).length;
	const hasFactor = security.totpEnabled || passkeyCount > 0;

	return (
		<Card>
			<CardHeader>
				<CardTitle className="flex items-center gap-2">
					<ShieldCheckIcon aria-hidden="true" className="size-4 text-ink-3" />
					Two-step verification
				</CardTitle>
				<Badge tone={hasFactor ? "ok" : "off"} lamp shape="capsule">
					{hasFactor ? "On" : "Off"}
				</Badge>
			</CardHeader>
			<div className="flex flex-col gap-3 px-5 py-5">
				<CardDescription>
					{security.totpEnabled
						? "Signing in asks for a 6-digit code from your authenticator app after your password."
						: passkeyCount > 0
							? "Signing in asks for one of your passkeys after your password. You can also add an authenticator app."
							: "Add a second step to signing in: a 6-digit code from an authenticator app, or a passkey. Anyone who learns your password still cannot sign in."}
				</CardDescription>
				{(security.requiredByTeams ?? []).length > 0 ? (
					<p className="text-pretty text-ink-2 text-xs leading-relaxed">
						Required by{" "}
						<span className="font-medium text-ink-1">
							{(security.requiredByTeams ?? [])
								.map((team) => team.name)
								.join(", ")}
						</span>
						.
					</p>
				) : null}
				{hasFactor && level !== "ok" ? (
					<Alert variant="warning">
						<AlertDescription>
							{level === "none"
								? "You have no recovery codes left. Create new ones now: without them a lost phone locks you out."
								: `Only ${security.recoveryCodesLeft} recovery ${security.recoveryCodesLeft === 1 ? "code is" : "codes are"} left. Create new ones soon.`}
						</AlertDescription>
					</Alert>
				) : null}
				{hasFactor && level === "ok" ? (
					<p className="text-ink-3 text-xs">
						{security.recoveryCodesLeft} recovery codes left.
					</p>
				) : null}
				{security.sessionMfa && !hasFactor ? (
					<p className="text-ink-3 text-xs">
						This session was verified by your identity provider.
					</p>
				) : null}
			</div>
			<CardFooter>
				{hasFactor ? (
					<Button variant="secondary" onClick={() => setRegenOpen(true)}>
						New recovery codes
					</Button>
				) : null}
				{security.totpEnabled ? (
					<Button variant="danger" onClick={() => setDisableOpen(true)}>
						Turn off authenticator app
					</Button>
				) : (
					<Button
						variant={primary ? "primary" : "secondary"}
						onClick={() => setEnrolOpen(true)}
					>
						{hasFactor ? "Add authenticator app" : "Turn on"}
					</Button>
				)}
			</CardFooter>
			<TotpEnrolDialog
				open={enrolOpen}
				onOpenChange={setEnrolOpen}
				account={account}
			/>
			<RegenerateCodesDialog
				open={regenOpen}
				onOpenChange={setRegenOpen}
				account={account}
				byPassword={!security.totpEnabled && security.hasPassword}
			/>
			<DisableTwoFactorDialog
				open={disableOpen}
				onOpenChange={setDisableOpen}
				hasPassword={security.hasPassword}
				requiredByTeams={(security.requiredByTeams ?? []).map(
					(team) => team.name,
				)}
			/>
		</Card>
	);
}
