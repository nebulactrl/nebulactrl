import { describe, expect, test } from "bun:test";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { useState } from "react";
import { makeService } from "#/features/canvas/lib/fixtures";
import { renderWithProviders } from "#/test/render";
import { type SettingsDraft, toDraft } from "./draft";
import type { DraftErrors } from "./fields";
import { type FileDraft, MAX_FILE_BYTES, MAX_FILES } from "./files";
import { FilesSection } from "./files-section";

function file(over: Partial<FileDraft> = {}): FileDraft {
	return {
		path: "/etc/app/config.toml",
		mode: "",
		content: "a = 1\n",
		...over,
	};
}

function draftWith(files: FileDraft[]): SettingsDraft {
	return { ...toDraft(makeService()), files };
}

type FilesSeen = { files: FileDraft[]; sets: string[] };

function FilesHarness({
	files,
	errors,
	seen,
}: {
	files: FileDraft[];
	errors: DraftErrors;
	seen: FilesSeen;
}) {
	const [draft, setDraft] = useState(draftWith(files));
	seen.files = draft.files;
	return (
		<FilesSection
			draft={draft}
			errors={errors}
			set={(key, value) => {
				seen.sets.push(key);
				setDraft((current) => ({ ...current, [key]: value }));
			}}
		/>
	);
}

/** Renders the section over a stateful draft and exposes the latest files. */
function mount(files: FileDraft[], errors: DraftErrors = {}) {
	const seen: FilesSeen = { files, sets: [] };
	return {
		seen,
		ui: <FilesHarness files={files} errors={errors} seen={seen} />,
	};
}

async function openAdd(user: UserEvent) {
	await user.click(screen.getByRole("button", { name: "Add file" }));
	return screen.findByRole("dialog");
}

describe("FilesSection list", () => {
	test("says there are no files and counts 0/16", async () => {
		const { ui } = mount([]);
		await renderWithProviders(ui);
		expect(
			screen.getByText(
				"No files. Add one to mount a config file at a fixed path.",
			),
		).toBeInTheDocument();
		expect(screen.getByText(`0/${MAX_FILES}`)).toBeInTheDocument();
		expect(screen.queryByRole("list")).toBeNull();
	});

	test("lists each file with its path, mode (default 0644) and size", async () => {
		const { ui } = mount([
			file({ path: "/etc/a.conf", mode: "0600", content: "x".repeat(2048) }),
			file({ path: "/etc/b.conf", mode: "", content: "héllo" }),
		]);
		await renderWithProviders(ui);
		const rows = within(screen.getByRole("list")).getAllByRole("listitem");
		expect(rows).toHaveLength(2);
		expect(rows[0]).toHaveTextContent("/etc/a.conf");
		expect(rows[0]).toHaveTextContent("mode 0600 · 2.0 KiB");
		expect(rows[1]).toHaveTextContent("/etc/b.conf");
		// é is two UTF-8 bytes, so five characters are six bytes.
		expect(rows[1]).toHaveTextContent("mode 0644 · 6 B");
		expect(screen.getByText(`2/${MAX_FILES}`)).toBeInTheDocument();
	});

	test("shows the draft-level files error under the list", async () => {
		const { ui } = mount([file()], {
			files: "/etc/app/config.toml: Use an octal mode like 0644.",
		});
		await renderWithProviders(ui);
		expect(
			screen.getByText("/etc/app/config.toml: Use an octal mode like 0644."),
		).toBeInTheDocument();
	});

	test("removing a file drops only that file from the draft", async () => {
		const user = userEvent.setup();
		const { ui, seen } = mount([
			file({ path: "/a" }),
			file({ path: "/b" }),
			file({ path: "/c" }),
		]);
		await renderWithProviders(ui);
		await user.click(screen.getByRole("button", { name: "Remove /b" }));
		expect(seen.files.map((f) => f.path)).toEqual(["/a", "/c"]);
		expect(screen.queryByText("/b")).toBeNull();
		expect(screen.getByText(`2/${MAX_FILES}`)).toBeInTheDocument();
	});

	test("disables Add file once 16 files are staged", async () => {
		const files = Array.from({ length: MAX_FILES }, (_, i) =>
			file({ path: `/f/${i}` }),
		);
		const { ui } = mount(files);
		await renderWithProviders(ui);
		expect(screen.getByRole("button", { name: "Add file" })).toBeDisabled();
		expect(screen.getByText(`${MAX_FILES}/${MAX_FILES}`)).toBeInTheDocument();
	});

	test("keeps Add file enabled at 15 files", async () => {
		const files = Array.from({ length: MAX_FILES - 1 }, (_, i) =>
			file({ path: `/f/${i}` }),
		);
		const { ui } = mount(files);
		await renderWithProviders(ui);
		expect(screen.getByRole("button", { name: "Add file" })).toBeEnabled();
	});
});

describe("FilesSection add dialog", () => {
	test("adds a file with the path trimmed and the contents untouched", async () => {
		const user = userEvent.setup();
		const { ui, seen } = mount([]);
		await renderWithProviders(ui);
		const dialog = await openAdd(user);
		expect(within(dialog).getByText("Add a file")).toBeInTheDocument();
		expect(
			within(dialog).getByText("Stored in plain text"),
		).toBeInTheDocument();

		await user.type(
			within(dialog).getByLabelText("Path in the container"),
			"/etc/app/config.toml  ",
		);
		await user.type(within(dialog).getByLabelText("Mode"), "0600");
		fireEvent.change(within(dialog).getByLabelText("Contents"), {
			target: { value: "  key = 'v'\n" },
		});
		await user.click(within(dialog).getByRole("button", { name: "Add file" }));

		expect(seen.files).toEqual([
			{ path: "/etc/app/config.toml", mode: "0600", content: "  key = 'v'\n" },
		]);
		expect(await screen.findByText("mode 0600 · 12 B")).toBeInTheDocument();
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});

	test("appends after the existing files and leaves them as they were", async () => {
		const user = userEvent.setup();
		const first = file({ path: "/first", mode: "0640", content: "1" });
		const { ui, seen } = mount([first]);
		await renderWithProviders(ui);
		const dialog = await openAdd(user);
		await user.type(
			within(dialog).getByLabelText("Path in the container"),
			"/second",
		);
		await user.click(within(dialog).getByRole("button", { name: "Add file" }));
		expect(seen.files).toEqual([
			first,
			{ path: "/second", mode: "", content: "" },
		]);
	});

	test("an empty path is refused with a message, nothing is added and Add file locks", async () => {
		const user = userEvent.setup();
		const { ui, seen } = mount([]);
		await renderWithProviders(ui);
		const dialog = await openAdd(user);
		const submit = within(dialog).getByRole("button", { name: "Add file" });
		// Errors stay hidden until the first attempt.
		expect(within(dialog).queryByText(/clean absolute path/)).toBeNull();
		expect(submit).toBeEnabled();

		await user.click(submit);

		expect(
			within(dialog).getByText(
				"Use a clean absolute path like /etc/app/config.toml (no .. and no trailing /).",
			),
		).toBeInTheDocument();
		expect(
			within(dialog).getByLabelText("Path in the container"),
		).toHaveAttribute("aria-invalid", "true");
		expect(seen.files).toEqual([]);
		expect(submit).toBeDisabled();
		expect(screen.getByRole("dialog")).toBeInTheDocument();

		// Fixing the path unlocks the button again.
		await user.type(
			within(dialog).getByLabelText("Path in the container"),
			"/ok",
		);
		expect(submit).toBeEnabled();
	});

	test.each([
		["relative path", "etc/app.conf"],
		["root", "/"],
		["trailing slash", "/etc/app/"],
		["dot-dot segment", "/etc/../shadow"],
		["dot segment", "/etc/./app"],
		["empty segment", "/etc//app"],
	])("refuses a path that is a %s", async (_name, path) => {
		const user = userEvent.setup();
		const { ui, seen } = mount([]);
		await renderWithProviders(ui);
		const dialog = await openAdd(user);
		await user.type(
			within(dialog).getByLabelText("Path in the container"),
			path,
		);
		await user.click(within(dialog).getByRole("button", { name: "Add file" }));
		expect(
			within(dialog).getByText(/Use a clean absolute path/),
		).toBeInTheDocument();
		expect(seen.files).toEqual([]);
	});

	test("refuses a path another file already uses", async () => {
		const user = userEvent.setup();
		const { ui, seen } = mount([file({ path: "/etc/taken" })]);
		await renderWithProviders(ui);
		const dialog = await openAdd(user);
		await user.type(
			within(dialog).getByLabelText("Path in the container"),
			"/etc/taken",
		);
		await user.click(within(dialog).getByRole("button", { name: "Add file" }));
		expect(
			within(dialog).getByText("Another file already uses this path."),
		).toBeInTheDocument();
		expect(seen.files).toHaveLength(1);
	});

	test.each(["0644", "644", "0755", "7777"])(
		"accepts mode %p (3-4 octal digits, optional leading 0)",
		async (mode) => {
			const user = userEvent.setup();
			const { ui, seen } = mount([]);
			await renderWithProviders(ui);
			const dialog = await openAdd(user);
			await user.type(
				within(dialog).getByLabelText("Path in the container"),
				"/f",
			);
			await user.type(within(dialog).getByLabelText("Mode"), mode);
			await user.click(
				within(dialog).getByRole("button", { name: "Add file" }),
			);
			expect(seen.files).toEqual([{ path: "/f", mode, content: "" }]);
		},
	);

	test.each(["0888", "0abc", "12", "77777", " 0644"])(
		"refuses mode %p",
		async (mode) => {
			const user = userEvent.setup();
			const { ui, seen } = mount([]);
			await renderWithProviders(ui);
			const dialog = await openAdd(user);
			await user.type(
				within(dialog).getByLabelText("Path in the container"),
				"/f",
			);
			await user.type(within(dialog).getByLabelText("Mode"), mode);
			await user.click(
				within(dialog).getByRole("button", { name: "Add file" }),
			);
			expect(
				within(dialog).getByText("Use an octal mode like 0644."),
			).toBeInTheDocument();
			expect(within(dialog).getByLabelText("Mode")).toHaveAttribute(
				"aria-invalid",
				"true",
			);
			expect(seen.files).toEqual([]);
		},
	);

	test("accepts contents of exactly 64 KiB and shows the size", async () => {
		const user = userEvent.setup();
		const { ui, seen } = mount([]);
		await renderWithProviders(ui);
		const dialog = await openAdd(user);
		await user.type(
			within(dialog).getByLabelText("Path in the container"),
			"/big",
		);
		fireEvent.change(within(dialog).getByLabelText("Contents"), {
			target: { value: "x".repeat(MAX_FILE_BYTES) },
		});
		expect(within(dialog).getByText("64 KiB of 64 KiB")).toBeInTheDocument();
		expect(within(dialog).queryByText(/A file holds at most/)).toBeNull();
		await user.click(within(dialog).getByRole("button", { name: "Add file" }));
		expect(seen.files[0]?.content).toHaveLength(MAX_FILE_BYTES);
	});

	test("refuses contents one byte over 64 KiB straight away, before any attempt", async () => {
		const user = userEvent.setup();
		const { ui, seen } = mount([]);
		await renderWithProviders(ui);
		const dialog = await openAdd(user);
		await user.type(
			within(dialog).getByLabelText("Path in the container"),
			"/big",
		);
		fireEvent.change(within(dialog).getByLabelText("Contents"), {
			target: { value: "x".repeat(MAX_FILE_BYTES + 1) },
		});
		expect(
			within(dialog).getByText("A file holds at most 64 KiB."),
		).toBeInTheDocument();
		expect(within(dialog).getByLabelText("Contents")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		await user.click(within(dialog).getByRole("button", { name: "Add file" }));
		expect(seen.files).toEqual([]);
		expect(screen.getByRole("dialog")).toBeInTheDocument();
	});

	test("counts UTF-8 bytes, not characters, against the limit", async () => {
		const user = userEvent.setup();
		const { ui, seen } = mount([]);
		await renderWithProviders(ui);
		const dialog = await openAdd(user);
		await user.type(
			within(dialog).getByLabelText("Path in the container"),
			"/wide",
		);
		// 32769 two-byte characters = 65538 bytes, though only 32769 characters.
		fireEvent.change(within(dialog).getByLabelText("Contents"), {
			target: { value: "é".repeat(MAX_FILE_BYTES / 2 + 1) },
		});
		expect(
			within(dialog).getByText("A file holds at most 64 KiB."),
		).toBeInTheDocument();
		await user.click(within(dialog).getByRole("button", { name: "Add file" }));
		expect(seen.files).toEqual([]);
	});

	test("Cancel closes without changing the draft, and the next opening starts empty", async () => {
		const user = userEvent.setup();
		const { ui, seen } = mount([]);
		await renderWithProviders(ui);
		let dialog = await openAdd(user);
		await user.type(
			within(dialog).getByLabelText("Path in the container"),
			"/half",
		);
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await screen.findByText(/No files/);
		expect(seen.files).toEqual([]);
		expect(seen.sets).toEqual([]);

		dialog = await openAdd(user);
		expect(within(dialog).getByLabelText("Path in the container")).toHaveValue(
			"",
		);
	});
});

describe("FilesSection edit dialog", () => {
	test("opens with the file's values and replaces it in place on Update", async () => {
		const user = userEvent.setup();
		const { ui, seen } = mount([
			file({ path: "/a", content: "A" }),
			file({ path: "/b", mode: "0600", content: "B" }),
			file({ path: "/c", content: "C" }),
		]);
		await renderWithProviders(ui);
		await user.click(screen.getByRole("button", { name: "Edit /b" }));
		const dialog = await screen.findByRole("dialog");
		expect(within(dialog).getByText("Edit file")).toBeInTheDocument();
		expect(within(dialog).getByLabelText("Path in the container")).toHaveValue(
			"/b",
		);
		expect(within(dialog).getByLabelText("Mode")).toHaveValue("0600");
		expect(within(dialog).getByLabelText("Contents")).toHaveValue("B");

		await user.clear(within(dialog).getByLabelText("Mode"));
		await user.type(within(dialog).getByLabelText("Mode"), "0400");
		fireEvent.change(within(dialog).getByLabelText("Contents"), {
			target: { value: "B2" },
		});
		await user.click(
			within(dialog).getByRole("button", { name: "Update file" }),
		);

		expect(seen.files).toEqual([
			{ path: "/a", mode: "", content: "A" },
			{ path: "/b", mode: "0400", content: "B2" },
			{ path: "/c", mode: "", content: "C" },
		]);
	});

	test("a file may keep its own path, but not take another file's", async () => {
		const user = userEvent.setup();
		const { ui, seen } = mount([file({ path: "/a" }), file({ path: "/b" })]);
		await renderWithProviders(ui);
		await user.click(screen.getByRole("button", { name: "Edit /a" }));
		const dialog = await screen.findByRole("dialog");

		const path = within(dialog).getByLabelText("Path in the container");
		await user.clear(path);
		await user.type(path, "/b");
		await user.click(
			within(dialog).getByRole("button", { name: "Update file" }),
		);
		expect(
			within(dialog).getByText("Another file already uses this path."),
		).toBeInTheDocument();
		expect(seen.files.map((f) => f.path)).toEqual(["/a", "/b"]);

		await user.clear(path);
		await user.type(path, "/a");
		await user.click(
			within(dialog).getByRole("button", { name: "Update file" }),
		);
		expect(seen.files.map((f) => f.path)).toEqual(["/a", "/b"]);
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
	});

	test("renaming a file's path updates the list entry", async () => {
		const user = userEvent.setup();
		const { ui, seen } = mount([file({ path: "/old" })]);
		await renderWithProviders(ui);
		await user.click(screen.getByRole("button", { name: "Edit /old" }));
		const dialog = await screen.findByRole("dialog");
		const path = within(dialog).getByLabelText("Path in the container");
		await user.clear(path);
		await user.type(path, "/new");
		await user.click(
			within(dialog).getByRole("button", { name: "Update file" }),
		);
		expect(seen.files.map((f) => f.path)).toEqual(["/new"]);
		expect(
			await screen.findByRole("button", { name: "Edit /new" }),
		).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Edit /old" })).toBeNull();
	});
});
