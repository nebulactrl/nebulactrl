import { useQuery } from "@tanstack/react-query";
import { FileTextIcon, TrashIcon, UploadIcon } from "lucide-react";
import { type DragEvent, useId, useState } from "react";
import { listGitConnectionsOptions } from "#/api/gen/@tanstack/react-query.gen";
import { Alert, AlertDescription } from "#/components/ui/alert";
import { Button } from "#/components/ui/button";
import { DisclosureChevron, DisclosurePanel } from "#/components/ui/disclosure";
import { Field, FieldDescription, FieldLabel } from "#/components/ui/field";
import { Input } from "#/components/ui/input";
import { Segmented } from "#/components/ui/segmented";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "#/components/ui/tabs";
import { Textarea } from "#/components/ui/textarea";
import {
	RepoPicker,
	type RepoSelection,
	usableConnections,
} from "#/features/git/repo-picker";
import {
	classifyUpload,
	formatBytes,
	type GitDraft,
	type GitSourceMode,
	type ImportMode,
	type SourceDraft,
	type SourceMode,
	type UploadedFile,
	uploadPath,
} from "./lib";

/** The API accepts up to 256 KB per text field. */
const MAX_BYTES = 262144;

const CODE_AREA =
	"min-h-48 max-h-[340px] overflow-y-auto overscroll-contain font-mono text-xs leading-[1.5] whitespace-pre";

function PasteTab({
	draft,
	onChange,
}: {
	draft: SourceDraft;
	onChange(next: SourceDraft): void;
}) {
	const id = useId();
	const [envOpen, setEnvOpen] = useState(draft.env !== "");
	return (
		<div className="flex flex-col gap-4">
			<Field>
				<FieldLabel htmlFor={`${id}-compose`}>docker-compose.yml</FieldLabel>
				<Textarea
					id={`${id}-compose`}
					autoFocus
					spellCheck={false}
					autoComplete="off"
					placeholder={"services:\n  web:\n    image: nginx:1.27"}
					value={draft.compose}
					onChange={(event) =>
						onChange({
							...draft,
							compose: event.target.value,
							composeName: "",
						})
					}
					className={`${CODE_AREA} min-h-64`}
				/>
			</Field>
			<div className="flex flex-col">
				<button
					type="button"
					aria-expanded={envOpen}
					aria-controls={`${id}-env`}
					onClick={() => setEnvOpen(!envOpen)}
					className="flex w-fit cursor-pointer items-center gap-1.5 rounded-sm text-[12px] text-ink-2 outline-none hover:text-ink-1 focus-visible:focus-ring"
				>
					<DisclosureChevron open={envOpen} className="size-3.5" />
					Environment file (.env)
					{draft.env.trim() !== "" ? (
						<span className="font-mono text-2xs text-ink-3">added</span>
					) : null}
				</button>
				<DisclosurePanel open={envOpen} id={`${id}-env`}>
					<Field className="pt-2">
						<Textarea
							aria-label="Environment file (.env)"
							spellCheck={false}
							autoComplete="off"
							placeholder={"TAG=1.4\nPOSTGRES_PASSWORD=change-me"}
							value={draft.env}
							onChange={(event) =>
								onChange({ ...draft, env: event.target.value, envName: "" })
							}
							className={`${CODE_AREA} min-h-24`}
						/>
						<FieldDescription>
							Fills the ${"{VAR}"} references in the Compose file. Values stay
							in this request and are not stored.
						</FieldDescription>
					</Field>
				</DisclosurePanel>
			</div>
		</div>
	);
}

function UploadedRow({
	icon,
	title,
	meta,
	onRemove,
	children,
}: {
	icon?: boolean;
	title: string;
	meta: string;
	onRemove(): void;
	children?: React.ReactNode;
}) {
	return (
		<li className="surface-well flex flex-col gap-2 rounded-ctl border border-line px-3 py-2">
			<div className="flex items-center gap-2.5">
				{icon === false ? null : (
					<FileTextIcon aria-hidden className="size-4 shrink-0 text-ink-3" />
				)}
				<span className="min-w-0 flex-1 truncate font-mono text-[12px] text-ink-1">
					{title}
				</span>
				<span className="shrink-0 font-mono text-2xs text-ink-3 tabular-nums">
					{meta}
				</span>
				<Button
					variant="ghost"
					size="icon-sm"
					aria-label={`Remove ${title}`}
					onClick={onRemove}
				>
					<TrashIcon aria-hidden />
				</Button>
			</div>
			{children}
		</li>
	);
}

function UploadTab({
	draft,
	onChange,
	knownPaths,
}: {
	draft: SourceDraft;
	onChange(next: SourceDraft): void;
	knownPaths: readonly string[];
}) {
	const id = useId();
	const [dragging, setDragging] = useState(false);
	const [problem, setProblem] = useState<string | null>(null);

	const take = async (list: FileList | readonly File[]) => {
		let next = draft;
		const refused: string[] = [];
		for (const file of Array.from(list)) {
			if (file.size > MAX_BYTES) {
				refused.push(file.name);
				continue;
			}
			const content = await file.text();
			const role = classifyUpload(file.name, next.compose !== "");
			if (role === "compose") {
				next = { ...next, compose: content, composeName: file.name };
			} else if (role === "env") {
				next = { ...next, env: content, envName: file.name };
			} else {
				const upload: UploadedFile = {
					id: crypto.randomUUID(),
					name: file.name,
					path: `./${file.name}`,
					pathEdited: false,
					content,
				};
				next = {
					...next,
					uploads: [
						...next.uploads.filter((u) => u.name !== file.name),
						upload,
					],
				};
			}
		}
		setProblem(
			refused.length > 0
				? `${refused.join(", ")} ${refused.length === 1 ? "is" : "are"} larger than 256 KB and was not added.`
				: null,
		);
		onChange(next);
	};

	const onDrop = (event: DragEvent) => {
		event.preventDefault();
		setDragging(false);
		void take(event.dataTransfer.files);
	};

	const hasFiles =
		draft.composeName !== "" ||
		draft.envName !== "" ||
		draft.uploads.length > 0;

	return (
		<div className="flex flex-col gap-4">
			{/* biome-ignore lint/a11y/noStaticElementInteractions: the drop target for files; the label inside is the keyboard path to the same picker. */}
			<div
				onDragOver={(event) => {
					event.preventDefault();
					setDragging(true);
				}}
				onDragLeave={() => setDragging(false)}
				onDrop={onDrop}
				className={`surface-well flex flex-col items-center gap-2 rounded-card border border-line-control border-dashed px-4 py-8 text-center transition-[border-color,background-color] duration-quick ease-out ${dragging ? "border-accent-ink bg-accent-wash" : ""}`}
			>
				<UploadIcon aria-hidden className="size-5 text-ink-3" />
				<p className="m-0 text-[13px] text-ink-1">
					Drop docker-compose.yml here
				</p>
				<p className="m-0 max-w-[46ch] text-[12px] text-ink-3 leading-[1.5]">
					Add a .env file and any files the Compose file uses, such as env files
					and config files, in the same drop.
				</p>
				<label
					htmlFor={`${id}-files`}
					className="inline-flex h-8 cursor-pointer items-center rounded-ctl border border-line-control px-3 font-medium text-[12px] text-ink-1 outline-none transition-colors duration-quick hover:bg-white/5 has-[:focus-visible]:focus-ring"
				>
					Choose files
					<input
						id={`${id}-files`}
						type="file"
						multiple
						className="sr-only"
						onChange={(event) => {
							if (event.target.files) void take(event.target.files);
							event.target.value = "";
						}}
					/>
				</label>
			</div>
			{problem ? (
				<Alert variant="warning">
					<AlertDescription>{problem}</AlertDescription>
				</Alert>
			) : null}
			{hasFiles ? (
				<ul className="m-0 flex list-none flex-col gap-2 p-0">
					{draft.composeName !== "" ? (
						<UploadedRow
							title={draft.composeName}
							meta={`Compose file · ${formatBytes(draft.compose.length)}`}
							onRemove={() =>
								onChange({ ...draft, compose: "", composeName: "" })
							}
						/>
					) : null}
					{draft.envName !== "" ? (
						<UploadedRow
							title={draft.envName}
							meta={`Environment · ${formatBytes(draft.env.length)}`}
							onRemove={() => onChange({ ...draft, env: "", envName: "" })}
						/>
					) : null}
					{draft.uploads.map((upload) => (
						<UploadedRow
							key={upload.id}
							title={upload.name}
							meta={formatBytes(upload.content.length)}
							onRemove={() =>
								onChange({
									...draft,
									uploads: draft.uploads.filter((u) => u.id !== upload.id),
								})
							}
						>
							<Field className="gap-1">
								<FieldLabel
									htmlFor={`${id}-${upload.id}`}
									className="text-2xs text-ink-3"
								>
									Path in the Compose file
								</FieldLabel>
								<Input
									id={`${id}-${upload.id}`}
									spellCheck={false}
									autoComplete="off"
									className="h-7 font-mono text-[12px]"
									value={uploadPath(upload, knownPaths)}
									onChange={(event) =>
										onChange({
											...draft,
											uploads: draft.uploads.map((u) =>
												u.id === upload.id
													? { ...u, path: event.target.value, pathEdited: true }
													: u,
											),
										})
									}
								/>
							</Field>
						</UploadedRow>
					))}
				</ul>
			) : null}
			{draft.compose !== "" && draft.composeName === "" ? (
				<p className="m-0 text-[12px] text-ink-3">
					A Compose file is already in the Paste tab and will be used.
				</p>
			) : null}
		</div>
	);
}

function GitTab({
	draft,
	onChange,
}: {
	draft: SourceDraft;
	onChange(next: SourceDraft): void;
}) {
	const id = useId();
	const connections = useQuery(listGitConnectionsOptions());
	const items = connections.data?.items ?? [];
	const hasAccounts = usableConnections(items).length > 0;
	const git = draft.git;
	const mode: GitSourceMode = hasAccounts ? git.mode : "url";
	const set = (patch: Partial<GitDraft>) =>
		onChange({ ...draft, git: { ...git, ...patch } });

	const selection: RepoSelection | null =
		git.connectionId === ""
			? null
			: {
					connectionId: git.connectionId,
					repo: git.repo,
					branch: git.branch,
					cloneUrl: null,
				};

	return (
		<div className="flex flex-col gap-4">
			{hasAccounts ? (
				<Segmented<GitSourceMode>
					aria-label="Where the repository comes from"
					font="sans"
					density="regular"
					value={mode}
					onValueChange={(next) => set({ mode: next })}
					options={[
						{ value: "account", label: "Connected account" },
						{ value: "url", label: "Public repository URL" },
					]}
				/>
			) : null}
			{mode === "account" ? (
				<RepoPicker
					idPrefix={`${id}-compose-repo`}
					connections={items}
					value={selection}
					onChange={(next) =>
						set(
							next
								? {
										connectionId: next.connectionId,
										repo: next.repo,
										branch: next.branch,
									}
								: { connectionId: "", repo: "", branch: "" },
						)
					}
				/>
			) : (
				<>
					<Field>
						<FieldLabel htmlFor={`${id}-url`}>Repository URL</FieldLabel>
						<Input
							id={`${id}-url`}
							autoFocus
							spellCheck={false}
							autoComplete="off"
							className="font-mono"
							placeholder="https://github.com/acme/shop"
							value={git.url}
							onChange={(event) => set({ url: event.target.value })}
						/>
						<FieldDescription>
							Public repositories on github.com or gitlab.com.
							{hasAccounts
								? ""
								: " Connect a git account in Team settings → Integrations to read private ones."}
						</FieldDescription>
					</Field>
					<Field>
						<FieldLabel htmlFor={`${id}-branch`}>Branch</FieldLabel>
						<Input
							id={`${id}-branch`}
							spellCheck={false}
							autoComplete="off"
							className="font-mono"
							placeholder="Default branch"
							value={git.branch}
							onChange={(event) => set({ branch: event.target.value })}
						/>
					</Field>
				</>
			)}
			<Field>
				<FieldLabel htmlFor={`${id}-path`}>Compose file path</FieldLabel>
				<Input
					id={`${id}-path`}
					spellCheck={false}
					autoComplete="off"
					className="font-mono"
					placeholder="docker-compose.yml"
					value={git.path}
					onChange={(event) => set({ path: event.target.value })}
				/>
				<FieldDescription>
					Files the Compose file references are read from the same repository.
				</FieldDescription>
			</Field>
		</div>
	);
}

/** Step one: where the Compose file comes from. */
export function SourceStep({
	draft,
	onChange,
	knownPaths,
}: {
	draft: SourceDraft;
	onChange(next: SourceDraft): void;
	knownPaths: readonly string[];
}) {
	return (
		<div className="flex flex-col gap-4">
			<div className="flex flex-col gap-1.5">
				<Segmented<ImportMode>
					aria-label="What the Compose file becomes"
					font="sans"
					density="regular"
					value={draft.importMode}
					onValueChange={(next) => onChange({ ...draft, importMode: next })}
					options={[
						{ value: "split", label: "Split into services" },
						{ value: "stack", label: "Run as stack" },
					]}
				/>
				<p className="m-0 text-[12px] text-ink-3 leading-[1.5]">
					{draft.importMode === "stack"
						? "The file runs as it is with docker compose, as one service on one server. It needs the Docker Compose plugin there."
						: "Each Compose service becomes its own service, with normal deploys, rollbacks, variables and domains."}
				</p>
			</div>
			<Tabs
				value={draft.mode}
				onValueChange={(next) =>
					onChange({ ...draft, mode: next as SourceMode })
				}
				className="gap-4"
			>
				<TabsList
					variant="pressed"
					aria-label="Where the Compose file comes from"
				>
					<TabsTrigger value="paste">Paste</TabsTrigger>
					<TabsTrigger value="upload">Upload</TabsTrigger>
					<TabsTrigger value="git">From git</TabsTrigger>
				</TabsList>
				<TabsContent value="paste">
					<PasteTab draft={draft} onChange={onChange} />
				</TabsContent>
				<TabsContent value="upload">
					<UploadTab
						draft={draft}
						onChange={onChange}
						knownPaths={knownPaths}
					/>
				</TabsContent>
				<TabsContent value="git">
					<GitTab draft={draft} onChange={onChange} />
				</TabsContent>
			</Tabs>
		</div>
	);
}
