import { cn } from "cn";
import { Alert, AlertDescription, AlertTitle } from "#/components/ui/alert";
import { Badge } from "#/components/ui/badge";
import {
	checkVerdict,
	componentCountText,
	groupViolations,
	type StackCheckResult,
	type StackCheckState,
	type StackComponent,
	UNRESTRICTED_HINT,
	type ViolationGroup,
	verdictText,
} from "./model";

/** The components a compose file declares: name, image and ports as ruled rows. */
function ComponentList({
	components,
	className,
}: {
	components: readonly StackComponent[];
	className?: string;
}) {
	if (components.length === 0) return null;
	return (
		<ul
			aria-label="Components"
			className={cn("rule-rows m-0 list-none p-0", className)}
		>
			{components.map((component) => (
				<li
					key={component.name}
					className="flex min-w-0 flex-wrap items-baseline gap-x-3 gap-y-0.5 py-1.5"
				>
					<span className="font-medium font-mono text-[12.5px] text-ink-1">
						{component.name}
					</span>
					<span
						title={component.image}
						className="min-w-0 flex-1 truncate font-mono text-[11.5px] text-ink-3"
					>
						{component.image || "no image"}
					</span>
					<span className="font-mono text-[11.5px] text-ink-3 tabular-nums">
						{(component.ports ?? []).length > 0
							? (component.ports ?? []).map((port) => `:${port}`).join(" ")
							: "no ports"}
					</span>
				</li>
			))}
		</ul>
	);
}

function GroupBlock({
	group,
	showHint,
	hint,
	unrestricted,
}: {
	group: ViolationGroup;
	showHint: boolean;
	hint: string;
	unrestricted: boolean;
}) {
	// A restricted problem is lifted on an unrestricted server; one that is still
	// listed there is only informational, so it reads as a note, not a block.
	const blocking = group.unsupported.length > 0;
	const items = [
		...group.unsupported.map((violation) => ({
			violation,
			label: "Not supported",
			tone: "destructive" as const,
		})),
		...group.restricted.map((violation) => ({
			violation,
			label: unrestricted ? "Allowed here" : "Restricted",
			tone: unrestricted ? ("success" as const) : ("warning" as const),
		})),
	];
	return (
		<Alert
			variant={blocking ? "destructive" : unrestricted ? "info" : "warning"}
			role="note"
			data-service={group.service}
		>
			<AlertTitle className="font-mono">
				{group.service === "" ? "Compose file" : group.service}
			</AlertTitle>
			<AlertDescription>
				<ul className="m-0 flex list-none flex-col gap-1.5 p-0">
					{items.map(({ violation, label, tone }) => (
						<li
							key={`${violation.key}:${violation.message}`}
							className="flex flex-wrap items-baseline gap-x-2 gap-y-1"
						>
							<Badge variant={tone}>{label}</Badge>
							<span className="font-mono text-[11.5px] text-ink-1">
								{violation.key}
							</span>
							<span className="min-w-0 flex-1 basis-48 text-ink-3">
								{violation.message}
							</span>
						</li>
					))}
				</ul>
				{showHint && group.restricted.length > 0 ? (
					<p className="mt-2">{hint}</p>
				) : null}
			</AlertDescription>
		</Alert>
	);
}

/** The violations of a check grouped by compose service, `unsupported` and `restricted` told apart. */
export function ViolationList({
	result,
	hint = UNRESTRICTED_HINT,
}: {
	result: Pick<StackCheckResult, "violations" | "unrestricted">;
	/** The line under a restricted problem that only an admin can lift. */
	hint?: string;
}) {
	const groups = groupViolations(result.violations);
	if (groups.length === 0) return null;
	const verdict = checkVerdict(result);
	return (
		<section className="flex flex-col gap-2" aria-label="Problems">
			{groups.map((group) => (
				<GroupBlock
					key={group.service}
					group={group}
					showHint={verdict.needsAdmin}
					hint={hint}
					unrestricted={result.unrestricted}
				/>
			))}
		</section>
	);
}

/**
 * The live check under a compose editor: the verdict in a sentence, the
 * components it found, the violations grouped by service and the notes. The
 * last answer stays on screen while the next is on its way; `serverName`
 * names the server the check judged against.
 */
export function StackCheckPanel({
	state,
	serverName,
}: {
	state: StackCheckState;
	serverName?: string | undefined;
}) {
	if (state.empty) {
		return (
			<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
				Paste or upload a Compose file. Its services become the stack's
				components.
			</p>
		);
	}
	const { result } = state;
	const verdict = result
		? checkVerdict({
				violations: result.violations,
				unrestricted: result.unrestricted,
			})
		: null;
	return (
		<div className="flex flex-col gap-3" aria-busy={state.pending}>
			<div className="flex items-baseline gap-2">
				<p
					role="status"
					aria-live="polite"
					className="m-0 font-medium text-[13px] text-ink-1"
				>
					{state.error
						? "The check did not run."
						: verdict
							? verdict.blocked
								? verdictText(verdict)
								: `${componentCountText(result?.components.length ?? 0)}. ${verdictText(verdict)}`
							: "Checking the Compose file…"}
				</p>
				<span
					aria-hidden={!state.pending || result === null}
					className={cn(
						"ml-auto text-2xs text-ink-3 transition-opacity duration-quick ease-out",
						state.pending && result ? "opacity-100" : "opacity-0",
					)}
				>
					Checking
				</span>
			</div>
			{state.error ? (
				<Alert variant="destructive">
					<AlertDescription>{state.error}</AlertDescription>
				</Alert>
			) : null}
			{result ? (
				<>
					<ComponentList components={result.components} />
					<ViolationList result={result} />
					{result.unrestricted && serverName ? (
						<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
							{serverName} is Unrestricted: restricted settings are allowed.
						</p>
					) : null}
					{result.composeAvailable === false ? (
						<Alert variant="warning" role="note">
							<AlertDescription>
								{serverName ?? "The server"} does not have the Docker Compose
								plugin (2.24 or newer). The stack can be staged, but it cannot
								be deployed until the plugin is installed.
							</AlertDescription>
						</Alert>
					) : null}
					{result.notes.length > 0 ? (
						<ul
							aria-label="Notes"
							className="m-0 flex list-none flex-col gap-1 p-0 text-[12px] text-ink-3 leading-[1.5]"
						>
							{result.notes.map((note) => (
								<li key={note}>{note}</li>
							))}
						</ul>
					) : null}
				</>
			) : null}
		</div>
	);
}
