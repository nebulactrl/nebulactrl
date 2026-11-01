import { FileTextIcon } from "lucide-react";
import type { EnvTemplateScan } from "#/api/gen/types.gen";
import { Loading } from "#/components/common/loading";
import {
	checkedCount,
	type ExampleChoices,
	emptyCount,
} from "#/features/variables/env-example";
import { EnvExampleList } from "#/features/variables/env-example-list";
import type { EnvPreview } from "./use-env-preview";

/**
 * One quiet line under the repository fields: what the read of its example
 * files found. Nothing while there is nothing to say (no file, or not read).
 */
export function EnvPreviewNote({ preview }: { preview: EnvPreview }) {
	if (preview.loading) {
		return (
			<Loading
				label="Looking for .env.example"
				className="h-auto min-h-0 w-auto justify-start text-xs"
			/>
		);
	}
	const scan = preview.scan;
	if (!scan) return null;
	if (scan.status === "unavailable") {
		return (
			<p className="text-ink-3 text-xs" role="status">
				The example files could not be read: {scan.message}
			</p>
		);
	}
	if (preview.items.length === 0) return null;
	const files = (scan.files ?? []).map((file) => file.path);
	return (
		<p className="flex items-center gap-2 text-ink-3 text-xs" role="status">
			<FileTextIcon aria-hidden className="size-3.5 shrink-0" />
			<span>
				{preview.items.length} {preview.items.length === 1 ? "key" : "keys"} in{" "}
				<span className="font-mono text-ink-2">{files.join(", ")}</span>. You
				review them next.
			</span>
		</p>
	);
}

/** The Variables step: the keys of the example files, to add with the service. */
export function VariablesStep({
	scan,
	items,
	choices,
	onChoices,
}: {
	scan: EnvTemplateScan;
	items: EnvPreview["items"];
	choices: ExampleChoices;
	onChoices(next: ExampleChoices): void;
}) {
	const count = checkedCount(items, choices);
	const empty = emptyCount(items, choices);
	const skipped = scan.skipped ?? [];
	return (
		<>
			<p className="type-secondary">
				Checked keys are staged with the service; nothing runs until you deploy.
				A key that needs your own value has a field; leave it empty to add the
				key now and fill it in later (the deploy plan reminds you).
			</p>
			<EnvExampleList
				idPrefix="create-env"
				items={items}
				choices={choices}
				onChoices={onChoices}
				autoFocus
			/>
			<p className="type-secondary" aria-live="polite">
				{count} of {items.length} selected
				{empty > 0 ? ` · ${empty} without a value` : ""}
				{skipped.length > 0
					? `. ${skipped.map((s) => s.key).join(", ")} ${skipped.length === 1 ? "is" : "are"} set by NebulaCtrl and left out.`
					: ""}
			</p>
		</>
	);
}
