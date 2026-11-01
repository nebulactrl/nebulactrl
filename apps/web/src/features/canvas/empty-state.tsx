import { DatabaseIcon, LayersIcon } from "lucide-react";
import { useRef, useState } from "react";
import { AsciiBackdrop } from "#/components/common/ascii/ascii-backdrop";
import { EmptyState } from "#/components/common/empty-state";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { Kbd } from "#/components/ui/kbd";
import { CreateButton, useCreateService } from "#/features/create";
import { templatesActions } from "#/features/templates/store";

export interface CanvasLoadError {
	message: string;
	onRetry(): void;
}

/**
 * What an empty environment shows in the middle of the canvas, over the
 * Eclipse (the pixel-dithered ringed planet): the services box, one primary
 * action (the Create menu) and one secondary (a database),
 * with the shortcuts that do the same. A load failure sits beside the
 * invitation, never in place of the canvas.
 */
export function CanvasEmptyState({
	error,
}: {
	error?: CanvasLoadError | undefined;
}) {
	const create = useCreateService();
	const cardRef = useRef<HTMLDivElement>(null);
	const [eclipse, setEclipse] = useState(false);
	return (
		<div className="pointer-events-none absolute inset-0 z-[5] grid place-items-center overflow-hidden p-6">
			{/* The page's one animated texture: the Eclipse the sign-in page
			    uses, held inside the canvas and dimmed round the card. */}
			<AsciiBackdrop
				cardRef={cardRef}
				active={eclipse}
				onActiveChange={setEclipse}
				className="absolute z-0"
			/>
			<div
				ref={cardRef}
				data-slot="canvas-empty"
				data-canvas-chrome
				className="pointer-events-auto relative flex w-full max-w-[420px] flex-col items-center gap-3"
			>
				<EmptyState
					frame="raised"
					illustration="box"
					title="Create your first service"
					body="Deploy a Docker image or a Git repository to one of your servers. It appears here as a card you can arrange, link and open."
					action={<CreateButton variant="primary" size="lg" />}
					secondaryAction={
						<>
							<Button
								variant="secondary"
								size="lg"
								onClick={() => templatesActions.open()}
							>
								<LayersIcon aria-hidden />
								Browse templates
							</Button>
							<Button
								variant="secondary"
								size="lg"
								onClick={() => create.open("database")}
							>
								<DatabaseIcon aria-hidden />
								Add database
							</Button>
						</>
					}
					hint={
						<>
							<Kbd variant="key">⌘K</Kbd> actions
							<span aria-hidden>·</span>
							right-click to create here
						</>
					}
				/>
				{error ? (
					<Alert variant="destructive" className="mt-1">
						<AlertDescription className="flex flex-col items-start gap-2">
							<span className="break-words">
								Couldn't load this environment's services: {error.message}
							</span>
							<Button variant="secondary" size="xs" onClick={error.onRetry}>
								Try again
							</Button>
						</AlertDescription>
					</Alert>
				) : null}
			</div>
		</div>
	);
}
