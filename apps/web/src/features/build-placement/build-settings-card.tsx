import type { ReactNode } from "react";
import { Button } from "#/components/ui/button";
import { Card, CardContent, CardFooter } from "#/components/ui/card";
import { FieldError } from "#/components/ui/field";
import { Spinner } from "#/components/ui/spinner";

/**
 * The card the team and the project build settings share: the control on top,
 * and a footer that says whether there is anything to save, with Discard and
 * Save. The footer states the unsaved state in words, like the server form.
 */
export function BuildSettingsCard({
	dirty,
	saving,
	error,
	onSave,
	onDiscard,
	children,
}: {
	dirty: boolean;
	saving: boolean;
	error: string | null;
	onSave: () => void;
	onDiscard: () => void;
	children: ReactNode;
}) {
	return (
		<Card>
			<CardContent className="py-5">{children}</CardContent>
			<CardFooter className="justify-between">
				<span className="min-w-0 text-xs">
					{error ? (
						<FieldError>{error}</FieldError>
					) : dirty ? (
						<span className="text-ink-3">You have unsaved changes.</span>
					) : (
						<span className="text-ink-3">No unsaved changes.</span>
					)}
				</span>
				<span className="flex items-center gap-2">
					{dirty ? (
						<Button type="button" variant="ghost" onClick={onDiscard}>
							Discard
						</Button>
					) : null}
					<Button
						type="button"
						variant="primary"
						disabled={!dirty || saving}
						onClick={onSave}
					>
						{saving ? <Spinner /> : null}
						Save changes
					</Button>
				</span>
			</CardFooter>
		</Card>
	);
}
