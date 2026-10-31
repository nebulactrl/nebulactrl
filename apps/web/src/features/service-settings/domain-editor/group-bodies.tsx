import { LockIcon, PlusIcon, XIcon } from "lucide-react";
import type { ReactNode } from "react";
import type { ServerCertificate } from "#/api/gen/types.gen";
import { Button } from "#/components/ui/button";
import { FieldDescription, FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Segmented } from "#/components/ui/segmented";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Switch } from "#/components/ui/switch";
import { AccessRulesEditor } from "#/features/access-rules/access-rules-editor";
import { checkFromErrors } from "#/features/access-rules/rules";
import type { AuthRow } from "#/features/proxy/lib/auth-users";
import {
	CORS_PRESETS,
	corsPresetOf,
	type FormErrors,
	type OptionsForm,
	type SecurityHeaders,
} from "#/features/proxy/lib/options";
import { certCovers } from "#/features/proxy/lib/validate";
import { HeaderRowsEditor } from "./lists";

export interface BodyProps {
	form: OptionsForm;
	set(patch: Partial<OptionsForm>): void;
	errors: FormErrors;
}

function Row({
	id,
	label,
	description,
	children,
}: {
	id: string;
	label: string;
	description?: ReactNode;
	children: ReactNode;
}) {
	return (
		<div className="flex items-start gap-3">
			<div className="min-w-0 flex-1">
				<label htmlFor={id} className="font-medium text-[13px] text-ink-1">
					{label}
				</label>
				{description ? (
					<p className="mt-0.5 text-ink-3 text-xs leading-[1.5]">
						{description}
					</p>
				) : null}
			</div>
			{children}
		</div>
	);
}

function SwitchRow({
	id,
	label,
	description,
	checked,
	onChange,
}: {
	id: string;
	label: string;
	description?: ReactNode;
	checked: boolean;
	onChange(next: boolean): void;
}) {
	return (
		<Row id={id} label={label} description={description}>
			<Switch id={id} checked={checked} onCheckedChange={onChange} />
		</Row>
	);
}

function Labeled({
	id,
	label,
	error,
	hint,
	children,
	className,
}: {
	id: string;
	label: string;
	error?: string | undefined;
	hint?: string;
	children: ReactNode;
	className?: string;
}) {
	return (
		<div className={className}>
			<label htmlFor={id} className="mb-1 block font-medium text-ink-2 text-xs">
				{label}
			</label>
			{children}
			{hint ? <p className="mt-1 text-ink-3 text-2xs">{hint}</p> : null}
			<FieldError className="mt-1">{error}</FieldError>
		</div>
	);
}

// --- Access -----------------------------------------------------------------

export function AccessBody({
	form,
	set,
	errors,
	rows,
	setRows,
	userErrors,
}: BodyProps & {
	rows: AuthRow[];
	setRows(next: AuthRow[]): void;
	userErrors: Record<string, string>;
}) {
	const setRow = (index: number, patch: Partial<AuthRow>) =>
		setRows(rows.map((row, i) => (i === index ? { ...row, ...patch } : row)));
	return (
		<>
			<div className="flex flex-col gap-3">
				<SwitchRow
					id="access-basic"
					label="Ask for a username and password"
					description="Visitors see the browser's login prompt. Passwords are hashed with bcrypt; only user names are ever shown here."
					checked={form.basicAuth}
					onChange={(basicAuth) => set({ basicAuth })}
				/>
				{errors.basicAuth ? <FieldError>{errors.basicAuth}</FieldError> : null}
				<ul aria-label="Basic-auth users" className="flex flex-col gap-1.5">
					{rows.map((row, index) => {
						const nameError = userErrors[`users.${index}.username`];
						const passError = userErrors[`users.${index}.password`];
						return (
							// biome-ignore lint/suspicious/noArrayIndexKey: rows are edited in place; a new row has no id yet
							<li key={index} className="flex flex-col gap-1">
								<div className="flex items-center gap-1.5">
									<Input
										aria-label={`User name ${index + 1}`}
										autoComplete="off"
										spellCheck={false}
										className="max-w-[180px] font-mono"
										placeholder="username"
										readOnly={row.existing}
										aria-invalid={nameError !== undefined}
										value={row.username}
										onChange={(event) =>
											setRow(index, { username: event.target.value })
										}
									/>
									<Input
										type="password"
										aria-label={`Password for user ${index + 1}`}
										autoComplete="new-password"
										className="font-mono"
										placeholder={
											row.existing
												? "Password set. Type to change it"
												: "Password (write-only)"
										}
										aria-invalid={passError !== undefined}
										value={row.password}
										onChange={(event) =>
											setRow(index, { password: event.target.value })
										}
									/>
									<Button
										type="button"
										variant="ghost"
										size="icon-sm"
										aria-label={`Remove user ${row.username || index + 1}`}
										onClick={() => setRows(rows.filter((_, i) => i !== index))}
									>
										<XIcon aria-hidden />
									</Button>
								</div>
								{nameError || passError ? (
									<FieldError>{nameError ?? passError}</FieldError>
								) : null}
							</li>
						);
					})}
				</ul>
				{userErrors.users ? <FieldError>{userErrors.users}</FieldError> : null}
				<div>
					<Button
						type="button"
						variant="secondary"
						size="xs"
						onClick={() =>
							setRows([
								...rows,
								{ username: "", password: "", existing: false },
							])
						}
					>
						<PlusIcon aria-hidden />
						Add user
					</Button>
				</div>
			</div>

			<div className="flex flex-col gap-3 border-line border-t pt-4">
				<SwitchRow
					id="access-allowlist"
					label="Restrict by IP address"
					description="Allow or deny networks. Denied networks are refused first; with an allow list, everyone else is refused too."
					checked={form.accessOn}
					onChange={(accessOn) => set({ accessOn })}
				/>
				{form.accessOn ? (
					<>
						<AccessRulesEditor
							allow={form.allowCidrs}
							deny={form.denyCidrs}
							onAllowChange={(allowCidrs) => set({ allowCidrs })}
							onDenyChange={(denyCidrs) => set({ denyCidrs })}
							check={checkFromErrors(errors)}
						/>
						<details className="group/cdn text-xs">
							<summary className="cursor-pointer select-none text-ink-2 outline-none hover:text-ink-1 focus-visible:focus-ring">
								Behind a CDN or load balancer?
							</summary>
							<div className="mt-2 flex flex-col gap-2 pl-1">
								<p className="text-ink-3 leading-[1.5]">
									Traffic then arrives from the CDN, not from the visitor. Set
									how many trusted proxies sit in front of NebulaCtrl
									(Cloudflare: 1) so the visitor's address from X-Forwarded-For
									is the one matched. 0 uses the connection's address.
								</p>
								<Labeled
									id="access-depth"
									label="Trusted proxy depth"
									error={errors.accessDepth}
									className="max-w-[140px]"
								>
									<Input
										id="access-depth"
										inputMode="numeric"
										className="font-mono"
										aria-invalid={errors.accessDepth !== undefined}
										value={form.accessDepth}
										onChange={(event) =>
											set({ accessDepth: event.target.value })
										}
									/>
								</Labeled>
							</div>
						</details>
						<Labeled
							id="access-reject"
							label="Refusal status"
							error={errors.rejectStatus}
							hint="Empty uses 403. 404 hides that the domain exists."
							className="max-w-[140px]"
						>
							<Input
								id="access-reject"
								inputMode="numeric"
								className="font-mono"
								placeholder="403"
								aria-invalid={errors.rejectStatus !== undefined}
								value={form.rejectStatus}
								onChange={(event) => set({ rejectStatus: event.target.value })}
							/>
						</Labeled>
					</>
				) : null}
			</div>
		</>
	);
}

// --- Headers & CORS ---------------------------------------------------------

const SECURITY_OPTIONS = [
	{ value: "none", label: "None" },
	{ value: "basic", label: "Basic" },
	{ value: "strict", label: "Strict (HSTS)" },
] as const;

const SECURITY_HELP: Record<string, string> = {
	none: "The proxy adds no security headers.",
	basic:
		"X-Content-Type-Options: nosniff, frame denial and a strict referrer policy.",
	strict:
		"Basic plus Strict-Transport-Security for a year, including subdomains. Browsers then refuse plain HTTP for this domain; turn it on only when HTTPS works everywhere.",
};

export function HeadersBody({
	form,
	set,
	errors,
	host,
}: BodyProps & { host: string }) {
	const preset = corsPresetOf(form, host);
	return (
		<>
			<div className="flex flex-col gap-2">
				<span className="font-medium text-[13px] text-ink-1">
					Security headers
				</span>
				<Segmented
					aria-label="Security headers"
					font="sans"
					value={form.securityHeaders === "" ? "none" : form.securityHeaders}
					onValueChange={(next) =>
						set({
							securityHeaders: next === "none" ? "" : (next as SecurityHeaders),
						})
					}
					options={SECURITY_OPTIONS}
				/>
				<p className="text-ink-3 text-xs leading-[1.5]">
					{
						SECURITY_HELP[
							form.securityHeaders === "" ? "none" : form.securityHeaders
						]
					}
				</p>
			</div>
			<SwitchRow
				id="headers-noindex"
				label="Ask search engines not to index"
				description="Adds X-Robots-Tag: noindex, nofollow. For staging and private tools."
				checked={form.noindex}
				onChange={(noindex) => set({ noindex })}
			/>
			<div className="flex flex-col gap-2 border-line border-t pt-4">
				<span className="font-medium text-[13px] text-ink-1">
					Request headers
				</span>
				<p className="text-ink-3 text-xs">
					Sent to your service on every request.
				</p>
				<HeaderRowsEditor
					label="Request header"
					rows={form.requestHeaders}
					errors={errors}
					errorKey="requestHeaders"
					onChange={(requestHeaders) => set({ requestHeaders })}
				/>
			</div>
			<div className="flex flex-col gap-2">
				<span className="font-medium text-[13px] text-ink-1">
					Response headers
				</span>
				<p className="text-ink-3 text-xs">Added to every answer.</p>
				<HeaderRowsEditor
					label="Response header"
					rows={form.responseHeaders}
					errors={errors}
					errorKey="responseHeaders"
					onChange={(responseHeaders) => set({ responseHeaders })}
				/>
			</div>
			<div className="flex flex-col gap-2 border-line border-t pt-4">
				<span className="font-medium text-[13px] text-ink-1">CORS</span>
				<Segmented
					aria-label="CORS preset"
					font="sans"
					value={preset}
					onValueChange={(next) => {
						if (next === "off") set({ corsOn: false });
						else if (next === "custom") set({ corsOn: true });
						else {
							const chosen = CORS_PRESETS.find((p) => p.id === next);
							if (chosen) set(chosen.apply(host));
						}
					}}
					options={[
						{ value: "off", label: "Off" },
						...CORS_PRESETS.map((p) => ({ value: p.id, label: p.label })),
						{ value: "custom", label: "Custom" },
					]}
				/>
				{preset !== "off" && preset !== "custom" ? (
					<p className="text-ink-3 text-xs">
						{CORS_PRESETS.find((p) => p.id === preset)?.description}
					</p>
				) : null}
				{form.corsOn ? (
					<div className="grid gap-3 sm:grid-cols-2">
						<Labeled
							id="cors-origins"
							label="Allowed origins"
							error={errors.corsOrigins}
							hint="Comma separated, or *."
							className="sm:col-span-2"
						>
							<Input
								id="cors-origins"
								autoComplete="off"
								spellCheck={false}
								className="font-mono"
								aria-invalid={errors.corsOrigins !== undefined}
								value={form.corsOrigins}
								onChange={(event) => set({ corsOrigins: event.target.value })}
							/>
						</Labeled>
						<Labeled
							id="cors-methods"
							label="Methods"
							error={errors.corsMethods}
						>
							<Input
								id="cors-methods"
								autoComplete="off"
								spellCheck={false}
								className="font-mono"
								aria-invalid={errors.corsMethods !== undefined}
								value={form.corsMethods}
								onChange={(event) => set({ corsMethods: event.target.value })}
							/>
						</Labeled>
						<Labeled
							id="cors-headers"
							label="Allowed headers"
							error={errors.corsHeaders}
						>
							<Input
								id="cors-headers"
								autoComplete="off"
								spellCheck={false}
								className="font-mono"
								aria-invalid={errors.corsHeaders !== undefined}
								value={form.corsHeaders}
								onChange={(event) => set({ corsHeaders: event.target.value })}
							/>
						</Labeled>
						<Labeled
							id="cors-expose"
							label="Exposed headers"
							error={errors.corsExpose}
						>
							<Input
								id="cors-expose"
								autoComplete="off"
								spellCheck={false}
								className="font-mono"
								aria-invalid={errors.corsExpose !== undefined}
								value={form.corsExpose}
								onChange={(event) => set({ corsExpose: event.target.value })}
							/>
						</Labeled>
						<Labeled
							id="cors-max-age"
							label="Cache preflight (seconds)"
							error={errors.corsMaxAge}
						>
							<Input
								id="cors-max-age"
								inputMode="numeric"
								className="font-mono"
								aria-invalid={errors.corsMaxAge !== undefined}
								value={form.corsMaxAge}
								onChange={(event) => set({ corsMaxAge: event.target.value })}
							/>
						</Labeled>
						<div className="sm:col-span-2">
							<SwitchRow
								id="cors-credentials"
								label="Allow cookies and credentials"
								description="Not allowed together with the * origin."
								checked={form.corsCredentials}
								onChange={(corsCredentials) => set({ corsCredentials })}
							/>
							<FieldError className="mt-1">{errors.corsCredentials}</FieldError>
						</div>
					</div>
				) : null}
			</div>
		</>
	);
}

// --- Redirects --------------------------------------------------------------

export function RedirectsBody({ form, set, errors }: BodyProps) {
	const setRule = (
		index: number,
		patch: Partial<OptionsForm["redirects"][number]>,
	) =>
		set({
			redirects: form.redirects.map((rule, i) =>
				i === index ? { ...rule, ...patch } : rule,
			),
		});
	return (
		<>
			<p className="text-ink-3 text-xs leading-[1.5]">
				Rewrite matching request URLs before they reach your service. The regex
				uses Go (RE2) syntax: no lookahead or backreferences. Refer to groups as{" "}
				{/* biome-ignore lint/suspicious/noTemplateCurlyInString: shows the literal ${1} group reference of the Go regexp replacement */}
				<code className="type-mono text-ink-2">{"${1}"}</code> in the
				replacement. Rules run in order; the first match wins.
			</p>
			<ol aria-label="Redirect rules" className="flex flex-col gap-3">
				{form.redirects.map((rule, index) => {
					const regexError = errors[`redirects.${index}.regex`];
					const replacementError = errors[`redirects.${index}.replacement`];
					return (
						<li
							// biome-ignore lint/suspicious/noArrayIndexKey: rules are ordered by position and edited in place
							key={index}
							className="surface-raised flex flex-col gap-2 rounded-ctl p-2.5"
						>
							<div className="flex items-start gap-2">
								<span className="mt-2 w-4 shrink-0 text-right font-mono text-2xs text-ink-4 tabular-nums">
									{index + 1}
								</span>
								<div className="grid min-w-0 flex-1 gap-2 sm:grid-cols-2">
									<Labeled
										id={`redirect-regex-${index}`}
										label="Regex"
										error={regexError}
									>
										<Input
											id={`redirect-regex-${index}`}
											autoComplete="off"
											spellCheck={false}
											className="font-mono"
											placeholder="^https?://[^/]+/old/(.*)"
											aria-invalid={regexError !== undefined}
											value={rule.regex}
											onChange={(event) =>
												setRule(index, { regex: event.target.value })
											}
										/>
									</Labeled>
									<Labeled
										id={`redirect-replacement-${index}`}
										label="Replacement"
										error={replacementError}
									>
										<Input
											id={`redirect-replacement-${index}`}
											autoComplete="off"
											spellCheck={false}
											className="font-mono"
											placeholder="https://example.com/new/${1}"
											aria-invalid={replacementError !== undefined}
											value={rule.replacement}
											onChange={(event) =>
												setRule(index, { replacement: event.target.value })
											}
										/>
									</Labeled>
								</div>
								<Button
									type="button"
									variant="ghost"
									size="icon-sm"
									aria-label={`Remove redirect rule ${index + 1}`}
									onClick={() =>
										set({
											redirects: form.redirects.filter((_, i) => i !== index),
										})
									}
								>
									<XIcon aria-hidden />
								</Button>
							</div>
							<div className="pl-6">
								<SwitchRow
									id={`redirect-permanent-${index}`}
									label="Permanent"
									description="Browsers remember a permanent redirect."
									checked={rule.permanent}
									onChange={(permanent) => setRule(index, { permanent })}
								/>
							</div>
						</li>
					);
				})}
			</ol>
			{errors.redirects ? <FieldError>{errors.redirects}</FieldError> : null}
			<div>
				<Button
					type="button"
					variant="secondary"
					size="xs"
					onClick={() =>
						set({
							redirects: [
								...form.redirects,
								{ regex: "", replacement: "", permanent: false },
							],
						})
					}
				>
					<PlusIcon aria-hidden />
					Add rule
				</Button>
			</div>
		</>
	);
}

// --- Limits -----------------------------------------------------------------

export function LimitsBody({ form, set, errors }: BodyProps) {
	return (
		<>
			<SwitchRow
				id="limits-rate"
				label="Rate limit"
				description="Per visitor address. Requests over the limit get a 429."
				checked={form.rateOn}
				onChange={(rateOn) => set({ rateOn })}
			/>
			{form.rateOn ? (
				<div className="grid gap-3 sm:grid-cols-4">
					<Labeled
						id="rate-average"
						label="Requests"
						error={errors.rateAverage}
					>
						<Input
							id="rate-average"
							inputMode="numeric"
							className="font-mono"
							aria-invalid={errors.rateAverage !== undefined}
							value={form.rateAverage}
							onChange={(event) => set({ rateAverage: event.target.value })}
						/>
					</Labeled>
					<Labeled
						id="rate-period"
						label="Per (seconds)"
						error={errors.ratePeriod}
					>
						<Input
							id="rate-period"
							inputMode="numeric"
							className="font-mono"
							aria-invalid={errors.ratePeriod !== undefined}
							value={form.ratePeriod}
							onChange={(event) => set({ ratePeriod: event.target.value })}
						/>
					</Labeled>
					<Labeled id="rate-burst" label="Burst" error={errors.rateBurst}>
						<Input
							id="rate-burst"
							inputMode="numeric"
							className="font-mono"
							aria-invalid={errors.rateBurst !== undefined}
							value={form.rateBurst}
							onChange={(event) => set({ rateBurst: event.target.value })}
						/>
					</Labeled>
					<Labeled
						id="rate-depth"
						label="Trusted proxies"
						error={errors.rateDepth}
						hint="0 behind no CDN."
					>
						<Input
							id="rate-depth"
							inputMode="numeric"
							className="font-mono"
							aria-invalid={errors.rateDepth !== undefined}
							value={form.rateDepth}
							onChange={(event) => set({ rateDepth: event.target.value })}
						/>
					</Labeled>
				</div>
			) : null}
			<SwitchRow
				id="limits-compress"
				label="Compress responses"
				description="gzip and brotli for text content. Skip it when your service already compresses."
				checked={form.compress}
				onChange={(compress) => set({ compress })}
			/>
		</>
	);
}

// --- TLS --------------------------------------------------------------------

export const TLS_OPTIONS = [
	{ value: "auto", label: "Automatic" },
	{ value: "custom", label: "Custom certificate" },
	{ value: "none", label: "Off (HTTP only)" },
] as const;

const TLS_HELP: Record<string, string> = {
	auto: "A Let's Encrypt certificate when the server has an ACME email and the name is public; otherwise the proxy's default certificate.",
	custom:
		"Serve an uploaded certificate for this host. Upload certificates on the server's Proxy tab.",
	none: "No HTTPS for this domain: it answers plain HTTP only. Credentials and cookies travel unencrypted.",
};

export function TlsBody({
	form,
	set,
	hostname,
	tlsMode,
	certificateId,
	onTls,
	certificates,
	certificateError,
	certsLoading,
}: Pick<BodyProps, "form" | "set"> & {
	hostname: string;
	tlsMode: "auto" | "custom" | "none";
	certificateId: string;
	onTls(patch: {
		tlsMode?: "auto" | "custom" | "none";
		certificateId?: string;
	}): void;
	certificates: readonly ServerCertificate[];
	certificateError?: string | undefined;
	certsLoading: boolean;
}) {
	const usable = certificates.filter((c) => c.change !== "remove");
	const items = usable.map((c) => ({ value: c.id, label: c.name }));
	return (
		<>
			<div className="flex flex-col gap-2">
				<span className="font-medium text-[13px] text-ink-1">Certificate</span>
				<Segmented
					aria-label="TLS mode"
					font="sans"
					value={tlsMode}
					onValueChange={(next) =>
						onTls({
							tlsMode: next,
							...(next !== "custom" ? { certificateId: "" } : {}),
						})
					}
					options={TLS_OPTIONS}
				/>
				<p
					className={`text-xs leading-[1.5] ${tlsMode === "none" ? "text-warning-text" : "text-ink-3"}`}
				>
					{TLS_HELP[tlsMode]}
				</p>
			</div>
			{tlsMode === "custom" ? (
				<div className="flex flex-col gap-1.5">
					<label
						htmlFor="tls-certificate"
						className="font-medium text-[13px] text-ink-1"
					>
						Pinned certificate
					</label>
					{usable.length === 0 && !certsLoading ? (
						<p className="text-ink-3 text-xs">
							No certificate is uploaded on this server yet. Upload one on the
							server's Proxy tab, then pick it here.
						</p>
					) : (
						<Select
							value={certificateId}
							items={items}
							onValueChange={(next) =>
								onTls({ certificateId: String(next ?? "") })
							}
						>
							<SelectTrigger
								id="tls-certificate"
								className="w-full max-w-[360px]"
								aria-invalid={certificateError !== undefined}
							>
								<SelectValue placeholder="Pick a certificate" />
							</SelectTrigger>
							<SelectContent alignItemWithTrigger={false}>
								{usable.map((cert) => {
									const covers = certCovers(cert.domains ?? [], hostname);
									return (
										<SelectItem
											key={cert.id}
											value={cert.id}
											disabled={!covers}
										>
											<span className="flex flex-col">
												<span>{cert.name}</span>
												<span className="text-2xs text-ink-3">
													{covers
														? (cert.domains ?? []).join(", ")
														: `does not cover ${hostname}`}
												</span>
											</span>
										</SelectItem>
									);
								})}
							</SelectContent>
						</Select>
					)}
					<FieldDescription>
						Optional: the proxy also picks a matching uploaded certificate by
						name on its own.
					</FieldDescription>
					<FieldError>{certificateError}</FieldError>
				</div>
			) : null}
			<SwitchRow
				id="tls-force"
				label="Redirect HTTP to HTTPS"
				description={
					tlsMode === "none"
						? "Not applicable without TLS."
						: "Turn it off to serve the same domain over plain HTTP as well."
				}
				checked={tlsMode === "none" ? false : form.forceHttps}
				onChange={(forceHttps) => set({ forceHttps })}
			/>
			<p className="flex items-start gap-2 text-ink-3 text-xs leading-[1.5]">
				<LockIcon aria-hidden className="mt-0.5 size-3.5 shrink-0" />
				After a deploy NebulaCtrl checks that the route answers. Verification
				checks routing, not access rules: a login, an allow-list or a rate limit
				does not make it fail.
			</p>
		</>
	);
}
