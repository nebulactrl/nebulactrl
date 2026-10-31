import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { errorMessage } from "#/api/client-config";
import {
	getMySecurityOptions,
	getTeamSecurityOptions,
	getTeamSecurityQueryKey,
} from "#/api/gen/@tanstack/react-query.gen";
import { setTeamSecurity } from "#/api/gen/sdk.gen";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { FieldError } from "#/components/ui/field";
import { Skeleton } from "#/components/ui/skeleton";
import { Switch } from "#/components/ui/switch";
import { toast } from "#/components/ui/toast";
import { SettingsPlate } from "#/features/settings/section";

/**
 * Team settings → General → Require two-step verification. Members without
 * it are stopped at an enrolment screen until they turn it on; whoever flips
 * the switch must have it themselves first (the server answers 409
 * `enrol_first`).
 */
export function TeamRequirementCard({ teamId }: { teamId: string }) {
	const queryClient = useQueryClient();
	const state = useQuery(getTeamSecurityOptions({ path: { teamId } }));
	const security = useQuery(getMySecurityOptions());
	const toggle = useMutation({
		mutationFn: async (require2fa: boolean) =>
			(await setTeamSecurity({ path: { teamId }, body: { require2fa } })).data,
		onSuccess: async (data) => {
			toast.success(
				data.require2fa
					? "Two-step verification is now required"
					: "Two-step verification is no longer required",
			);
			await queryClient.invalidateQueries({
				queryKey: getTeamSecurityQueryKey({ path: { teamId } }),
			});
		},
	});

	const data = state.data;
	const waiting = data ? data.members - data.enrolled : 0;
	const mine = security.data;
	const hasOwnTwoFactor = mine
		? mine.totpEnabled || (mine.passkeys ?? []).length > 0 || mine.sessionMfa
		: true;

	return (
		<SettingsPlate
			id="require-two-step"
			title="Require two-step verification"
			tipLabel="About requiring two-step verification"
			tip="Until a member turns it on they see only a setup screen. You need it on your own account first."
		>
			{state.isPending ? (
				<Skeleton className="h-10 rounded-ctl" />
			) : data ? (
				<>
					<div className="flex items-center justify-between gap-4">
						<label
							htmlFor="require-two-step-switch"
							className="type-secondary text-ink-2"
						>
							Every member turns it on before acting in this team.
						</label>
						<Switch
							id="require-two-step-switch"
							checked={data.require2fa}
							disabled={
								toggle.isPending || (!data.require2fa && !hasOwnTwoFactor)
							}
							onCheckedChange={(next) => toggle.mutate(next)}
						/>
					</div>
					<p className="text-ink-3 text-xs">
						{data.enrolled} of {data.members}{" "}
						{data.members === 1 ? "member has" : "members have"} it on
						{data.require2fa && waiting > 0
							? `; ${waiting} ${waiting === 1 ? "is" : "are"} waiting to set it up.`
							: "."}{" "}
						API tokens of a member without it stop working in this team.
					</p>
					{!hasOwnTwoFactor && !data.require2fa ? (
						<Alert variant="warning">
							<AlertDescription>
								Turn on two-step verification for your own account first, under{" "}
								<Link
									to="/account/$section"
									params={{ section: "security" }}
									className="text-accent-ink underline"
								>
									Account settings → Security
								</Link>
								. The server refuses the switch until you have it.
							</AlertDescription>
						</Alert>
					) : null}
					{toggle.isError ? (
						<FieldError>{errorMessage(toggle.error)}</FieldError>
					) : null}
				</>
			) : (
				<FieldError>Could not load this team's setting.</FieldError>
			)}
		</SettingsPlate>
	);
}
