import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import {
	getMySecurityOptions,
	getMySecurityQueryKey,
	listLoginProvidersOptions,
} from "#/api/gen/@tanstack/react-query.gen";
import { meQuery } from "#/api/hooks";
import { ssoLinkOutcome } from "#/components/auth/auth-messages";
import { ErrorState } from "#/components/common/error-state";
import { Skeleton } from "#/components/ui/skeleton";
import { toast } from "#/components/ui/toast";
import { passkeysSupported } from "#/lib/webauthn";
import { LinkedAccountsCard } from "./linked-accounts-card";
import { PasskeysCard } from "./passkeys-card";
import { securityPrimary } from "./primary-action";
import { TwoFactorCard } from "./two-factor-card";

/**
 * Account settings → Security: this account's passkeys, two-step verification
 * and single-sign-on accounts. The team's requirement lives in Team settings →
 * General, the sign-in providers in Admin → Sign-in and teams. One card at most
 * carries the primary key (`securityPrimary`).
 */
export function SecuritySection() {
	const queryClient = useQueryClient();
	const me = useQuery(meQuery());
	const security = useQuery(getMySecurityOptions());
	const providers = useQuery(listLoginProvidersOptions());

	// A link started here comes back with its result in the fragment.
	useEffect(() => {
		const outcome = ssoLinkOutcome(window.location.hash);
		if (!outcome) return;
		window.history.replaceState(
			null,
			"",
			window.location.pathname + window.location.search,
		);
		if (outcome.kind === "linked") {
			toast.success("Single sign-on account linked");
			void queryClient.invalidateQueries({ queryKey: getMySecurityQueryKey() });
		} else {
			toast.error("Could not link the account", outcome.message);
		}
	}, [queryClient]);

	if (security.isPending || me.isPending) {
		return <Skeleton className="h-64 rounded-card" />;
	}
	if (!security.data || !me.data) {
		return (
			<ErrorState
				frame="raised"
				title="Security settings did not load"
				body="The control plane did not answer, so nothing was changed. Try again in a moment."
				retry={() => {
					void security.refetch();
					void me.refetch();
				}}
			/>
		);
	}

	const identities = security.data.identities ?? [];
	const ssoProviders = providers.data?.providers ?? [];
	const primary = securityPrimary({
		totpEnabled: security.data.totpEnabled,
		passkeyCount: (security.data.passkeys ?? []).length,
		canAddPasskey: passkeysSupported() && !security.data.passkeysUnavailable,
	});

	return (
		<div className="flex flex-col gap-5">
			<PasskeysCard
				security={security.data}
				account={me.data.email}
				primary={primary === "passkey"}
			/>
			<TwoFactorCard
				security={security.data}
				account={me.data.email}
				primary={primary === "totp"}
			/>
			{identities.length > 0 || ssoProviders.length > 0 ? (
				<LinkedAccountsCard
					identities={identities}
					providers={ssoProviders}
					hasPassword={security.data.hasPassword}
				/>
			) : null}
		</div>
	);
}
