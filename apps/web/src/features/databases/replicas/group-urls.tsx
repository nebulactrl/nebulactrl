import { ClipboardCopyIcon } from "lucide-react";
import { useState } from "react";
import { Button } from "#/components/ui/button";
import { Spinner } from "#/components/ui/spinner";
import { useExportReveal } from "../use-export-reveal";

const WRITER = "DATABASE_WRITE_URL";
const READER = "DATABASE_READ_URL";

/**
 * Copy the writer URL (always the current primary) or the reader URL (a read
 * replica the consumer can reach, the primary when there is none). They are
 * the same from any member of the group; the value comes from the audited
 * reveal endpoint like every connection string.
 */
export function GroupUrls({ serviceId }: { serviceId: string }) {
	const { busy, run } = useExportReveal(serviceId);
	const [key, setKey] = useState<string | null>(null);
	const copy = (next: string) => {
		setKey(next);
		void run(next, "copy", `Could not reveal ${next}`).finally(() =>
			setKey(null),
		);
	};
	return (
		<div className="flex flex-wrap items-center gap-2">
			<Button
				variant="ghost"
				size="md"
				disabled={busy}
				title={`${WRITER}: always the current primary`}
				onClick={() => copy(WRITER)}
			>
				{key === WRITER ? <Spinner /> : <ClipboardCopyIcon aria-hidden />}
				Copy writer URL
			</Button>
			<Button
				variant="ghost"
				size="md"
				disabled={busy}
				title={`${READER}: a read replica the consumer can reach, the primary when there is none`}
				onClick={() => copy(READER)}
			>
				{key === READER ? <Spinner /> : <ClipboardCopyIcon aria-hidden />}
				Copy reader URL
			</Button>
		</div>
	);
}
