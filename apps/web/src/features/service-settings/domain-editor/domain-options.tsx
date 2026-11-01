import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useMemo, useState } from "react";
import type { Service, ServiceDomain } from "#/api/models";
import {
	type AuthRow,
	authUsersChanged,
	buildAuthBody,
	toAuthRows,
	validateAuthRows,
} from "#/features/proxy/lib/auth-users";
import {
	groupServerErrors,
	isGenerationConflict,
} from "#/features/proxy/lib/errors";
import {
	applyGroup,
	formToOptions,
	GROUP_TITLE,
	type GroupEdits,
	groupDirty,
	groupSummary,
	mergeEdits,
	type OptionGroup,
	type OptionsForm,
	setGroupEdit,
	toForm,
	validateGroup,
} from "#/features/proxy/lib/options";
import type { DomainStaged } from "#/features/proxy/lib/staged";
import { serverCertificatesQuery } from "#/features/proxy/queries";
import {
	AccessBody,
	HeadersBody,
	LimitsBody,
	RedirectsBody,
	TlsBody,
} from "./group-bodies";
import { OptionGroupPanel } from "./option-group-panel";
import { useDomainSave } from "./use-domain-patch";

type TlsMode = ServiceDomain["tlsMode"];

interface TlsEdit {
	tlsMode: TlsMode;
	certificateId: string;
}

/**
 * The five collapsible groups of a serve domain: Access, Headers & CORS,
 * Redirects, Limits, TLS. Each keeps its own unsaved edits and saves only its
 * own option fields over the domain's current options, so two groups never
 * overwrite each other.
 */
export function DomainOptions({
	service,
	domain,
	staged,
	disabled,
}: {
	service: Service;
	domain: ServiceDomain;
	staged: DomainStaged;
	/** The agent is too old for proxy v2: nothing can be staged. */
	disabled: boolean;
}) {
	const save = useDomainSave(service);
	const [edits, setEdits] = useState<GroupEdits>({});
	const [userEdit, setUserEdit] = useState<AuthRow[] | null>(null);
	const [tlsEdit, setTlsEdit] = useState<TlsEdit | null>(null);
	const [failure, setFailure] = useState<{
		group: OptionGroup;
		error: unknown;
	} | null>(null);

	const base = useMemo(() => toForm(domain.options), [domain.options]);
	const form = mergeEdits(base, edits);
	const savedUsers = domain.authUsers;
	const rows = userEdit ?? toAuthRows(savedUsers);
	const tls: TlsEdit = tlsEdit ?? {
		tlsMode: domain.tlsMode,
		certificateId: domain.certificateId ?? "",
	};

	const certs = useQuery({
		...serverCertificatesQuery(service.serverId ?? ""),
		enabled: service.serverId !== null,
	});

	const setIn = (group: OptionGroup) => (patch: Partial<OptionsForm>) =>
		setEdits((current) => setGroupEdit(current, base, group, patch));

	const userProblems = validateAuthRows(rows);
	const usersDirty = authUsersChanged(savedUsers, rows);
	const tlsDirty =
		tls.tlsMode !== domain.tlsMode ||
		tls.certificateId !== (domain.certificateId ?? "");

	const dirtyOf = (group: OptionGroup): boolean => {
		if (group === "access")
			return groupDirty(domain.options, form, group) || usersDirty;
		if (group === "tls")
			return groupDirty(domain.options, form, group) || tlsDirty;
		return groupDirty(domain.options, form, group);
	};

	const errorsOf = (group: OptionGroup) => {
		const own = validateGroup(form, group, { authUsers: rows.length });
		const merged =
			group === "access" ? { ...own, ...userProblems } : { ...own };
		if (group === "tls") {
			if (tls.tlsMode === "custom" && tls.certificateId === "") {
				merged.certificate = "Pick the certificate to serve.";
			}
		}
		const server =
			failure?.group === group ? groupServerErrors(failure.error, group) : null;
		return {
			fields: { ...merged, ...(server?.fields ?? {}) },
			general: server?.general,
			local: merged,
		};
	};

	const reset = (group: OptionGroup) => {
		setEdits((current) => {
			const next = { ...current };
			delete next[group];
			return next;
		});
		if (group === "access") setUserEdit(null);
		if (group === "tls") setTlsEdit(null);
		setFailure((current) => (current?.group === group ? null : current));
	};

	const saveGroup = (group: OptionGroup) => {
		const options = applyGroup(domain.options, formToOptions(form), group);
		const done = {
			onSuccess: () => reset(group),
			onError: (error: unknown) => {
				if (isGenerationConflict(error)) reset(group);
				else setFailure({ group, error });
			},
		};
		setFailure(null);
		if (group === "access") {
			save.mutate(
				{
					domainId: domain.id,
					body: { options },
					...(usersDirty ? { users: buildAuthBody(rows) } : {}),
					// Users must exist before basic auth turns on; when it turns
					// off the flag goes first.
					usersFirst: form.basicAuth,
				},
				done,
			);
			return;
		}
		if (group === "tls") {
			save.mutate(
				{
					domainId: domain.id,
					body: {
						tlsMode: tls.tlsMode,
						certificateId: tls.tlsMode === "custom" ? tls.certificateId : "",
						options,
					},
				},
				done,
			);
			return;
		}
		save.mutate({ domainId: domain.id, body: { options } }, done);
	};

	const panel = (
		group: OptionGroup,
		body: (errors: Record<string, string>) => ReactNode,
		extra: { summary?: string; defaultOpen?: boolean } = {},
	) => {
		const errors = errorsOf(group);
		const saving = save.isPending;
		return (
			<OptionGroupPanel
				key={group}
				title={GROUP_TITLE[group]}
				summary={
					extra.summary ??
					groupSummary(domain.options, group, { authUsers: savedUsers.length })
				}
				staged={staged.added || staged.buckets.has(group)}
				stagedTitle={`Staged: generation ${service.generation}, deployed ${service.appliedGeneration}`}
				dirty={dirtyOf(group)}
				invalid={Object.keys(errors.local).length > 0}
				saving={saving}
				error={errors.general}
				locked={disabled}
				onSave={() => saveGroup(group)}
				onDiscard={() => reset(group)}
				defaultOpen={extra.defaultOpen ?? false}
			>
				{body(errors.fields)}
			</OptionGroupPanel>
		);
	};

	return (
		<div className="flex flex-col gap-2" data-section="options">
			{panel("access", (errors) => (
				<AccessBody
					form={form}
					set={setIn("access")}
					errors={errors}
					rows={rows}
					setRows={setUserEdit}
					userErrors={errors}
				/>
			))}
			{panel("headers", (errors) => (
				<HeadersBody
					form={form}
					set={setIn("headers")}
					errors={errors}
					host={domain.hostname}
				/>
			))}
			{panel("redirects", (errors) => (
				<RedirectsBody form={form} set={setIn("redirects")} errors={errors} />
			))}
			{panel("limits", (errors) => (
				<LimitsBody form={form} set={setIn("limits")} errors={errors} />
			))}
			{panel(
				"tls",
				(errors) => (
					<TlsBody
						form={form}
						set={setIn("tls")}
						hostname={domain.hostname}
						tlsMode={tls.tlsMode}
						certificateId={tls.certificateId}
						onTls={(patch) => setTlsEdit({ ...tls, ...patch })}
						certificates={certs.data?.items ?? []}
						certificateError={errors.certificate}
						certsLoading={certs.isPending && service.serverId !== null}
					/>
				),
				{
					summary: groupSummary(domain.options, "tls", {
						tlsMode: domain.tlsMode,
					}),
				},
			)}
		</div>
	);
}
