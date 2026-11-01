import { cn } from "cn";
import {
	ChevronDownIcon,
	EllipsisIcon,
	ListChecksIcon,
	RotateCcwIcon,
	Undo2Icon,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { Environment, Project } from "#/api/models";
import { useConfirm } from "#/components/common/confirm-dialog";
import { RocketIcon } from "#/components/icons/animated/rocket-icon";
import { useTriggeredPlay } from "#/components/icons/animated/use-play-once";
import type { PaletteCommand } from "#/components/shell/palette-registry";
import { useRegisterCommands } from "#/components/shell/palette-registry";
import { useProjectContext } from "#/components/shell/use-project-context";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuGroup,
	DropdownMenuItem,
	DropdownMenuLabel,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "#/components/ui/dropdown-menu";
import { toast } from "#/components/ui/toast";
import { ProjectMenuGroup } from "#/features/projects/project-menu-group";
import { RenameProjectDialog } from "#/features/projects/rename-project-dialog";
import { GuardedKey, useGuard } from "./guarded-key";
import { onOpenStagedReview } from "./open-review";
import { StagedReview } from "./staged-review";
import { useBarNarrow } from "./use-bar-narrow";
import {
	type EnvironmentDeploy,
	useEnvironmentDeploy,
} from "./use-environment-deploy";

const KEY =
	"btn-primary pressable flex h-[30px] cursor-pointer items-center gap-2 text-[13px] font-medium leading-none outline-none focus-visible:outline-2 focus-visible:outline-accent-ink focus-visible:outline-offset-2 aria-disabled:cursor-default aria-disabled:opacity-60 disabled:cursor-default disabled:opacity-60";

function CountBadge({ count }: { count: number }) {
	return (
		<span
			aria-hidden
			className="inline-grid h-[18px] min-w-[18px] place-items-center rounded-full bg-[rgb(20_10_90/.45)] px-[5px] font-mono font-semibold text-[11px] text-white leading-none tabular-nums shadow-[inset_0_1px_1px_rgb(2_4_10/.5),0_1px_0_rgb(255_255_255/.18)]"
		>
			{count}
		</span>
	);
}

/**
 * The current environment's group: Review / Redeploy current / Discard all,
 * named by the environment so it is never read as an action on the project.
 */
function DeployMenuItems({
	environmentName,
	count,
	disabled,
	onReview,
	onRedeploy,
	onDiscard,
}: {
	environmentName: string;
	count: number;
	disabled: boolean;
	onReview(): void;
	onRedeploy(): void;
	onDiscard(): void;
}) {
	return (
		<DropdownMenuGroup>
			<DropdownMenuLabel className="truncate">
				{environmentName}
			</DropdownMenuLabel>
			<DropdownMenuItem
				render={<button type="button" />}
				disabled={count === 0}
				onClick={onReview}
			>
				<ListChecksIcon />
				Review changes
			</DropdownMenuItem>
			<DropdownMenuItem
				render={<button type="button" />}
				disabled={disabled}
				onClick={onRedeploy}
			>
				<RotateCcwIcon />
				Redeploy current
			</DropdownMenuItem>
			<DropdownMenuSeparator />
			<DropdownMenuItem
				render={<button type="button" />}
				variant="destructive"
				disabled={count === 0 || disabled}
				onClick={onDiscard}
			>
				<Undo2Icon />
				Discard all
			</DropdownMenuItem>
		</DropdownMenuGroup>
	);
}

function EnvironmentDeployButton({
	environment,
	project,
}: {
	environment: Pick<Environment, "id" | "name" | "isProduction">;
	/** Undefined until the project list has answered; its actions wait for it. */
	project: Project | undefined;
}) {
	const [renaming, setRenaming] = useState(false);
	const [reviewing, setReviewing] = useState(false);
	// Set when the sheet was opened for one service (canvas dock, palette).
	const [scopeServiceId, setScopeServiceId] = useState<string | undefined>();
	useEffect(
		() =>
			onOpenStagedReview((options) => {
				setScopeServiceId(options.serviceId);
				setReviewing(true);
			}),
		[],
	);
	const deploy: EnvironmentDeploy = useEnvironmentDeploy(
		environment,
		reviewing,
	);
	const guard = useGuard();
	const confirm = useConfirm();
	const { count, busy, sent } = deploy;
	// The rocket plays once when a deploy is sent, from the key or from the
	// review sheet, and not for a deploy sent by keyboard.
	const [launching, launch] = useTriggeredPlay();
	const launchingDeploy = useMemo<EnvironmentDeploy>(
		() => ({
			...deploy,
			deployStaged: (...args) => {
				launch();
				return deploy.deployStaged(...args);
			},
		}),
		[deploy, launch],
	);
	const production = environment.isProduction;
	// Only the variant that fits the bar is rendered: a hidden production key
	// would still be clickable by a script and skip the guard's cover.
	const probe = useRef<HTMLSpanElement>(null);
	const narrow = useBarNarrow(probe);
	const guardedKey = production && !narrow;

	const review = useCallback(() => {
		setScopeServiceId(undefined);
		setReviewing(true);
	}, []);
	const redeploy = async () => {
		if (production) {
			const ok = await confirm({
				title: `Redeploy current releases in ${environment.name}?`,
				body: "Every deployed service gets a new deployment of its current release and configuration. Services with staged changes are left alone.",
				cta: "Redeploy",
				hold: false,
			});
			if (!ok) return;
		}
		await deploy.redeployCurrent();
	};
	const discardAll = async () => {
		const ok = await confirm({
			title: `Discard ${count} staged change${count === 1 ? "" : "s"}?`,
			body: `Every service in ${environment.name} returns to its deployed configuration. This cannot be undone.`,
			cta: "Discard all",
			danger: true,
			hold: false,
		});
		if (ok) await deploy.discardAll();
	};
	const press = async () => {
		if (count === 0 || busy) return;
		try {
			await launchingDeploy.deployStaged();
		} finally {
			guard.resolved();
		}
	};

	const commands = useMemo<PaletteCommand[]>(
		() => [
			{
				id: "deploy:staged",
				label: "Deploy staged changes",
				group: "Action",
				hint:
					count === 0
						? "nothing staged"
						: `${count} change${count === 1 ? "" : "s"} · review first`,
				icon: ListChecksIcon,
				// Nothing staged: findable by typing, never offered first.
				disabled: count === 0,
				keywords: ["apply", "release", "review", "staged", "changes"],
				run: () => {
					if (count === 0) {
						toast.add({
							type: "info",
							title: "Nothing staged",
							description:
								"Changes you make to a service wait here until deployed.",
						});
						return;
					}
					review();
				},
			},
		],
		[count, review],
	);
	useRegisterCommands("deploy.staged", commands);

	const menu = (
		<>
			<DeployMenuItems
				environmentName={environment.name}
				count={count}
				disabled={busy || sent}
				onReview={review}
				onRedeploy={() => void redeploy()}
				onDiscard={() => void discardAll()}
			/>
			{project ? (
				<>
					<DropdownMenuSeparator />
					<ProjectMenuGroup
						project={project}
						onRename={() => setRenaming(true)}
					/>
				</>
			) : null}
		</>
	);

	return (
		<>
			<span ref={probe} hidden aria-hidden />
			{guardedKey ? (
				<div className="flex items-center gap-2">
					<GuardedKey
						guard={guard}
						label={`Deploy · ${count}`}
						launching={launching}
						sent={sent}
						disabled={count === 0 || busy}
						disabledReason="Nothing staged to deploy"
						onPress={() => void press()}
					/>
					<DropdownMenu>
						<DropdownMenuTrigger
							aria-label="Deploy options"
							className="btn-secondary pressable grid size-[30px] cursor-pointer place-items-center rounded-ctl outline-none focus-visible:outline-2 focus-visible:outline-accent-ink focus-visible:outline-offset-2"
						>
							<EllipsisIcon aria-hidden className="size-4" />
						</DropdownMenuTrigger>
						<DropdownMenuContent
							align="end"
							sideOffset={8}
							className="w-[220px]"
						>
							{menu}
						</DropdownMenuContent>
					</DropdownMenu>
				</div>
			) : (
				// The split button: the plain Deploy outside production, and on a bar
				// under 760px the icon-only stand-in for the production key too. It
				// opens the review sheet, where the production guard waits.
				<fieldset
					aria-label={production ? "Production deploy" : "Deploy"}
					className="m-0 inline-flex min-w-0 border-0 p-0"
				>
					<button
						type="button"
						aria-disabled={count === 0 || undefined}
						data-busy={busy || sent || undefined}
						title={count === 0 ? "Nothing staged to deploy" : undefined}
						onClick={() => {
							if (count > 0) review();
						}}
						className={cn(
							KEY,
							"rounded-r-none rounded-l-ctl pr-2.5 pl-3 @max-[759px]:gap-1.5 @max-[759px]:px-2.5",
						)}
					>
						<RocketIcon animate={launching} className="size-4" />
						<span className="@max-[759px]:hidden">Deploy</span>
						<CountBadge count={count} />
						<span className="sr-only">
							{production ? "Deploy to production. " : "Deploy. "}
							{count} staged change{count === 1 ? "" : "s"}. Opens review.
						</span>
					</button>
					<DropdownMenu>
						<DropdownMenuTrigger
							aria-label="Deploy options"
							data-busy={busy || sent || undefined}
							className={cn(
								KEY,
								"rounded-r-ctl rounded-l-none border-l-[oklch(0.42_0.17_278/.9)] px-[7px] shadow-[inset_1px_0_0_rgb(255_255_255/.16),inset_0_1px_0_rgb(255_255_255/.25),inset_0_-1px_0_rgb(20_10_90/.4)]",
							)}
						>
							<ChevronDownIcon aria-hidden className="size-3.5" />
						</DropdownMenuTrigger>
						<DropdownMenuContent
							align="end"
							sideOffset={8}
							className="w-[220px]"
						>
							{menu}
						</DropdownMenuContent>
					</DropdownMenu>
				</fieldset>
			)}
			<StagedReview
				open={reviewing}
				onOpenChange={setReviewing}
				scopeServiceId={scopeServiceId}
				environmentName={environment.name}
				isProduction={production}
				deploy={launchingDeploy}
			/>
			{project ? (
				<RenameProjectDialog
					project={project}
					open={renaming}
					onOpenChange={setRenaming}
				/>
			) : null}
		</>
	);
}

/**
 * The top bar's Deploy · N: the staged changes of the environment on screen,
 * and the one place they are applied. Renders nothing outside an
 * environment. A production environment gets the guarded key; any other
 * gets the plain split button.
 */
export function DeployButton() {
	const context = useProjectContext();
	const environment = context?.environment;
	if (!environment) return null;
	return (
		<EnvironmentDeployButton
			environment={environment}
			project={context?.project}
		/>
	);
}
