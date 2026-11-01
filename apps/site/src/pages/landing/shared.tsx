import { Fragment, type ReactNode } from "react";
import CopyButton from "../../islands/copy-button.tsx";
import { Island } from "../../islands/island.tsx";

export const container = "mx-auto max-w-[1240px] px-4 sm:px-8";
/** Space above a section of the page: 96px on a phone, 160px on a desktop. */
export const gap = "pt-24 md:pt-40";
export const link = "text-accent-ink";
/** Body copy: 16px, ink-2, at most 62 characters wide. */
export const body = "m-0 max-w-[62ch] text-base text-ink-2";

/**
 * Code in an inset well: mono, ink-2, a faint
 * border. It scrolls sideways on a phone, so the well is focusable.
 */
export function CodeWell({
	children,
	label,
}: {
	children: ReactNode;
	label: string;
}) {
	return (
		<section
			// biome-ignore lint/a11y/noNoninteractiveTabindex: a scrollable code block must be reachable by keyboard.
			tabIndex={0}
			aria-label={label}
			className="surface-well overflow-x-auto rounded-xl px-4 py-3 font-mono text-[13px] text-ink-2 leading-6 outline-none focus-visible:focus-ring"
		>
			<pre className="m-0 font-[inherit]">
				<code>{children}</code>
			</pre>
		</section>
	);
}

/**
 * The command in the places it may wrap on a phone: before the URL and before
 * the pipe, never inside them. Anything that does not look like `curl URL | sh`
 * stays whole.
 */
function breakPoints(command: string): string[] {
	const match = command.match(/^(.*?)(\S+:\/\/\S+)(.*)$/);
	if (!match) return [command];
	return [match[1], match[2], match[3]]
		.map((part) => (part ?? "").trim())
		.filter(Boolean);
}

/**
 * The command in an engraved well with the accent Copy key: the page's one primary
 * action. Without JavaScript the key is hidden and the command stays selectable text.
 */
export function InstallWell({
	command,
	id,
	compact = false,
}: {
	command: string;
	id: string;
	/** A narrow column: a smaller command from lg up, so the command and the key share one row. */
	compact?: boolean;
}) {
	return (
		<div
			className="surface-well flex flex-col items-stretch gap-2 rounded-xl p-2 pl-4 sm:flex-row sm:items-center"
			id={id}
		>
			<code
				className={`install-cmd min-w-0 flex-1 py-2 font-mono text-ink-1 text-xs max-sm:overflow-x-auto sm:text-sm ${compact ? "lg:text-xs" : ""}`}
			>
				{breakPoints(command).map((part, i) => (
					<Fragment key={part}>
						{i > 0 ? " " : null}
						<span className="whitespace-nowrap">
							{i === 0 ? (
								<span aria-hidden className="select-none text-ink-4">
									${" "}
								</span>
							) : null}
							{part}
						</span>
					</Fragment>
				))}
			</code>
			<div className="js-only shrink-0 max-sm:self-end sm:ml-auto">
				<Island name="copy" mode="visible" props={{ text: command, compact }}>
					<CopyButton text={command} compact={compact} />
				</Island>
			</div>
		</div>
	);
}

/** The footer's engraved chips: version, price and architectures. */
export function FactChips({ version }: { version: string | null }) {
	const chips = [
		...(version ? [version] : []),
		"Free to use",
		"amd64 and arm64",
	];
	return (
		<ul
			className="m-0 flex list-none flex-wrap gap-1.5 p-0"
			aria-label="Version, price and supported systems"
		>
			{chips.map((chip) => (
				<li key={chip} className="chip">
					{chip}
				</li>
			))}
		</ul>
	);
}
