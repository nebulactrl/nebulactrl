import type { ReactNode } from "react";

const tone = {
	table: "text-accent-ink",
	key: "text-ink-1",
	punct: "text-ink-4",
	string: "text-ok",
	number: "text-warn",
	ref: "text-accent-ink",
} as const;

function Tok({
	kind,
	children,
}: {
	kind: keyof typeof tone;
	children: string;
}) {
	return <span className={tone[kind]}>{children}</span>;
}

/** A TOML string, with a `${{service.VAR}}` reference picked out inside it. */
function StringValue({ text }: { text: string }) {
	const parts = text.split(/(\$\{\{[^}]*\}\})/);
	return (
		<>
			{parts.map((part, i) => (
				// biome-ignore lint/suspicious/noArrayIndexKey: a fixed line, split once
				<Tok key={i} kind={part.startsWith("${{") ? "ref" : "string"}>
					{part}
				</Tok>
			))}
		</>
	);
}

/** One line of TOML as coloured spans: a table header or a `key = value` pair. */
export function tokenize(line: string): ReactNode {
	const header = /^\[([^\]]+)\]$/.exec(line);
	if (header) {
		return (
			<>
				<Tok kind="punct">[</Tok>
				<Tok kind="table">{header[1] as string}</Tok>
				<Tok kind="punct">]</Tok>
			</>
		);
	}
	const pair = /^(\S+) = (.+)$/.exec(line);
	if (pair) {
		const value = pair[2] as string;
		return (
			<>
				<Tok kind="key">{pair[1] as string}</Tok>
				<Tok kind="punct">{" = "}</Tok>
				{value.startsWith('"') || value.startsWith("[") ? (
					<StringValue text={value} />
				) : (
					<Tok kind="number">{value}</Tok>
				)}
			</>
		);
	}
	return line;
}
