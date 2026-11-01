import { FingerprintIcon } from "lucide-react";
import { useState } from "react";
import type { PasskeyBody, SecurityBody } from "#/api/gen/types.gen";
import { Relative } from "#/components/common/relative";
import { DocsLink } from "#/components/docs/docs-link";
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
import { passkeysSupported } from "#/lib/webauthn";
import {
	AddPasskeyDialog,
	RemovePasskeyDialog,
	RenamePasskeyDialog,
} from "./passkey-dialogs";

/**
 * Passkeys: sign in without a password, or confirm a password sign-in. Several
 * can be registered (a laptop, a phone, a security key) and each is named.
 */
export function PasskeysCard({
	security,
	account,
	primary,
}: {
	security: SecurityBody;
	account: string;
	/** This card owns the page's one primary key (see `securityPrimary`). */
	primary: boolean;
}) {
	const [addOpen, setAddOpen] = useState(false);
	const [renaming, setRenaming] = useState<PasskeyBody | null>(null);
	const [removing, setRemoving] = useState<PasskeyBody | null>(null);
	const passkeys = security.passkeys ?? [];
	const supported = passkeysSupported();
	const unavailable = security.passkeysUnavailable;
	const canAdd = supported && !unavailable;

	return (
		<Card>
			<CardHeader>
				<CardTitle className="flex items-center gap-2">
					<FingerprintIcon aria-hidden="true" className="size-4 text-ink-3" />
					Passkeys
				</CardTitle>
				<Badge tone={passkeys.length > 0 ? "ok" : "off"} lamp shape="capsule">
					{passkeys.length > 0
						? `${passkeys.length} ${passkeys.length === 1 ? "passkey" : "passkeys"}`
						: "None"}
				</Badge>
			</CardHeader>
			<div className="flex flex-col gap-3 px-5 py-5">
				<CardDescription>
					A passkey signs you in with a fingerprint, face, PIN or security key
					instead of a password. It also counts as your second step, so no code
					is asked for. Add one for each device you use.{" "}
					<DocsLink to="security/login-methods#passkeys-webauthn" />
				</CardDescription>
				{unavailable ? (
					<Alert variant="warning">
						<AlertDescription>{unavailable}</AlertDescription>
					</Alert>
				) : !supported ? (
					<Alert variant="warning">
						<AlertDescription>
							This browser cannot create passkeys here. Passkeys need HTTPS (or
							localhost) and a browser that supports WebAuthn.
						</AlertDescription>
					</Alert>
				) : null}
				{passkeys.length > 0 ? (
					<ul className="m-0 flex list-none flex-col divide-y divide-line p-0">
						{passkeys.map((passkey) => (
							<li
								key={passkey.id}
								className="flex items-center gap-3 py-2.5 first:pt-0 last:pb-0"
							>
								<FingerprintIcon
									aria-hidden="true"
									className="size-4 shrink-0 text-ink-3"
								/>
								<span className="flex min-w-0 flex-1 flex-col gap-0.5">
									<span className="flex items-center gap-2">
										<span className="truncate font-medium text-ink-1 text-sm">
											{passkey.name}
										</span>
										<Badge>
											{passkey.synced ? "Synced" : "This device only"}
										</Badge>
									</span>
									<span className="truncate font-mono text-2xs text-ink-3">
										Added <Relative iso={passkey.createdAt} />
										{" · "}
										{passkey.lastUsedAt ? (
											<>
												last used <Relative iso={passkey.lastUsedAt} />
											</>
										) : (
											"never used"
										)}
									</span>
								</span>
								<Button
									variant="ghost"
									size="sm"
									onClick={() => setRenaming(passkey)}
								>
									Rename
								</Button>
								<Button
									variant="ghost"
									size="sm"
									className="text-destructive-text"
									onClick={() => setRemoving(passkey)}
								>
									Remove
								</Button>
							</li>
						))}
					</ul>
				) : null}
				{passkeys.length > 0 && security.recoveryCodesLeft === 0 ? (
					<Alert variant="warning">
						<AlertDescription>
							You have no recovery codes left. Create new ones below: without
							them, losing every passkey locks you out.
						</AlertDescription>
					</Alert>
				) : null}
			</div>
			<CardFooter>
				<Button
					variant={primary ? "primary" : "secondary"}
					disabled={!canAdd}
					onClick={() => setAddOpen(true)}
				>
					Add a passkey
				</Button>
			</CardFooter>
			<AddPasskeyDialog
				open={addOpen}
				onOpenChange={setAddOpen}
				hasPassword={security.hasPassword}
				account={account}
			/>
			<RenamePasskeyDialog
				passkey={renaming}
				onOpenChange={(open) => {
					if (!open) setRenaming(null);
				}}
			/>
			<RemovePasskeyDialog
				passkey={removing}
				onOpenChange={(open) => {
					if (!open) setRemoving(null);
				}}
				hasPassword={security.hasPassword}
				lastSecondFactor={passkeys.length === 1 && !security.totpEnabled}
				requiredByTeams={(security.requiredByTeams ?? []).map(
					(team) => team.name,
				)}
			/>
		</Card>
	);
}
