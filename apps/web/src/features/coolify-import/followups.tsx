import { CopyButton } from "#/components/common/copy-button";
import { followupSegments } from "./lib";

/**
 * The manual steps after an import. The commands inside a sentence are drawn
 * as code with a copy button, so a dump or restore line can be pasted as it is.
 */
export function FollowupList({ followups }: { followups: readonly string[] }) {
	return (
		<ul className="m-0 flex list-none flex-col gap-3 p-0">
			{followups.map((text) => {
				const segments = followupSegments(text);
				return (
					<li
						key={text}
						className="flex flex-col gap-1.5 text-[12px] text-ink-2 leading-[1.55]"
					>
						{segments.map((segment, index) =>
							segment.kind === "code" ? (
								<span
									// biome-ignore lint/suspicious/noArrayIndexKey: segments are positional and never reorder
									key={index}
									className="surface-well flex items-start gap-2 rounded-ctl border border-line py-1 pr-1 pl-2.5"
								>
									<code className="min-w-0 flex-1 select-all break-all py-1 font-mono text-[12px] text-ink-1">
										{segment.text}
									</code>
									<CopyButton
										value={segment.text}
										label="Copy command"
										variant="ghost"
										size="icon-sm"
									/>
								</span>
							) : (
								// biome-ignore lint/suspicious/noArrayIndexKey: segments are positional and never reorder
								<span key={index}>{segment.text.trim()}</span>
							),
						)}
					</li>
				);
			})}
		</ul>
	);
}
