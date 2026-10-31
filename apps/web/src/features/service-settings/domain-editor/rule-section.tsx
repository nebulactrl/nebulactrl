import { type ReactNode, useMemo, useState } from "react";
import { errorMessage } from "#/api/client-config";
import type { Service, ServiceDomain } from "#/api/models";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Segmented } from "#/components/ui/segmented";
import { Spinner } from "#/components/ui/spinner";
import { Switch } from "#/components/ui/switch";
import {
	fieldError,
	isAgentTooOld,
	isGenerationConflict,
} from "#/features/proxy/lib/errors";
import {
	buildRulePatch,
	codeForFamily,
	REDIRECT_FAMILY_HELP,
	type RuleContext,
	type RuleForm,
	redirectDiscards,
	redirectFamily,
	ruleDirty,
	toRuleForm,
	validateRule,
	wwwCounterpart,
} from "#/features/proxy/lib/rule";
import { AgentTooOldBanner } from "#/features/proxy/ui/agent-too-old";
import {
	SAVES_STAGED,
	stripPrefixExample,
	wwwRedirectHelp,
} from "./domain-copy";
import { useDomainSave } from "./use-domain-patch";

function Field({
	id,
	label,
	hint,
	error,
	children,
}: {
	id: string;
	label: string;
	hint?: string;
	error?: string | undefined;
	children: ReactNode;
}) {
	return (
		<div>
			<label htmlFor={id} className="mb-1 block font-medium text-ink-2 text-xs">
				{label}
			</label>
			{children}
			{hint ? <p className="mt-1 text-ink-3 text-2xs">{hint}</p> : null}
			<FieldError className="mt-1">{error}</FieldError>
		</div>
	);
}

/**
 * Where a domain points: host, path prefix and how it is forwarded, or a
 * redirect to another host. Saved with one PATCH of the changed fields.
 */
export function RuleSection({
	service,
	domain,
	context,
	disabled,
}: {
	service: Service;
	domain: ServiceDomain;
	context: RuleContext;
	/** The agent is too old: nothing here can be staged. */
	disabled: boolean;
}) {
	const [edited, setEdited] = useState<RuleForm | null>(null);
	const baseline = useMemo(() => toRuleForm(domain), [domain]);
	const form = edited ?? baseline;
	const dirty = edited !== null && ruleDirty(domain, edited);
	const errors = useMemo(
		() => validateRule(form, domain, context),
		[form, domain, context],
	);
	const invalid = Object.keys(errors).length > 0;
	const save = useDomainSave(service);
	const set = (patch: Partial<RuleForm>) => setEdited({ ...form, ...patch });

	const redirect = form.kind === "redirect";
	const discards =
		redirect && domain.kind !== "redirect" ? redirectDiscards(domain) : [];
	const counterpart = domain.wwwPartner ?? "";
	const wwwOn = domain.wwwRedirect ?? false;
	// Offered on a whole-host serve domain that is an apex or www.<apex>; a
	// redirect that already exists can always be switched off.
	const offerWww =
		domain.kind === "serve" &&
		domain.pathPrefix === "" &&
		counterpart !== "" &&
		(wwwOn || wwwCounterpart(domain.hostname) !== null);
	const toggleWww = (next: boolean) =>
		save.mutate({ domainId: domain.id, body: { wwwRedirect: next } });
	const stripExample = stripPrefixExample(form.pathPrefix, form.addPrefix);

	const onSave = () => {
		if (invalid) return;
		save.mutate(
			{ domainId: domain.id, body: buildRulePatch(domain, form) },
			{
				onSuccess: () => setEdited(null),
				onError: (error) => {
					if (isGenerationConflict(error)) setEdited(null);
				},
			},
		);
	};

	const problem = save.error;
	const unmapped =
		problem &&
		fieldError(problem, "pathPrefix") === undefined &&
		fieldError(problem, "redirectTo") === undefined
			? problem
			: undefined;

	return (
		<div className="flex flex-col gap-3" data-section="rule">
			{isAgentTooOld(problem) ? (
				<AgentTooOldBanner
					{...(service.server ? { serverName: service.server.name } : {})}
				/>
			) : null}
			<Segmented
				aria-label="What this domain does"
				font="sans"
				value={form.kind}
				onValueChange={(kind) => set({ kind })}
				options={[
					{ value: "serve", label: "Serve this service", disabled },
					{ value: "redirect", label: "Redirect to…", disabled },
				]}
			/>

			<div className="grid gap-3 sm:grid-cols-2">
				<Field
					id={`rule-host-${domain.id}`}
					label={redirect ? "This host" : "Hostname"}
					error={errors.hostname}
				>
					<Input
						id={`rule-host-${domain.id}`}
						autoComplete="off"
						spellCheck={false}
						className="font-mono"
						disabled={disabled}
						aria-invalid={errors.hostname !== undefined}
						value={form.hostname}
						onChange={(event) => set({ hostname: event.target.value })}
					/>
				</Field>
				{redirect ? (
					<Field
						id={`rule-to-${domain.id}`}
						label="Redirect to host"
						hint="A hostname, not a URL."
						error={errors.redirectTo ?? fieldError(problem, "redirectTo")}
					>
						<Input
							id={`rule-to-${domain.id}`}
							autoComplete="off"
							spellCheck={false}
							className="font-mono"
							placeholder="example.com"
							disabled={disabled}
							aria-invalid={errors.redirectTo !== undefined}
							value={form.redirectTo}
							onChange={(event) => set({ redirectTo: event.target.value })}
						/>
					</Field>
				) : null}
				<Field
					id={`rule-path-${domain.id}`}
					label="Path prefix"
					hint={
						redirect
							? "Redirect only this path and below. Empty redirects the whole host."
							: "Serve only this path and below, e.g. /api. Empty serves the whole host."
					}
					error={errors.pathPrefix ?? fieldError(problem, "pathPrefix")}
				>
					<Input
						id={`rule-path-${domain.id}`}
						autoComplete="off"
						spellCheck={false}
						className="font-mono"
						placeholder="/api"
						disabled={disabled}
						aria-invalid={errors.pathPrefix !== undefined}
						value={form.pathPrefix}
						onChange={(event) => set({ pathPrefix: event.target.value })}
					/>
				</Field>
				{redirect ? (
					<div>
						<span className="mb-1 block font-medium text-ink-2 text-xs">
							Kind of redirect
						</span>
						<Segmented
							aria-label="Redirect kind"
							font="sans"
							value={redirectFamily(form.redirectCode)}
							onValueChange={(family) =>
								set({ redirectCode: codeForFamily(family, form.redirectCode) })
							}
							options={[
								{ value: "permanent", label: "Permanent", disabled },
								{ value: "temporary", label: "Temporary", disabled },
							]}
						/>
						<p className="mt-1 text-ink-3 text-2xs leading-[1.5]">
							{REDIRECT_FAMILY_HELP[redirectFamily(form.redirectCode)]}
						</p>
					</div>
				) : (
					<Field
						id={`rule-add-${domain.id}`}
						label="Add prefix"
						hint="Prepended to the path before it reaches your service."
						error={errors.addPrefix}
					>
						<Input
							id={`rule-add-${domain.id}`}
							autoComplete="off"
							spellCheck={false}
							className="font-mono"
							placeholder="/v2"
							disabled={disabled}
							aria-invalid={errors.addPrefix !== undefined}
							value={form.addPrefix}
							onChange={(event) => set({ addPrefix: event.target.value })}
						/>
					</Field>
				)}
			</div>

			{redirect ? (
				<div className="flex items-start gap-3">
					<div className="min-w-0 flex-1">
						<label
							htmlFor={`rule-preserve-${domain.id}`}
							className="font-medium text-[13px] text-ink-1"
						>
							Keep the path and query
						</label>
						<p className="mt-0.5 text-ink-3 text-xs">
							/docs?a=1 goes to the same path on the target. Off sends everyone
							to its root.
						</p>
					</div>
					<Switch
						id={`rule-preserve-${domain.id}`}
						checked={form.preservePath}
						disabled={disabled}
						onCheckedChange={(preservePath) => set({ preservePath })}
					/>
				</div>
			) : (
				<div className="flex items-start gap-3">
					<div className="min-w-0 flex-1">
						<label
							htmlFor={`rule-strip-${domain.id}`}
							className="font-medium text-[13px] text-ink-1"
						>
							Strip the prefix
						</label>
						<p className="mt-0.5 text-ink-3 text-xs">
							{stripExample ? (
								<>
									<span className="font-mono">{stripExample.from}</span> reaches
									your service as{" "}
									<span className="font-mono">{stripExample.to}</span>.
								</>
							) : (
								"Needs a path prefix above. The prefix is removed before the request reaches your service."
							)}
						</p>
						<FieldError className="mt-1">{errors.stripPrefix}</FieldError>
					</div>
					<Switch
						id={`rule-strip-${domain.id}`}
						checked={form.stripPrefix}
						disabled={disabled}
						onCheckedChange={(stripPrefix) => set({ stripPrefix })}
					/>
				</div>
			)}

			{discards.length > 0 ? (
				<Alert variant="warning">
					<AlertDescription>
						Saving this as a redirect clears {discards.join(", ")}.
					</AlertDescription>
				</Alert>
			) : null}
			{domain.wwwRedirectFor ? (
				<p className="border-line border-t pt-3 text-ink-3 text-xs leading-[1.5]">
					This redirect is kept by the www redirect of{" "}
					<span className="font-mono">{domain.wwwRedirectFor}</span>. It follows
					that domain's hostname and certificate settings; turn the option off
					there to remove it.
				</p>
			) : null}
			{offerWww && !dirty ? (
				<div className="flex items-start gap-3 border-line border-t pt-3">
					<div className="min-w-0 flex-1">
						<label
							htmlFor={`rule-www-${domain.id}`}
							className="font-medium text-[13px] text-ink-1"
						>
							Redirect {counterpart} to this domain
						</label>
						<p className="mt-0.5 text-ink-3 text-xs">
							{wwwRedirectHelp({
								on: wwwOn,
								hostname: domain.hostname,
								counterpart,
							})}{" "}
							<span className="text-ink-3">{SAVES_STAGED}.</span>
						</p>
					</div>
					<Switch
						id={`rule-www-${domain.id}`}
						checked={wwwOn}
						disabled={disabled || save.isPending}
						onCheckedChange={toggleWww}
					/>
				</div>
			) : null}
			{unmapped ? <FieldError>{errorMessage(unmapped)}</FieldError> : null}
			{dirty ? (
				<div className="flex items-center justify-end gap-2">
					<Button
						type="button"
						variant="ghost"
						size="sm"
						disabled={save.isPending}
						onClick={() => setEdited(null)}
					>
						Discard
					</Button>
					<Button
						type="button"
						variant="secondary"
						size="sm"
						disabled={invalid || save.isPending || disabled}
						onClick={onSave}
					>
						{save.isPending ? <Spinner /> : null}
						Save domain
					</Button>
				</div>
			) : null}
		</div>
	);
}
