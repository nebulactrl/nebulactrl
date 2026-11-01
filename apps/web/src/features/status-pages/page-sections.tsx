import { CheckIcon, CircleIcon, ExternalLinkIcon } from "lucide-react";
import type { StatusPageView } from "#/api/gen/types.gen";
import { Button } from "#/components/ui/button";
import { FieldError } from "#/components/ui/field";
import { Segmented } from "#/components/ui/segmented";
import { Switch } from "#/components/ui/switch";
import { Textarea } from "#/components/ui/textarea";
import {
	Section,
	SectionBody,
	SettingRow,
	TextRow,
} from "#/features/service-settings/fields";
import { DnsPanel } from "./cloudflare-dns";
import {
	MAX_DESCRIPTION,
	MAX_FOOTER,
	MAX_SLUG,
	normalizeHostname,
	type PageForm,
	type PageFormErrors,
	publicUrl,
	THEMES,
	type Theme,
} from "./model";

export type FormChange = (change: Partial<PageForm>) => void;

/** What the page says about itself: heading, address segment, text, theme. */
export function GeneralSection({
	form,
	errors,
	onChange,
	readOnly = false,
}: {
	form: PageForm;
	errors: PageFormErrors;
	onChange: FormChange;
	readOnly?: boolean;
}) {
	return (
		<Section
			title="General"
			description="What visitors read at the top and the bottom of the page."
		>
			<TextRow
				id="page-title"
				label="Title"
				description="The heading of the page."
				value={form.title}
				error={errors.title}
				placeholder="Acme status"
				onChange={(title) => onChange({ title })}
			/>
			<TextRow
				id="page-slug"
				label="Short name"
				description={`Names the page inside your team, for the API and scripts: acme-status. Lowercase letters, digits and hyphens, up to ${MAX_SLUG}. Visitors never see it.`}
				value={form.slug}
				error={errors.slug}
				mono
				onChange={(slug) => onChange({ slug })}
			/>
			<SettingRow
				label="Description"
				htmlFor="page-description"
				description="One or two sentences under the title. Optional."
			>
				<Textarea
					id="page-description"
					rows={2}
					maxLength={MAX_DESCRIPTION + 100}
					aria-invalid={errors.description !== undefined}
					value={form.description}
					onChange={(event) => onChange({ description: event.target.value })}
				/>
				<FieldError>{errors.description}</FieldError>
			</SettingRow>
			<SettingRow
				label="Footer"
				htmlFor="page-footer"
				description="Plain text under the page: a contact address, a support hours note. Line breaks stay. Optional."
			>
				<Textarea
					id="page-footer"
					rows={2}
					maxLength={MAX_FOOTER + 100}
					aria-invalid={errors.footer !== undefined}
					value={form.footer}
					onChange={(event) => onChange({ footer: event.target.value })}
				/>
				<FieldError>{errors.footer}</FieldError>
			</SettingRow>
			<SettingRow
				label="Theme"
				description="Auto follows each visitor's own light or dark setting."
			>
				<div>
					<Segmented<Theme>
						aria-label="Theme of the public page"
						font="sans"
						density="regular"
						value={form.theme}
						onValueChange={(theme) => onChange({ theme })}
						options={THEMES.map((theme) => ({ ...theme, disabled: readOnly }))}
					/>
				</div>
			</SettingRow>
			<SettingRow
				label="Show uptime"
				htmlFor="page-show-uptime"
				description="Availability percentages next to each component's 90 day bar."
			>
				<div className="flex items-center gap-2.5">
					<Switch
						id="page-show-uptime"
						checked={form.showUptime}
						disabled={readOnly}
						onCheckedChange={(showUptime) => onChange({ showUptime })}
					/>
					<span className="text-ink-2 text-xs">
						{form.showUptime ? "Percentages shown" : "Bars only"}
					</span>
				</div>
			</SettingRow>
		</Section>
	);
}

/** The public address, and the DNS records it needs. */
export function HostnameSection({
	page,
	form,
	error,
	onChange,
}: {
	page: StatusPageView;
	form: PageForm;
	error: string | undefined;
	onChange: FormChange;
}) {
	const saved = page.hostname ?? "";
	const edited = normalizeHostname(form.hostname) !== normalizeHostname(saved);
	return (
		<Section
			title="Hostname"
			description="Only the page is public; your dashboard stays private. This name serves the status page and nothing else."
		>
			<TextRow
				id="page-hostname"
				label="Public hostname"
				description="A name on a domain you own, like status.example.com. It cannot be the dashboard's own address or a name a service already uses."
				value={form.hostname}
				error={error}
				mono
				placeholder="status.example.com"
				autoComplete="off"
				onChange={(hostname) => onChange({ hostname })}
			/>
			<SectionBody className="py-2">
				{saved === "" ? (
					<p className="surface-well m-0 rounded-card p-3 text-ink-3 text-xs leading-relaxed">
						{edited
							? "Save the hostname to see the DNS records it needs."
							: "Add a hostname and save to see the DNS records it needs."}
					</p>
				) : edited ? (
					<p className="surface-well m-0 rounded-card p-3 text-ink-3 text-xs leading-relaxed">
						Save the new hostname to check its DNS. The check below applies to{" "}
						<span className="font-mono text-ink-2">{saved}</span> until then.
					</p>
				) : (
					<DnsPanel pageId={page.id} hostname={saved} />
				)}
			</SectionBody>
		</Section>
	);
}

function Requirement({ met, children }: { met: boolean; children: string }) {
	return (
		<li className="flex items-center gap-2 text-xs">
			{met ? (
				<CheckIcon aria-hidden className="size-3.5 shrink-0 text-status-live" />
			) : (
				<CircleIcon aria-hidden className="size-3.5 shrink-0 text-ink-4" />
			)}
			<span className={met ? "text-ink-2" : "text-ink-1"}>
				{children}
				<span className="sr-only">{met ? ", done" : ", still needed"}</span>
			</span>
		</li>
	);
}

/**
 * Serving the page. The two things it needs are listed with what is met; the
 * control plane has the last word, and its refusal shows right under the
 * switch. It publishes what is saved, so unsaved edits come first.
 */
export function PublishSection({
	page,
	dirty,
	pending,
	problem,
	onChange,
	readOnly = false,
}: {
	page: StatusPageView;
	/** There are unsaved edits to the form or the components. */
	dirty: boolean;
	pending: boolean;
	problem: string | undefined;
	onChange: (published: boolean) => void;
	readOnly?: boolean;
}) {
	const hasHostname = (page.hostname ?? "") !== "";
	const hasComponents = (page.components ?? []).length > 0;
	const open = page.published && page.hostname;
	return (
		<Section
			title="Publish"
			description="Publishing puts the page on its hostname for anyone to read, with no sign-in. Unpublishing takes it down within moments."
		>
			<SettingRow
				label="Published"
				htmlFor="page-published"
				description={
					dirty && !page.published
						? "Save or discard your changes first: publishing uses what is saved."
						: undefined
				}
			>
				<div className="flex flex-wrap items-center gap-3">
					<Switch
						id="page-published"
						checked={page.published}
						disabled={readOnly || pending || (dirty && !page.published)}
						onCheckedChange={onChange}
					/>
					<span className="text-ink-2 text-xs">
						{pending
							? page.published
								? "Taking the page down…"
								: "Publishing…"
							: page.published
								? "Live on its hostname"
								: "Draft, not visible to anyone"}
					</span>
					{open ? (
						<Button
							variant="secondary"
							size="sm"
							nativeButton={false}
							render={
								<a
									href={publicUrl(page.hostname ?? "")}
									target="_blank"
									rel="noopener noreferrer"
								/>
							}
						>
							<ExternalLinkIcon />
							Open page
							<span className="sr-only"> (opens in a new tab)</span>
						</Button>
					) : null}
				</div>
				<FieldError>{problem}</FieldError>
				<ul
					aria-label="What publishing needs"
					className="m-0 mt-1 flex list-none flex-col gap-1.5 p-0"
				>
					<Requirement met={hasHostname}>A hostname</Requirement>
					<Requirement met={hasComponents}>At least one component</Requirement>
				</ul>
			</SettingRow>
		</Section>
	);
}
