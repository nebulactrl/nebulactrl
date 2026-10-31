import {
	createContext,
	type ReactNode,
	useCallback,
	useContext,
	useId,
	useRef,
	useState,
} from "react";
import { errorMessage } from "#/api/client-config";
import { TrashIcon } from "#/components/icons/animated/trash-icon";
import { usePlayOnce } from "#/components/icons/animated/use-play-once";
import { HoldActionButton } from "#/components/motion/hold-action-button";
import { Alert, AlertDescription } from "#/components/ui/alert";
import {
	AlertDialog,
	AlertDialogBody,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogTitle,
} from "#/components/ui/alert-dialog";
import { Button } from "#/components/ui/button";
import { useOpenedByKeyboard } from "#/components/ui/dialog";
import { Input } from "#/components/ui/input";
import { Spinner } from "#/components/ui/spinner";
import { GuardedKey, useGuard } from "#/features/deploy-button/guarded-key";

export interface ConfirmOptions {
	/** The question: "Delete checkout-api from production?" */
	title: ReactNode;
	/** What happens, what stays, and whether it can be undone. */
	body: ReactNode;
	/** The action's verb: "Delete service", "Roll back", "Request approval". */
	cta: string;
	/**
	 * The label of the button that backs out. Defaults to "Cancel"; name what
	 * is kept ("Keep deploying") when the action itself cancels something, so
	 * one verb never means both things in the same footer.
	 */
	dismiss?: string;
	/**
	 * A glyph beside the title. `trash` marks a delete: the can's lid lifts
	 * once as the dialog opens, unless the keyboard opened it.
	 */
	glyph?: "trash";
	/** Red confirm button for an irreversible action. */
	danger?: boolean;
	/**
	 * Require a 1.3s pointer/key hold instead of a click. Defaults to `danger`
	 * when no typed confirmation is present; `false` opts a danger action out.
	 */
	hold?: boolean;
	/**
	 * The action changes production: the confirm button is the hatched
	 * production key (lift the cover, then press), the same guard as Deploy.
	 * Wins over `hold`. Name production in `title` or `body`.
	 */
	guarded?: boolean;
	/**
	 * A name the reader must type before the action unlocks, for actions
	 * whose blast radius is a whole service or cluster.
	 */
	typeToConfirm?: string;
	/**
	 * Keep the action locked while something the body still has to show is
	 * loading, so nobody confirms what they have not been shown.
	 */
	blocked?: boolean;
	/**
	 * Runs on confirm with the dialog still open, so a slow action shows its
	 * progress in place and a failure keeps the reader's context. Throwing
	 * keeps the dialog open and shows the error's message under the body.
	 */
	onConfirm?: () => Promise<void> | void;
}

export interface ConfirmDialogProps extends ConfirmOptions {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	/** Called once the action has run (or immediately without `onConfirm`). */
	onConfirmed?: () => void;
}

/** The trash can beside a delete's title, lid lifting once after the open. */
function TrashGlyph() {
	// Wait out the dialog's own 220ms entrance so the lid is seen moving.
	const playing = usePlayOnce(true, 240);
	return (
		<span
			aria-hidden
			className="surface-well grid size-8 shrink-0 place-items-center rounded-ctl text-destructive-text"
		>
			<TrashIcon animate={playing} className="size-4" />
		</span>
	);
}

/**
 * The one confirmation dialog: a question, what it does, an optional typed
 * name gate, Cancel and the action. Prefer `useConfirm()` for imperative
 * call sites; render this directly when the dialog's open state already
 * lives in the URL or a parent.
 */
export function ConfirmDialog({
	open,
	onOpenChange,
	onConfirmed,
	title,
	body,
	cta,
	dismiss,
	glyph,
	danger = false,
	hold,
	guarded = false,
	typeToConfirm,
	blocked = false,
	onConfirm,
}: ConfirmDialogProps) {
	const inputId = useId();
	// A confirmation opened with the keyboard appears at once: the entrance
	// would only make the reader wait.
	const instant = useOpenedByKeyboard(open);
	const [typed, setTyped] = useState("");
	const [pending, setPending] = useState(false);
	const [error, setError] = useState<string | null>(null);
	const locked =
		blocked || (typeToConfirm !== undefined && typed !== typeToConfirm);
	const holdToConfirm = hold === undefined ? danger : hold;
	const guard = useGuard();

	const reset = () => {
		setTyped("");
		setPending(false);
		setError(null);
		guard.lower("outside");
		guard.resolved();
	};

	const confirm = async () => {
		if (locked || pending) return;
		if (onConfirm) {
			setPending(true);
			setError(null);
			try {
				await onConfirm();
			} catch (cause) {
				setPending(false);
				guard.resolved();
				// The generated client throws the parsed problem document, a
				// plain object, so `instanceof Error` would drop the server's
				// reason; errorMessage reads it in full.
				setError(errorMessage(cause));
				return;
			}
		}
		reset();
		onConfirmed?.();
		onOpenChange(false);
	};

	return (
		<AlertDialog
			open={open}
			onOpenChange={(next) => {
				// A running action cannot be abandoned half-way from the dialog.
				if (!next && pending) return;
				if (!next) reset();
				onOpenChange(next);
			}}
		>
			<AlertDialogContent instant={instant}>
				<AlertDialogBody className="gap-2.5">
					{glyph === "trash" ? (
						<div className="flex items-center gap-3">
							<TrashGlyph />
							<AlertDialogTitle>{title}</AlertDialogTitle>
						</div>
					) : (
						<AlertDialogTitle>{title}</AlertDialogTitle>
					)}
					{/* A div, not the default paragraph: a body may hold lists. */}
					<AlertDialogDescription render={<div />}>
						{body}
					</AlertDialogDescription>
					{typeToConfirm !== undefined ? (
						<div className="mt-1.5 flex flex-col gap-2">
							<label htmlFor={inputId} className="type-label">
								Type{" "}
								<span className="font-mono text-ink-1">{typeToConfirm}</span> to
								confirm
							</label>
							<Input
								id={inputId}
								value={typed}
								onChange={(event) => setTyped(event.target.value)}
								onKeyDown={(event) => {
									// A hold dialog is confirmed by holding, never by a
									// keystroke: Enter would skip the 1.3 s guard.
									if (event.key !== "Enter") return;
									event.preventDefault();
									if (!holdToConfirm) void confirm();
								}}
								autoComplete="off"
								spellCheck={false}
								className="h-[34px] font-mono"
							/>
						</div>
					) : null}
					{error ? (
						<Alert variant="destructive">
							<AlertDescription>{error}</AlertDescription>
						</Alert>
					) : null}
				</AlertDialogBody>
				<AlertDialogFooter>
					<AlertDialogCancel variant="secondary" size="lg" disabled={pending}>
						{dismiss ?? "Cancel"}
					</AlertDialogCancel>
					{guarded ? (
						<GuardedKey
							guard={guard}
							label={cta}
							sent={pending}
							disabled={locked || pending}
							disabledReason="Complete the confirmation first"
							onPress={() => void confirm()}
						/>
					) : holdToConfirm ? (
						<HoldActionButton
							type="horizontal"
							holdDuration={1300}
							disabled={pending || blocked}
							holdingLabel="Keep holding…"
							completeLabel={
								pending ? (
									<>
										<Spinner data-icon="inline-start" />
										Working
									</>
								) : (
									cta
								)
							}
							onHoldComplete={() => void confirm()}
							fillClassName="bg-destructive-deep"
							labelClassName="text-[13px] leading-none"
							activeLabelClassName="text-inverse-hover"
							// The fill's clip reads --hold-radius, not the button's
							// border-radius; setting the corner through the variable keeps
							// the growing fill inside the same corners as the button.
							className="h-[34px] min-w-0 px-4 font-semibold"
							refused={locked}
						>
							Hold to {cta.replace(/…$/, "").toLowerCase()}
						</HoldActionButton>
					) : (
						<Button
							variant={danger ? "danger-solid" : "primary"}
							size="lg"
							disabled={locked || pending}
							onClick={() => void confirm()}
						>
							{pending ? <Spinner data-icon="inline-start" /> : null}
							{cta}
						</Button>
					)}
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}

type Confirm = (options: ConfirmOptions) => Promise<boolean>;

const ConfirmContext = createContext<Confirm | null>(null);

/**
 * Mounts the single confirmation dialog `useConfirm()` drives. Mount once,
 * inside the toast and tooltip providers, around the app.
 */
export function ConfirmProvider({ children }: { children: ReactNode }) {
	const [options, setOptions] = useState<ConfirmOptions | null>(null);
	const [open, setOpen] = useState(false);
	const resolveRef = useRef<((confirmed: boolean) => void) | null>(null);

	const confirm = useCallback<Confirm>((next) => {
		// A second request supersedes the first, which counts as cancelled.
		resolveRef.current?.(false);
		setOptions(next);
		setOpen(true);
		return new Promise<boolean>((resolve) => {
			resolveRef.current = resolve;
		});
	}, []);

	const settle = (confirmed: boolean) => {
		resolveRef.current?.(confirmed);
		resolveRef.current = null;
	};

	return (
		<ConfirmContext.Provider value={confirm}>
			{children}
			{options ? (
				<ConfirmDialog
					{...options}
					open={open}
					onOpenChange={(next) => {
						setOpen(next);
						if (!next) settle(false);
					}}
					onConfirmed={() => settle(true)}
				/>
			) : null}
		</ConfirmContext.Provider>
	);
}

/**
 * Asks before doing something consequential and resolves `true` once the
 * reader confirmed (and `onConfirm`, if given, finished), `false` if they
 * backed out.
 *
 *     const confirm = useConfirm();
 *     if (await confirm({ title, body, cta: "Delete service", danger: true,
 *       typeToConfirm: service.slug })) { ... }
 */
export function useConfirm(): Confirm {
	const confirm = useContext(ConfirmContext);
	if (!confirm) {
		throw new Error(
			"useConfirm needs a <ConfirmProvider> above it; mount one around the app next to the Toaster.",
		);
	}
	return confirm;
}
