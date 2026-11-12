import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { cn } from "cn";
import {
	ArrowDownIcon,
	ArrowUpIcon,
	BanIcon,
	CopyIcon,
	EllipsisIcon,
	LockIcon,
	PlusIcon,
	ShieldCheckIcon,
	Trash2Icon,
} from "lucide-react";
import { useId, useMemo, useState } from "react";
import { errorMessage } from "#/api/client-config";
import {
	getServiceFirewallOptions,
	getServiceFirewallQueryKey,
} from "#/api/gen/@tanstack/react-query.gen";
import { setServiceFirewall } from "#/api/gen/sdk.gen";
import type { Service } from "#/api/models";
import { SaveBarStatus } from "#/components/common/save-bar-status";
import { FactLine } from "#/components/settings/fact-line";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { DisclosureChevron, DisclosurePanel } from "#/components/ui/disclosure";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "#/components/ui/dropdown-menu";
import { Skeleton } from "#/components/ui/skeleton";
import { Spinner } from "#/components/ui/spinner";
import { Switch } from "#/components/ui/switch";
import { toast } from "#/components/ui/toast";
import { Section, SectionBody } from "#/features/service-settings/fields";
import {
	blankRule,
	type FirewallRule,
	type FirewallView,
	listPhrase,
	MAX_RULES,
	moveRule,
	newRuleId,
	RECIPES,
	type Recipe,
	type RuleErrors,
	ruleSentence,
	rulesEqual,
	rulesOf,
	type StandardRule,
	summary,
	toBody,
	usesCountry,
	validateRules,
} from "./model";
import { CountryOptions, RuleEditor } from "./rule-editor";
import { TryRequest } from "./try-request";

/**
 * Settings → Firewall. Ordered block and allow rules the proxy applies to every
 * request for the service's domains. Like maintenance it is operational: Apply
 * changes the proxy at once, nothing is staged, and deploys keep it.
 */
export function FirewallSection({ service }: { service: Service }) {
	const queryClient = useQueryClient();
	const options = getServiceFirewallOptions({
		path: { serviceId: service.id },
	});
	const query = useQuery(options);
	const live = useMemo(() => rulesOf(query.data), [query.data]);
	// `null` means untouched: the editor follows the server as it refetches.
	const [edited, setEdited] = useState<FirewallRule[] | null>(null);
	const rules = edited ?? live;
	const [open, setOpen] = useState<string | null>(null);
	const errors = useMemo(() => validateRules(rules), [rules]);
	const invalid = Object.keys(errors).length > 0;
	const dirty = edited !== null && !rulesEqual(edited, live);

	const apply = useMutation({
		mutationFn: async (next: FirewallRule[]) => {
			const { data } = await setServiceFirewall({
				path: { serviceId: service.id },
				body: toBody(next),
				headers: { "Idempotency-Key": crypto.randomUUID() },
			});
			return data;
		},
		onSuccess: (view) => {
			queryClient.setQueryData(
				getServiceFirewallQueryKey({ path: { serviceId: service.id } }),
				view,
			);
			setEdited(null);
			const active = (view.rules ?? []).filter((r) => !r.disabled).length;
			toast.success(
				active === 0 ? "Firewall rules removed" : "Firewall applied",
				active === 0
					? `Only the standard protection applies to ${service.name} now.`
					: view.deployed
						? `The proxy enforces ${active === 1 ? "1 rule" : `${active} rules`} on ${listPhrase(view.hosts ?? [], "and")} now.`
						: `The ${active === 1 ? "rule applies" : "rules apply"} with the first deploy of ${service.name}.`,
			);
		},
	});

	const change = (next: FirewallRule[]) => {
		apply.reset();
		setEdited(next);
	};
	const update = (index: number, rule: FirewallRule) =>
		change(rules.map((r, i) => (i === index ? rule : r)));
	const add = (rule: FirewallRule) => {
		change([...rules, rule]);
		setOpen(rule.id);
	};

	if (query.isPending) {
		return (
			<Section title="Firewall">
				<SectionBody className="flex flex-col gap-2">
					<Skeleton className="h-5 w-2/3 rounded-ctl" />
					<Skeleton className="h-14 rounded-ctl" />
				</SectionBody>
			</Section>
		);
	}
	if (query.isError) {
		return (
			<Section title="Firewall">
				<SectionBody>
					<Alert variant="destructive">
						<AlertTitle>The firewall could not be read</AlertTitle>
						<AlertDescription>{errorMessage(query.error)}</AlertDescription>
					</Alert>
				</SectionBody>
			</Section>
		);
	}
	const view = query.data;

	return (
		<>
			<CountryOptions />
			<Section
				title="Rules"
				description="Checked top to bottom for every request. The first rule that matches decides; a request no rule matches reaches the service."
				aside={summary(rules)}
			>
				<SectionBody className="flex flex-col gap-3">
					<Coverage view={view} rules={rules} />
					<Problems view={view} rules={rules} />
				</SectionBody>
				{rules.length === 0 ? (
					<SectionBody>
						<Recipes
							geoAvailable={view.geoAvailable}
							onPick={(recipe) => add(recipe.build())}
							onBlank={() => add(blankRule())}
						/>
					</SectionBody>
				) : (
					<SectionBody>
						<ol
							aria-label="Firewall rules"
							className="rule-rows m-0 list-none p-0"
						>
							{rules.map((rule, index) => (
								<RuleRow
									key={rule.id}
									rule={rule}
									index={index}
									count={rules.length}
									errors={errors}
									open={open === rule.id}
									onToggle={() => setOpen(open === rule.id ? null : rule.id)}
									onChange={(next) => update(index, next)}
									onMove={(to) => change(moveRule(rules, index, to))}
									onDuplicate={() => {
										const copy = {
											...structuredClone(rule),
											id: newRuleId(),
											name: `${rule.name} (copy)`,
										};
										change([
											...rules.slice(0, index + 1),
											copy,
											...rules.slice(index + 1),
										]);
										setOpen(copy.id);
									}}
									onDelete={() => change(rules.filter((r) => r.id !== rule.id))}
								/>
							))}
						</ol>
						<div className="flex flex-wrap items-center gap-2 pt-2">
							<Button
								type="button"
								variant="secondary"
								size="sm"
								disabled={rules.length >= MAX_RULES}
								onClick={() => add(blankRule())}
							>
								<PlusIcon aria-hidden />
								Add rule
							</Button>
							<DropdownMenu>
								<DropdownMenuTrigger
									render={
										<Button
											type="button"
											variant="ghost"
											size="sm"
											disabled={rules.length >= MAX_RULES}
										>
											Add from a recipe
										</Button>
									}
								/>
								<DropdownMenuContent className="w-[19rem]">
									{RECIPES.map((recipe) => (
										<DropdownMenuItem
											key={recipe.id}
											className="min-h-0 items-start py-2"
											onClick={() => add(recipe.build())}
										>
											<span className="flex flex-col gap-0.5">
												<span className="text-ink-1">{recipe.title}</span>
												<span className="text-ink-3 text-xs leading-[1.4]">
													{recipe.description}
												</span>
											</span>
										</DropdownMenuItem>
									))}
								</DropdownMenuContent>
							</DropdownMenu>
						</div>
						{errors.rules ? (
							<p className="m-0 text-destructive-text text-xs">
								{errors.rules}
							</p>
						) : null}
					</SectionBody>
				)}
				{(view.standard ?? []).length > 0 ? (
					<SectionBody anchor="firewall-standard">
						<StandardProtection standard={view.standard ?? []} />
					</SectionBody>
				) : null}
				{dirty || apply.isError ? (
					<ApplyBar
						state={apply.isError ? "failed" : invalid ? "invalid" : "dirty"}
						message={
							apply.isError
								? errorMessage(apply.error)
								: invalid
									? "Fix the highlighted rules."
									: "Unsaved changes, applied without a deploy."
						}
						pending={apply.isPending}
						canApply={dirty && !invalid}
						onDiscard={() => {
							apply.reset();
							setEdited(null);
						}}
						onApply={() => apply.mutate(rules)}
					/>
				) : null}
			</Section>
			{rules.length > 0 || (view.standard ?? []).length > 0 ? (
				<Section
					title="Try a request"
					description="Describe a request and see what the rules as edited, then the standard protection, do with it before you apply them."
				>
					<SectionBody anchor="firewall-try">
						<TryRequest
							rules={rules}
							standard={view.standard ?? []}
							countryKnown={view.geoAvailable}
							onShowRule={(index) => {
								const rule = rules[index];
								if (rule) setOpen(rule.id);
							}}
						/>
					</SectionBody>
				</Section>
			) : null}
		</>
	);
}

/** Which domains the rules cover, on which server, and how the proxy runs them. */
function Coverage({
	view,
	rules,
}: {
	view: FirewallView;
	rules: readonly FirewallRule[];
}) {
	const hosts = view.hosts ?? [];
	const own = rules.some((r) => !r.disabled);
	const what = own
		? "your rules and the standard protection"
		: "the standard protection";
	const where = view.server ? (
		<>
			{" "}
			on <span className="font-mono text-ink-1">{view.server}</span>
		</>
	) : null;
	return (
		<FactLine>
			{hosts.length === 0 ? (
				<>
					This service has no domain yet. Rules apply to every domain you add.
				</>
			) : (
				<>
					Covers{" "}
					{hosts.map((host, i) => (
						<span key={host}>
							{i > 0 ? (i === hosts.length - 1 ? " and " : ", ") : null}
							<span className="font-mono text-ink-1">{host}</span>
						</span>
					))}
					{where}.
				</>
			)}{" "}
			{view.enforcement === "guard"
				? "Country rules (and visitor addresses behind Cloudflare) are checked by a small guard next to the proxy, about a millisecond per request."
				: view.enforcement === "routes"
					? `The proxy applies ${what} in its own routing, with no extra hop.`
					: null}
		</FactLine>
	);
}

/** What keeps the rules from working as written, and how to fix it. */
function Problems({
	view,
	rules,
}: {
	view: FirewallView;
	rules: readonly FirewallRule[];
}) {
	const out: {
		key: string;
		title: string;
		body: string;
		tone: "warning" | "info";
	}[] = [];
	if (!view.supported) {
		out.push({
			key: "agent",
			tone: "warning",
			title: "The agent is too old for firewalls",
			body: `Update the agent on ${view.server}: until then it applies neither your rules nor the standard protection.`,
		});
	}
	if (usesCountry(rules) && !view.geoAvailable) {
		out.push({
			key: "geo",
			tone: "warning",
			title: "Country rules need geolocation",
			body: `Turn on traffic analytics and geolocation for ${view.server || "the server"} in its settings. The free DB-IP country database needs no account.`,
		});
	}
	if (!view.deployed && rules.some((r) => !r.disabled)) {
		out.push({
			key: "deploy",
			tone: "info",
			title: "Not running yet",
			body: "The rules apply with the service's first deploy.",
		});
	}
	if (out.length === 0) return null;
	return (
		<div className="flex flex-col gap-2">
			{out.map((p) => (
				<Alert key={p.key} variant={p.tone}>
					<AlertTitle>{p.title}</AlertTitle>
					<AlertDescription>{p.body}</AlertDescription>
				</Alert>
			))}
		</div>
	);
}

/** The empty state: only the standard protection applies; start from a recipe. */
function Recipes({
	geoAvailable,
	onPick,
	onBlank,
}: {
	geoAvailable: boolean;
	onPick(recipe: Recipe): void;
	onBlank(): void;
}) {
	return (
		<div className="flex flex-col gap-3">
			<p className="m-0 text-ink-2 text-sm">
				No rules of your own: only the standard protection below applies. Add a
				common one, or write your own.
			</p>
			<ul className="m-0 grid list-none grid-cols-1 gap-2 p-0 @lg/section:grid-cols-2">
				{RECIPES.map((recipe) => (
					<li key={recipe.id}>
						<button
							type="button"
							onClick={() => onPick(recipe)}
							className="surface-raised pressable flex h-full w-full flex-col items-start gap-1 rounded-card px-3 py-2.5 text-left outline-none hover:border-line-control focus-visible:focus-ring"
						>
							<span className="flex items-center gap-1.5 font-medium text-ink-1 text-sm">
								<BanIcon aria-hidden className="size-3.5 text-bad" />
								{recipe.title}
							</span>
							<span className="text-ink-3 text-xs leading-[1.45]">
								{recipe.description}
							</span>
							{recipe.geo && !geoAvailable ? (
								<span className="text-ink-4 text-2xs">
									Needs geolocation on the server.
								</span>
							) : null}
						</button>
					</li>
				))}
			</ul>
			<div>
				<Button type="button" variant="secondary" size="sm" onClick={onBlank}>
					<PlusIcon aria-hidden />
					Write a rule
				</Button>
			</div>
		</div>
	);
}

/**
 * The standard protection: rules NebulaCtrl keeps on every HTTP service, after
 * the service's own. Shown in evaluation order, below the editable list, and
 * locked: there is nothing to switch, only what it blocks and how to exempt a
 * path.
 */
function StandardProtection({
	standard,
}: {
	standard: readonly StandardRule[];
}) {
	return (
		<div className="flex flex-col gap-2">
			<div className="flex items-baseline justify-between gap-3">
				<span className="font-medium text-ink-1 text-sm">
					Standard protection
				</span>
				<span className="text-ink-3 text-xs">Always on, after your rules</span>
			</div>
			<ul
				aria-label="Standard protection"
				className="rule-rows m-0 list-none p-0"
			>
				{standard.map((s) => (
					<li key={s.rule.id} className="flex items-start gap-2">
						<LockIcon
							aria-hidden
							className="mt-0.5 ml-7 size-3.5 shrink-0 text-ink-3"
						/>
						<span className="flex min-w-0 flex-1 flex-col gap-1.5">
							<span className="font-medium text-ink-1 text-sm">
								{s.rule.name}
							</span>
							<span className="text-ink-3 text-xs leading-[1.45]">
								{s.description}
							</span>
							<span className="flex flex-wrap gap-1">
								{(s.examples ?? []).map((example) => (
									<code
										key={example}
										className="rounded-[5px] bg-surface-raised px-1.5 py-0.5 text-ink-2 text-xs"
									>
										{example}
									</code>
								))}
							</span>
						</span>
						<Badge variant="outline">Block</Badge>
					</li>
				))}
			</ul>
			<p className="m-0 text-ink-3 text-xs">
				To let one of these paths through, add an Allow rule for it: your rules
				run first.
			</p>
		</div>
	);
}

function RuleRow({
	rule,
	index,
	count,
	errors,
	open,
	onToggle,
	onChange,
	onMove,
	onDuplicate,
	onDelete,
}: {
	rule: FirewallRule;
	index: number;
	count: number;
	errors: RuleErrors;
	open: boolean;
	onToggle(): void;
	onChange(next: FirewallRule): void;
	onMove(to: number): void;
	onDuplicate(): void;
	onDelete(): void;
}) {
	const id = useId();
	const block = rule.action === "block";
	const off = Boolean(rule.disabled);
	const broken = Object.keys(errors).some((key) =>
		key.startsWith(`${rule.id}.`),
	);
	const Icon = block ? BanIcon : ShieldCheckIcon;
	return (
		<li className="flex flex-col">
			<div className="flex items-start gap-2">
				<span className="w-5 shrink-0 pt-[3px] text-right font-mono text-2xs text-ink-4 tabular-nums">
					{index + 1}
				</span>
				<button
					type="button"
					id={`${id}-head`}
					aria-expanded={open}
					aria-controls={`${id}-body`}
					onClick={onToggle}
					className="group/rule flex min-w-0 flex-1 items-start gap-2 rounded-ctl text-left outline-none focus-visible:focus-ring"
				>
					<Icon
						aria-hidden
						className={cn(
							"mt-0.5 size-3.5 shrink-0",
							off ? "text-ink-4" : block ? "text-bad" : "text-ok",
						)}
					/>
					<span className="flex min-w-0 flex-1 flex-col gap-0.5">
						<span className="flex min-w-0 items-center gap-2">
							<span
								className={cn(
									"truncate font-medium text-sm",
									off ? "text-ink-3" : "text-ink-1",
								)}
							>
								{rule.name || "Untitled rule"}
							</span>
							{off ? <Badge variant="outline">Off</Badge> : null}
							{broken ? <Badge variant="warning">Needs fix</Badge> : null}
						</span>
						<span
							className={cn(
								"line-clamp-2 text-xs leading-[1.45]",
								off ? "text-ink-4" : "text-ink-3",
							)}
						>
							{ruleSentence(rule)}
						</span>
					</span>
					<DisclosureChevron
						open={open}
						className="mt-0.5 size-3.5 shrink-0 text-ink-3 group-hover/rule:text-ink-1"
					/>
				</button>
				<div className="flex shrink-0 items-center gap-0.5">
					<Switch
						size="sm"
						checked={!off}
						aria-label={`Rule ${index + 1}, ${rule.name}: on`}
						onCheckedChange={(on) => {
							const next = { ...rule };
							if (on) delete next.disabled;
							else next.disabled = true;
							onChange(next);
						}}
						className="mr-1.5"
					/>
					<Button
						type="button"
						variant="ghost"
						size="icon-sm"
						aria-label={`Move rule ${index + 1} up`}
						disabled={index === 0}
						onClick={() => onMove(index - 1)}
					>
						<ArrowUpIcon aria-hidden />
					</Button>
					<Button
						type="button"
						variant="ghost"
						size="icon-sm"
						aria-label={`Move rule ${index + 1} down`}
						disabled={index === count - 1}
						onClick={() => onMove(index + 1)}
					>
						<ArrowDownIcon aria-hidden />
					</Button>
					<DropdownMenu>
						<DropdownMenuTrigger
							render={
								<Button
									type="button"
									variant="ghost"
									size="icon-sm"
									aria-label={`More for rule ${index + 1}`}
								>
									<EllipsisIcon aria-hidden />
								</Button>
							}
						/>
						<DropdownMenuContent align="end">
							<DropdownMenuItem onClick={onDuplicate}>
								<CopyIcon aria-hidden />
								Duplicate
							</DropdownMenuItem>
							<DropdownMenuSeparator />
							<DropdownMenuItem variant="destructive" onClick={onDelete}>
								<Trash2Icon aria-hidden />
								Delete rule
							</DropdownMenuItem>
						</DropdownMenuContent>
					</DropdownMenu>
				</div>
			</div>
			<DisclosurePanel open={open} id={`${id}-body`} labelledBy={`${id}-head`}>
				<RuleEditor rule={rule} errors={errors} onChange={onChange} />
			</DisclosurePanel>
		</li>
	);
}

/** Discard and Apply for the firewall: it acts at once, so it has its own bar. */
function ApplyBar({
	state,
	message,
	pending,
	canApply,
	onDiscard,
	onApply,
}: {
	state: "dirty" | "invalid" | "failed";
	message: string;
	pending: boolean;
	canApply: boolean;
	onDiscard(): void;
	onApply(): void;
}) {
	return (
		<div
			className="surface-float sticky bottom-0 z-10 -mx-1 flex flex-wrap items-center gap-3 rounded-ctl px-3 py-2.5"
			aria-live="polite"
		>
			<SaveBarStatus state={state}>{message}</SaveBarStatus>
			<Button
				type="button"
				variant="secondary"
				disabled={pending}
				onClick={onDiscard}
			>
				Discard
			</Button>
			<Button
				type="button"
				variant="primary"
				disabled={!canApply || pending}
				onClick={onApply}
			>
				{pending ? <Spinner /> : null}
				Apply firewall
			</Button>
		</div>
	);
}
