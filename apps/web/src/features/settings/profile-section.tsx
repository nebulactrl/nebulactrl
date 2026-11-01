import { DitherAvatar } from "@nebulactrl/dither/interface/avatar";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
	type ComponentProps,
	type FormEvent,
	useEffect,
	useState,
} from "react";
import { errorMessage } from "#/api/client-config";
import { getMeQueryKey } from "#/api/gen/@tanstack/react-query.gen";
import { changePassword, updateMe } from "#/api/gen/sdk.gen";
import { meQuery } from "#/api/hooks";
import { ErrorState } from "#/components/common/error-state";
import { Button } from "#/components/ui/button";
import { CardFooter } from "#/components/ui/card";
import { CheckLabel } from "#/components/ui/check-label";
import {
	Field,
	FieldDescription,
	FieldError,
	FieldLabel,
} from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Skeleton, SkeletonReveal } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { toast } from "#/components/ui/toast";
import { SettingsPlate } from "./section";
import { KeptTokens } from "./security/kept-tokens";

type KeptTokenList = ComponentProps<typeof KeptTokens>["tokens"];

/** The server's rule (internal/auth MinPasswordLength). */
const MIN_PASSWORD_LENGTH = 10;

function passwordProblem(next: string, confirm: string): string | undefined {
	if (next.length < MIN_PASSWORD_LENGTH) {
		return `Use at least ${MIN_PASSWORD_LENGTH} characters.`;
	}
	if (next !== confirm) return "The two passwords do not match.";
	return undefined;
}

/**
 * A form action that has nothing to submit yet: the label keeps full ink
 * (readable at 4.5:1) and the state reads from the flat, frameless shape.
 */
const IDLE_ACTION =
	"disabled:bg-transparent disabled:text-ink-3 disabled:opacity-100 disabled:shadow-none";

function NameForm({ name, email }: { name: string; email: string }) {
	const queryClient = useQueryClient();
	const [value, setValue] = useState(name);
	useEffect(() => setValue(name), [name]);
	const save = useMutation({
		mutationFn: async () => {
			await updateMe({ body: { name: value.trim() } });
		},
		onSuccess: async () => {
			toast.success("Profile saved");
			await queryClient.invalidateQueries({ queryKey: getMeQueryKey() });
		},
	});
	const dirty = value.trim() !== name && value.trim() !== "";
	const submit = (event: FormEvent) => {
		event.preventDefault();
		if (dirty && !save.isPending) save.mutate();
	};
	return (
		<form onSubmit={submit} className="contents">
			<div className="flex max-w-xl flex-col gap-4 px-5 py-5">
				<div className="flex items-end gap-4">
					<DitherAvatar
						aria-hidden
						name={email || name}
						size="lg"
						className="shrink-0 shadow-[0_0_0_1px_rgb(2_4_10/.6)]"
					/>
					<Field className="min-w-0 flex-1">
						<FieldLabel htmlFor="profile-name">Name</FieldLabel>
						<Input
							id="profile-name"
							scale="lg"
							autoComplete="name"
							value={value}
							onChange={(e) => setValue(e.target.value)}
						/>
					</Field>
				</div>
				{save.isError ? (
					<FieldError>{errorMessage(save.error)}</FieldError>
				) : null}
			</div>
			<CardFooter>
				<Button
					type="submit"
					variant={dirty ? "primary" : "secondary"}
					className={IDLE_ACTION}
					disabled={!dirty || save.isPending}
				>
					{save.isPending ? <Spinner /> : null}
					Save name
				</Button>
			</CardFooter>
		</form>
	);
}

function PasswordForm() {
	const [current, setCurrent] = useState("");
	const [next, setNext] = useState("");
	const [confirm, setConfirm] = useState("");
	const [touched, setTouched] = useState(false);
	// Other sessions end with a new password; API tokens stay unless asked.
	const [revoke, setRevoke] = useState(false);
	const [outcome, setOutcome] = useState<
		{ revoked: number; kept: KeptTokenList } | undefined
	>();
	const change = useMutation({
		mutationFn: async () => {
			const { data } = await changePassword({
				body: {
					currentPassword: current,
					newPassword: next,
					revokeApiTokens: revoke,
				},
			});
			return data;
		},
		onSuccess: (data) => {
			toast.success("Password changed", "Your other sessions were signed out.");
			setOutcome({
				revoked: data.revokedTokens,
				kept: data.keptTokens ?? [],
			});
			setCurrent("");
			setNext("");
			setConfirm("");
			setTouched(false);
			setRevoke(false);
		},
	});
	const problem = passwordProblem(next, confirm);
	const submit = (event: FormEvent) => {
		event.preventDefault();
		setTouched(true);
		if (current === "" || problem || change.isPending) return;
		change.mutate();
	};
	const ready = current !== "" && next !== "" && !change.isPending;
	return (
		<form onSubmit={submit} className="contents">
			<div className="flex max-w-xl flex-col gap-4 px-5 py-5">
				<Field>
					<FieldLabel htmlFor="pw-current">Current password</FieldLabel>
					<Input
						id="pw-current"
						type="password"
						scale="lg"
						autoComplete="current-password"
						value={current}
						onChange={(e) => setCurrent(e.target.value)}
					/>
				</Field>
				<Field>
					<FieldLabel htmlFor="pw-new">New password</FieldLabel>
					<Input
						id="pw-new"
						type="password"
						scale="lg"
						autoComplete="new-password"
						value={next}
						onChange={(e) => setNext(e.target.value)}
						aria-invalid={touched && problem !== undefined}
					/>
					<FieldDescription>
						At least {MIN_PASSWORD_LENGTH} characters.
					</FieldDescription>
				</Field>
				<Field>
					<FieldLabel htmlFor="pw-confirm">Repeat new password</FieldLabel>
					<Input
						id="pw-confirm"
						type="password"
						scale="lg"
						autoComplete="new-password"
						value={confirm}
						onChange={(e) => setConfirm(e.target.value)}
					/>
				</Field>
				{touched && problem ? <FieldError>{problem}</FieldError> : null}
				<CheckLabel checked={revoke} onCheckedChange={setRevoke}>
					Also revoke my API tokens
				</CheckLabel>
				{change.isError ? (
					<FieldError>{errorMessage(change.error)}</FieldError>
				) : null}
				{outcome ? (
					<KeptTokens tokens={outcome.kept} revoked={outcome.revoked} />
				) : null}
			</div>
			<CardFooter>
				<Button
					type="submit"
					variant={ready ? "primary" : "secondary"}
					className={IDLE_ACTION}
					disabled={!ready}
				>
					{change.isPending ? <Spinner /> : null}
					Change password
				</Button>
			</CardFooter>
		</form>
	);
}

/** Profile name and password change (PATCH /me, POST /me/password). */
export function ProfileSection() {
	const me = useQuery(meQuery());
	return (
		<SkeletonReveal
			loading={me.isPending}
			skeleton={<Skeleton className="h-64 rounded-card" />}
		>
			{() =>
				me.isError && !me.data ? (
					<ErrorState
						frame="raised"
						title="Your profile did not load"
						body="The control plane did not answer, so nothing was changed. Try again in a moment."
						retry={() => void me.refetch()}
					/>
				) : (
					<div className="flex min-w-0 flex-col gap-5">
						<SettingsPlate
							id="profile-account"
							title="Account"
							flush
							actions={
								<span className="min-w-0 truncate font-mono text-ink-3 text-xs">
									{me.data?.email}
								</span>
							}
						>
							<NameForm
								name={me.data?.name ?? ""}
								email={me.data?.email ?? ""}
							/>
						</SettingsPlate>
						<SettingsPlate id="profile-password" title="Password" flush>
							<PasswordForm />
						</SettingsPlate>
					</div>
				)
			}
		</SkeletonReveal>
	);
}
