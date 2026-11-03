import { DatabaseZapIcon } from "lucide-react";
import {
	Empty,
	EmptyDescription,
	EmptyHeader,
	EmptyTitle,
} from "#/components/ui/empty";

/** The pane's message when the database cannot be read right now ("Start the database to browse its data."). */
export function BlockedState({ message }: { message: string }) {
	return (
		<Empty role="status" className="py-10">
			<DatabaseZapIcon aria-hidden className="size-6 text-ink-4" />
			<EmptyHeader>
				<EmptyTitle>{message}</EmptyTitle>
				<EmptyDescription>
					The Data tab reads the database through its agent.
				</EmptyDescription>
			</EmptyHeader>
		</Empty>
	);
}
