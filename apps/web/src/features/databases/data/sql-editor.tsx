import { defaultKeymap, history, historyKeymap } from "@codemirror/commands";
import {
	MariaSQL,
	MySQL,
	PostgreSQL,
	type SQLDialect,
	StandardSQL,
	sql,
} from "@codemirror/lang-sql";
import {
	bracketMatching,
	defaultHighlightStyle,
	syntaxHighlighting,
} from "@codemirror/language";
import { Compartment, EditorState, Prec } from "@codemirror/state";
import { EditorView, keymap, placeholder } from "@codemirror/view";
import { useEffect, useRef } from "react";
import type { SqlDialectName } from "./model";

const DIALECT: Record<SqlDialectName, SQLDialect> = {
	postgres: PostgreSQL,
	mysql: MySQL,
	mariadb: MariaSQL,
	standard: StandardSQL,
};

/** The editor's look: the console's tokens on the engraved well of an input. */
const theme = EditorView.theme(
	{
		"&": {
			color: "var(--color-ink-1)",
			backgroundColor: "transparent",
			fontSize: "13px",
		},
		"&.cm-focused": { outline: "none" },
		".cm-scroller": {
			fontFamily: "var(--font-mono)",
			lineHeight: "1.55",
			minHeight: "120px",
			maxHeight: "320px",
		},
		".cm-content": { caretColor: "var(--color-ink-1)", padding: "8px 0" },
		".cm-line": { padding: "0 10px" },
		".cm-cursor": { borderLeftColor: "var(--color-ink-1)" },
		"&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection":
			{ backgroundColor: "var(--color-accent-wash)" },
		".cm-placeholder": { color: "var(--color-ink-4)" },
		".cm-matchingBracket": {
			backgroundColor: "var(--color-accent-wash)",
			outline: "none",
		},
	},
	{ dark: true },
);

export interface SqlEditorProps {
	value: string;
	onChange(value: string): void;
	/** ⌘↵ / Ctrl+↵ in the editor. */
	onRun(): void;
	dialect: SqlDialectName;
	/** Names the editor for assistive tech. */
	label: string;
}

/**
 * A CodeMirror SQL editor: syntax colouring for the engine's dialect, history,
 * and ⌘↵ to run. Controlled by `value`; CodeMirror owns the document, so the
 * prop is written into it only when it differs (a clear after a run).
 */
export function SqlEditor({
	value,
	onChange,
	onRun,
	dialect,
	label,
}: SqlEditorProps) {
	const host = useRef<HTMLDivElement>(null);
	const view = useRef<EditorView | null>(null);
	const language = useRef(new Compartment());
	// The latest callbacks, read by the editor's long-lived extensions.
	const handlers = useRef({ onChange, onRun });
	handlers.current = { onChange, onRun };
	const initial = useRef({ value, dialect });

	useEffect(() => {
		const parent = host.current;
		if (!parent) return;
		const editor = new EditorView({
			parent,
			state: EditorState.create({
				doc: initial.current.value,
				extensions: [
					history(),
					bracketMatching(),
					syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
					language.current.of(
						sql({
							dialect: DIALECT[initial.current.dialect],
							upperCaseKeywords: true,
						}),
					),
					placeholder("SELECT * FROM …"),
					EditorView.contentAttributes.of({
						"aria-label": label,
						"aria-multiline": "true",
						role: "textbox",
						spellcheck: "false",
					}),
					Prec.highest(
						keymap.of([
							{
								key: "Mod-Enter",
								run: () => {
									handlers.current.onRun();
									return true;
								},
							},
						]),
					),
					keymap.of([...defaultKeymap, ...historyKeymap]),
					EditorView.updateListener.of((update) => {
						if (update.docChanged) {
							handlers.current.onChange(update.state.doc.toString());
						}
					}),
					theme,
				],
			}),
		});
		view.current = editor;
		return () => {
			editor.destroy();
			view.current = null;
		};
	}, [label]);

	useEffect(() => {
		const editor = view.current;
		if (editor && editor.state.doc.toString() !== value) {
			editor.dispatch({
				changes: { from: 0, to: editor.state.doc.length, insert: value },
			});
		}
	}, [value]);

	useEffect(() => {
		view.current?.dispatch({
			effects: language.current.reconfigure(
				sql({ dialect: DIALECT[dialect], upperCaseKeywords: true }),
			),
		});
	}, [dialect]);

	return (
		<div
			ref={host}
			data-slot="sql-editor"
			className="field w-full min-w-0 overflow-hidden rounded-ctl"
		/>
	);
}
