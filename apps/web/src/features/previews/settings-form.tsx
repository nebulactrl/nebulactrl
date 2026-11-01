import { cn } from "cn";
import { PlusIcon, XIcon } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { errorMessage } from "#/api/client-config";
import type { Environment, PreviewSettings } from "#/api/gen/types.gen";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { CheckLabel } from "#/components/ui/check-label";
import { FieldError } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "#/components/ui/select";
import { Spinner } from "#/components/ui/spinner";
import { Switch } from "#/components/ui/switch";
import { useServices } from "#/features/canvas/data";
import {
	Section,
	SectionBody,
	SettingRow,
	TextRow,
} from "#/features/service-settings/fields";
import {
	draftFrom,
	hasErrors,
	isDirty,
	newSeed,
	type SeedDraft,
	type SettingsDraft,
	validateDraft,
} from "./model";
import { useSavePreviewSettings } from "./queries";

const RUN_ON = [
	{ value: "create", label: "First deploy" },
	{ value: "push", label: "Every push" },
] as const;

function SeedRow({
	seed,
	services,
	error,
	onChange,
	onRemove,
}: {
	seed: SeedDraft;
	services: readonly { slug: string; name: string }[];
	error: string | undefined;
	onChange(patch: Partial<SeedDraft>): void;
	onRemove(): void;
}) {
	return (
		<div className="flex flex-col gap-1.5 py-1.5">
			<div className="flex flex-wrap items-start gap-2">
				<Select
					value={seed.service}
					items={services.map((s) => ({ value: s.slug, label: s.name }))}
					onValueChange={(next) => onChange({ service: String(next ?? "") })}
				>
					<SelectTrigger
						aria-label="Service that runs the command"
						className="w-[170px]"
						aria-invalid={error !== undefined}
					>
						<SelectValue placeholder="Service" />
					</SelectTrigger>
					<SelectContent alignItemWithTrigger={false}>
						{services.map((s) => (
							<SelectItem key={s.slug} value={s.slug}>
								{s.name}
							</SelectItem>
						))}
					</SelectContent>
				</Select>
				<Input
					aria-label="Command"
					className="min-w-[220px] flex-1 font-mono"
					placeholder="npm run db:migrate && npm run db:seed"
					autoComplete="off"
					spellCheck={false}
					value={seed.command}
					aria-invalid={error !== undefined}
					onChange={(event) => onChange({ command: event.target.value })}
				/>
				<Select
					value={seed.runOn}
					items={RUN_ON.map((r) => ({ value: r.value, label: r.label }))}
					onValueChange={(next) =>
						onChange({ runOn: next === "push" ? "push" : "create" })
					}
				>
					<SelectTrigger aria-label="When to run" className="w-[130px]">
						<SelectValue />
					</SelectTrigger>
					<SelectContent alignItemWithTrigger={false}>
						{RUN_ON.map((r) => (
							<SelectItem key={r.value} value={r.value}>
								{r.label}
							</SelectItem>
						))}
					</SelectContent>
				</Select>
				<Button
					type="button"
					size="icon"
					variant="ghost"
					aria-label="Remove seed command"
					onClick={onRemove}
				>
					<XIcon aria-hidden />
				</Button>
			</div>
			<FieldError>{error}</FieldError>
		</div>
	);
}

/**
 * Project → Previews: whether pull requests get an environment, which
 * environment they are cloned from, the limits, which services stay out of
 * previews and the commands that prepare each preview's fresh database.
 */
export function PreviewSettingsForm({
	projectId,
	settings,
	environments,
}: {
	projectId: string;
	settings: PreviewSettings;
	environments: readonly Environment[];
}) {
	const baseline = useMemo(() => draftFrom(settings), [settings]);
	// `null`: untouched, so the form follows the server as it refetches.
	const [edited, setEdited] = useState<SettingsDraft | null>(null);
	const draft = edited ?? baseline;
	const save = useSavePreviewSettings(projectId);
	const errors = validateDraft(draft);
	const dirty = isDirty(baseline, draft);
	const invalid = hasErrors(errors);

	const bases = environments.filter((e) => e.kind !== "preview");
	const baseEnvironment = bases.find((e) => e.id === draft.baseEnvironmentId);
	const services = useServices(baseEnvironment?.id);
	const serviceList = useMemo(
		() =>
			(services.data ?? [])
				.filter((s) => s.kind !== "route")
				.map((s) => ({ slug: s.slug, name: s.name })),
		[services.data],
	);

	// A saved draft is the server's now.
	useEffect(() => {
		if (save.isSuccess) setEdited(null);
	}, [save.isSuccess]);

	const update = (patch: Partial<SettingsDraft>) => {
		save.reset();
		setEdited({ ...draft, ...patch });
	};
	const toggleExcluded = (slug: string, excluded: boolean) =>
		update({
			excluded: excluded
				? [...draft.excluded, slug]
				: draft.excluded.filter((s) => s !== slug),
			// A service that is out of previews cannot run a seed command.
			seeds: excluded
				? draft.seeds.filter((seed) => seed.service !== slug)
				: draft.seeds,
		});
	const patchSeed = (key: string, patch: Partial<SeedDraft>) =>
		update({
			seeds: draft.seeds.map((seed) =>
				seed.key === key ? { ...seed, ...patch } : seed,
			),
		});

	return (
		<form
			className="flex flex-col gap-8"
			onSubmit={(event) => {
				event.preventDefault();
				if (dirty && !invalid && !save.isPending) save.mutate(draft);
			}}
		>
			<Section
				title="Pull-request previews"
				description="Every open pull request of a connected repository gets its own environment: a copy of the base environment with a fresh, empty database, its own URLs and the pull request's commit. It is deleted when the pull request closes."
			>
				<SettingRow
					label="Previews"
					htmlFor="previews-enabled"
					description="Turn this on after connecting at least one service to a repository."
				>
					<div className="flex items-center gap-3">
						<Switch
							id="previews-enabled"
							checked={draft.enabled}
							onCheckedChange={(enabled) => update({ enabled })}
						/>
						<span className="text-[13px] text-ink-2">
							{draft.enabled ? "On" : "Off"}
						</span>
					</div>
				</SettingRow>
				<SettingRow
					label="Base environment"
					htmlFor="previews-base"
					description="Previews copy its services, variables and volumes (empty). Databases are created fresh."
				>
					<Select
						value={draft.baseEnvironmentId ?? ""}
						items={bases.map((e) => ({ value: e.id, label: e.name }))}
						onValueChange={(next) =>
							update({
								baseEnvironmentId: next ? String(next) : null,
								excluded: [],
								seeds: [],
							})
						}
					>
						<SelectTrigger
							id="previews-base"
							className="w-full max-w-[360px]"
							aria-invalid={errors.base !== undefined}
						>
							<SelectValue placeholder="Choose an environment" />
						</SelectTrigger>
						<SelectContent alignItemWithTrigger={false}>
							{bases.map((e) => (
								<SelectItem key={e.id} value={e.id}>
									{e.name}
								</SelectItem>
							))}
						</SelectContent>
					</Select>
					<FieldError>{errors.base}</FieldError>
				</SettingRow>
			</Section>

			<Section
				title="Limits"
				description="Previews cost memory. Past the limit the least recently pushed preview is paused and starts again with its next push. A preview with no push for the idle period is deleted."
			>
				<TextRow
					id="previews-max"
					label="Running previews"
					description="At most this many run at once."
					type="number"
					narrow
					value={draft.maxConcurrent}
					error={errors.max}
					onChange={(maxConcurrent) => update({ maxConcurrent })}
				/>
				<TextRow
					id="previews-idle"
					label="Delete after idle days"
					description="Counted from the last push."
					type="number"
					narrow
					value={draft.idleDays}
					error={errors.idle}
					onChange={(idleDays) => update({ idleDays })}
				/>
			</Section>

			<Section
				title="Fork pull requests"
				description="A fork's code is not yours. Previews of forks stay off unless you allow them."
			>
				<SettingRow label="Previews for forks" htmlFor="previews-forks">
					<div className="flex items-center gap-3">
						<Switch
							id="previews-forks"
							checked={draft.forkPrs}
							onCheckedChange={(forkPrs) => update({ forkPrs })}
						/>
						<span className="text-[13px] text-ink-2">
							{draft.forkPrs ? "On" : "Off"}
						</span>
					</div>
				</SettingRow>
				{draft.forkPrs ? (
					<SectionBody>
						<Alert variant="info">
							<AlertTitle>Fork previews never receive secrets</AlertTitle>
							<AlertDescription>
								They get no secret variables, no variable that references a
								secret, no registry credentials and no access token for the
								repository: only a public clone of the fork. Database
								credentials are always freshly generated.
							</AlertDescription>
						</Alert>
					</SectionBody>
				) : null}
			</Section>

			<Section
				title="Services in previews"
				description="Everything in the base environment is copied unless you leave it out."
			>
				<SectionBody
					className={cn(
						"flex flex-col gap-1 py-1.5",
						baseEnvironment ? "" : "text-[13px] text-ink-3",
					)}
				>
					{baseEnvironment === undefined ? (
						"Choose a base environment first."
					) : serviceList.length === 0 ? (
						<span className="text-[13px] text-ink-3">
							{baseEnvironment.name} has no services yet.
						</span>
					) : (
						serviceList.map((service) => (
							<CheckLabel
								key={service.slug}
								checked={!draft.excluded.includes(service.slug)}
								onCheckedChange={(include) =>
									toggleExcluded(service.slug, !include)
								}
								labelClassName="text-[13px] text-ink-1"
							>
								{service.name}
								<span className="ml-2 font-mono text-[11px] text-ink-4">
									{service.slug}
								</span>
							</CheckLabel>
						))
					)}
				</SectionBody>
			</Section>

			<Section
				title="Seed commands"
				description="Previews start with an empty database. A seed command runs once as a one-off job in a service of the preview after it deployed, for example a migration or a seed script. A failing command marks the preview failed."
			>
				<SectionBody className="flex flex-col gap-1">
					{draft.seeds.map((seed) => (
						<SeedRow
							key={seed.key}
							seed={seed}
							services={serviceList.filter(
								(s) => !draft.excluded.includes(s.slug),
							)}
							error={errors.seeds?.[seed.key]}
							onChange={(patch) => patchSeed(seed.key, patch)}
							onRemove={() =>
								update({ seeds: draft.seeds.filter((s) => s.key !== seed.key) })
							}
						/>
					))}
					<div>
						<Button
							type="button"
							size="sm"
							variant="secondary"
							disabled={serviceList.length === 0}
							onClick={() => update({ seeds: [...draft.seeds, newSeed()] })}
						>
							<PlusIcon aria-hidden />
							Add seed command
						</Button>
					</div>
				</SectionBody>
			</Section>

			{save.isError ? (
				<Alert variant="destructive">
					<AlertDescription>{errorMessage(save.error)}</AlertDescription>
				</Alert>
			) : null}
			<div className="flex items-center gap-2">
				<span className="ml-auto" />
				<Button
					type="button"
					variant="ghost"
					disabled={!dirty || save.isPending}
					onClick={() => {
						save.reset();
						setEdited(null);
					}}
				>
					Discard changes
				</Button>
				<Button
					type="submit"
					variant="primary"
					disabled={!dirty || invalid || save.isPending}
				>
					{save.isPending ? <Spinner /> : null}
					Save
				</Button>
			</div>
		</form>
	);
}
