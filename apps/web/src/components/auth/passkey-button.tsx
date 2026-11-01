import { useMutation } from "@tanstack/react-query";
import { FingerprintIcon } from "lucide-react";
import { errorCode, errorMessage, errorStatus } from "#/api/client-config";
import { passkeyLogin, passkeyLoginOptions } from "#/api/gen/sdk.gen";
import { Button } from "#/components/ui/button";
import {
	getPasskey,
	PasskeyError,
	passkeysSupported,
	type RequestOptionsJSON,
} from "#/lib/webauthn";
import { passkeyFailureText } from "./auth-messages";
import { AuthNotice } from "./auth-notice";

/**
 * Passwordless sign-in: the browser lists the passkeys it holds for this site,
 * the person verifies on the authenticator and the session starts. A passkey
 * is already two factors, so no code follows. Renders nothing where WebAuthn is
 * unavailable (plain HTTP, old browsers).
 */
export function PasskeyLoginButton({
	onSignedIn,
}: {
	onSignedIn: () => void | Promise<void>;
}) {
	const signIn = useMutation({
		mutationFn: async () => {
			const options = (await passkeyLoginOptions()).data as RequestOptionsJSON;
			const assertion = await getPasskey(options);
			await passkeyLogin({ body: assertion });
		},
		onSuccess: () => onSignedIn(),
	});

	if (!passkeysSupported()) return null;

	const failure = signIn.error
		? passkeyFailureText(
				signIn.error,
				errorStatus(signIn.error),
				errorCode(signIn.error),
				signIn.error instanceof PasskeyError
					? signIn.error.message
					: errorMessage(signIn.error),
				signIn.error instanceof PasskeyError ? signIn.error.kind : undefined,
			)
		: null;

	return (
		<div className="flex flex-col gap-2">
			{failure ? <AuthNotice tone="destructive">{failure}</AuthNotice> : null}
			<Button
				type="button"
				variant="secondary"
				size="xl"
				className="w-full"
				disabled={signIn.isPending}
				onClick={() => signIn.mutate()}
			>
				<FingerprintIcon />
				Sign in with a passkey
			</Button>
		</div>
	);
}
