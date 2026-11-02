import { describe, expect, mock, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
	VolumeBackupView,
	VolumeFileEntry,
	VolumeFileListing,
} from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { FilesDialog } from "./files-dialog";

const LIST = "GET /volume-backups/:backupId/files";

function backup(over: Partial<VolumeBackupView> = {}): VolumeBackupView {
	return {
		id: "b1",
		serviceId: "svc",
		destinationId: "d1",
		status: "succeeded",
		trigger: "manual",
		mode: "freeze",
		createdAt: "2026-01-12T03:30:00Z",
		freezeMs: 0,
		stageMs: 0,
		sizeBytes: 10,
		bytesAdded: 10,
		files: 1,
		locked: false,
		objectDeleted: false,
		restorable: true,
		volumes: ["data"],
		warnings: null,
		...over,
	};
}

function listing(
	entries: VolumeFileEntry[] | null,
	over: Partial<VolumeFileListing> = {},
): VolumeFileListing {
	return {
		backupId: "b1",
		volume: "data",
		path: "/",
		truncated: false,
		entries,
		...over,
	};
}

const file = (name: string, size = 100, extra = {}): VolumeFileEntry => ({
	name,
	size,
	type: "file",
	...extra,
});
const dir = (name: string): VolumeFileEntry => ({
	name,
	size: 0,
	type: "dir",
});

/** The entries of the open folder, in display order. */
function names(): string[] {
	const list = screen.getByRole("list", { name: "Files" });
	return within(list)
		.getAllByRole("listitem")
		.map((li) => li.querySelector("button, span[title]")?.textContent ?? "");
}

describe("FilesDialog", () => {
	test("renders nothing and requests nothing without a backup", async () => {
		const api = stubApi({});
		await renderWithProviders(<FilesDialog backup={null} onClose={() => {}} />);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	test("lists the root of the first volume: folders first, sizes, modes, and the request it made", async () => {
		const api = stubApi({
			[LIST]: {
				body: listing([
					file("b.txt", 2048),
					dir("zeta"),
					file("A.log", 12, {
						modifiedAt: "2026-01-10T08:00:00Z",
						mode: "0644",
					}),
					dir("alpha"),
				]),
			},
		});
		await renderWithProviders(
			<FilesDialog backup={backup()} onClose={() => {}} />,
		);
		const dialog = await screen.findByRole("dialog", { name: "Browse files" });
		expect(dialog).toHaveTextContent(
			"Backup of Jan 12, 03:30 (2026-01-12 03:30:00 UTC). Files are read from the stored backup; nothing is restored.",
		);
		expect(await screen.findByRole("list", { name: "Files" })).toBeVisible();
		expect(names()).toEqual(["alpha", "zeta", "A.log", "b.txt"]);
		expect(screen.getByText("2 KiB")).toBeInTheDocument();
		expect(screen.getByText("12 B")).toBeInTheDocument();
		// modified time is shown for entries that have one
		expect(screen.getByText("2026-01-10 08:00:00 UTC")).toHaveAttribute(
			"title",
			"0644",
		);
		expect(api.calls).toHaveLength(1);
		expect(api.calls[0]?.path).toBe("/volume-backups/b1/files");
		expect(api.calls[0]?.query.get("volume")).toBe("data");
		expect(api.calls[0]?.query.get("path")).toBe("/");
		expect(api.unmatched).toEqual([]);
	});

	test("shows placeholders while the listing loads", async () => {
		const gate = Promise.withResolvers<Response>();
		stubApi({ [LIST]: () => gate.promise });
		await renderWithProviders(
			<FilesDialog backup={backup()} onClose={() => {}} />,
		);
		await screen.findByRole("dialog");
		expect(screen.queryByRole("list", { name: "Files" })).toBeNull();
		expect(screen.queryByText("This folder is empty.")).toBeNull();
		gate.resolve(Response.json(listing([file("x")])));
		expect(await screen.findByRole("list", { name: "Files" })).toBeVisible();
	});

	test("files download as they are, folders as a .zip, through the raw download route", async () => {
		stubApi({
			[LIST]: { body: listing([dir("sub dir"), file("my file.txt")]) },
		});
		await renderWithProviders(
			<FilesDialog backup={backup()} onClose={() => {}} />,
		);
		const fileLink = await screen.findByRole("link", {
			name: "Download my file.txt",
		});
		expect(fileLink).toHaveAttribute(
			"href",
			"/api/v1/volume-backups/b1/files/content?volume=data&path=%2Fmy+file.txt",
		);
		expect(fileLink).toHaveAttribute("download");
		const dirLink = screen.getByRole("link", {
			name: "Download sub dir as .zip",
		});
		expect(dirLink).toHaveAttribute(
			"href",
			"/api/v1/volume-backups/b1/files/content?volume=data&path=%2Fsub+dir&archive=true",
		);
		expect(
			screen.getByRole("link", { name: "Download folder as .zip" }),
		).toHaveAttribute(
			"href",
			"/api/v1/volume-backups/b1/files/content?volume=data&path=%2F&archive=true",
		);
	});

	test("links and special files are listed but cannot be downloaded", async () => {
		stubApi({
			[LIST]: {
				body: listing([
					{ name: "current", size: 0, type: "link" },
					{ name: "sock", size: 0, type: "other" },
					file("real.txt"),
				]),
			},
		});
		await renderWithProviders(
			<FilesDialog backup={backup()} onClose={() => {}} />,
		);
		const items = within(
			await screen.findByRole("list", { name: "Files" }),
		).getAllByRole("listitem");
		expect(items).toHaveLength(3);
		const linkItem = items.find((li) => li.textContent?.includes("current"));
		expect(linkItem).toHaveTextContent("link");
		expect(within(linkItem as HTMLElement).queryByRole("link")).toBeNull();
		const sockItem = items.find((li) => li.textContent?.includes("sock"));
		expect(within(sockItem as HTMLElement).queryByRole("link")).toBeNull();
		// only the file and the whole-folder archive are downloadable
		expect(
			screen
				.getAllByRole("link")
				.map((a) => a.getAttribute("aria-label") ?? a.textContent),
		).toEqual(["Download folder as .zip", "Download real.txt"]);
		// sizes are only given for regular files
		expect(screen.getByText("100 B")).toBeInTheDocument();
		expect(screen.queryByText("0 B")).toBeNull();
	});

	test("opens a folder, lists it and navigates back through the breadcrumbs", async () => {
		const api = stubApi({
			[LIST]: (req) => {
				const path = new URL(req.url).searchParams.get("path");
				if (path === "/") return Response.json(listing([dir("a")]));
				if (path === "/a") return Response.json(listing([dir("b")]));
				if (path === "/a/b") return Response.json(listing([file("deep.txt")]));
				return Response.json(listing([]));
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<FilesDialog backup={backup()} onClose={() => {}} />,
		);
		await user.click(await screen.findByRole("button", { name: "a" }));
		await user.click(await screen.findByRole("button", { name: "b" }));
		expect(await screen.findByText("deep.txt")).toBeInTheDocument();

		const nav = screen.getByRole("navigation", { name: "Folder" });
		expect(within(nav).getByRole("button", { name: "data" })).toBeVisible();
		expect(within(nav).getByRole("button", { name: "a" })).toBeVisible();
		// the current folder is plain text, not a link back to itself
		expect(within(nav).getByText("b")).toHaveAttribute("aria-current", "page");
		expect(within(nav).queryByRole("button", { name: "b" })).toBeNull();
		expect(
			screen.getByRole("link", { name: "Download folder as .zip" }),
		).toHaveAttribute(
			"href",
			expect.stringContaining("path=%2Fa%2Fb&archive=true"),
		);
		expect(
			screen.getByRole("link", { name: "Download deep.txt" }),
		).toHaveAttribute(
			"href",
			expect.stringContaining("path=%2Fa%2Fb%2Fdeep.txt"),
		);

		await user.click(within(nav).getByRole("button", { name: "a" }));
		expect(await screen.findByRole("button", { name: "b" })).toBeVisible();
		expect(screen.queryByText("deep.txt")).toBeNull();

		await user.click(within(nav).getByRole("button", { name: "data" }));
		expect(await screen.findByRole("button", { name: "a" })).toBeVisible();
		expect(api.calls.map((c) => c.query.get("path"))).toEqual([
			"/",
			"/a",
			"/a/b",
			"/a",
			"/",
		]);
		expect(api.unmatched).toEqual([]);
	});

	test("an empty folder says so", async () => {
		stubApi({ [LIST]: { body: listing([]) } });
		await renderWithProviders(
			<FilesDialog backup={backup()} onClose={() => {}} />,
		);
		expect(await screen.findByText("This folder is empty.")).toBeVisible();
		expect(screen.queryByRole("list", { name: "Files" })).toBeNull();
	});

	test("a folder the API reports as null entries is empty", async () => {
		stubApi({ [LIST]: { body: listing(null) } });
		await renderWithProviders(
			<FilesDialog backup={backup()} onClose={() => {}} />,
		);
		expect(await screen.findByText("This folder is empty.")).toBeVisible();
	});

	test("a cut-short listing warns how many entries are shown", async () => {
		stubApi({
			[LIST]: {
				body: listing([file("a"), file("b"), file("c")], { truncated: true }),
			},
		});
		await renderWithProviders(
			<FilesDialog backup={backup()} onClose={() => {}} />,
		);
		expect(
			await screen.findByText(
				"Showing the first 3 entries of this folder. Download it as a .zip to get everything.",
			),
		).toBeVisible();
	});

	test("a complete listing has no truncation notice", async () => {
		stubApi({ [LIST]: { body: listing([file("a")]) } });
		await renderWithProviders(
			<FilesDialog backup={backup()} onClose={() => {}} />,
		);
		await screen.findByText("a");
		expect(screen.queryByText(/Showing the first/)).toBeNull();
	});

	test("a failed listing shows the reason and offers no downloads", async () => {
		stubApi({
			[LIST]: () =>
				Response.json(
					{
						status: 404,
						title: "Not found",
						code: "snapshot_missing",
						detail: "The snapshot no longer exists",
					},
					{ status: 404 },
				),
		});
		await renderWithProviders(
			<FilesDialog backup={backup()} onClose={() => {}} />,
		);
		expect(await screen.findByText("The files did not load")).toBeVisible();
		expect(screen.getByText("The snapshot no longer exists")).toBeVisible();
		expect(screen.queryByRole("link")).toBeNull();
		expect(screen.queryByRole("list", { name: "Files" })).toBeNull();
		expect(screen.queryByText("The server is offline")).toBeNull();
	});

	test("an offline server is explained, not reported as a failure", async () => {
		stubApi({
			[LIST]: () =>
				Response.json(
					{
						status: 503,
						title: "Unavailable",
						code: "server_offline",
						detail: "agent unreachable",
					},
					{ status: 503 },
				),
		});
		await renderWithProviders(
			<FilesDialog backup={backup()} onClose={() => {}} />,
		);
		expect(await screen.findByText("The server is offline")).toBeVisible();
		expect(screen.getByText(/can not be listed or downloaded/)).toBeVisible();
		expect(screen.queryByText("The files did not load")).toBeNull();
		expect(screen.queryByText("agent unreachable")).toBeNull();
		expect(screen.queryByRole("link")).toBeNull();
	});

	test("a backup with several volumes offers a volume picker and starts at the first", async () => {
		const api = stubApi({
			[LIST]: (req) => {
				const url = new URL(req.url);
				return Response.json(
					listing(
						url.searchParams.get("volume") === "data"
							? [file("in-data.txt")]
							: [file("in-uploads.txt")],
					),
				);
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<FilesDialog
				backup={backup({ volumes: ["data", "uploads"] })}
				onClose={() => {}}
			/>,
		);
		expect(await screen.findByText("in-data.txt")).toBeVisible();
		const picker = screen.getByRole("combobox", { name: "Volume" });
		expect(picker).toHaveTextContent("data");
		await user.click(picker);
		await user.click(await screen.findByRole("option", { name: "uploads" }));
		expect(await screen.findByText("in-uploads.txt")).toBeVisible();
		expect(screen.queryByText("in-data.txt")).toBeNull();
		expect(api.calls.at(-1)?.query.get("volume")).toBe("uploads");
		expect(
			within(screen.getByRole("navigation", { name: "Folder" })).getByText(
				"uploads",
			),
		).toBeVisible();
	});

	test("switching volume goes back to the root", async () => {
		const api = stubApi({
			[LIST]: (req) => {
				const url = new URL(req.url);
				return Response.json(
					listing(
						url.searchParams.get("path") === "/" ? [dir("sub")] : [file("f")],
					),
				);
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<FilesDialog
				backup={backup({ volumes: ["data", "uploads"] })}
				onClose={() => {}}
			/>,
		);
		await user.click(await screen.findByRole("button", { name: "sub" }));
		await screen.findByText("f");
		await user.click(screen.getByRole("combobox", { name: "Volume" }));
		await user.click(await screen.findByRole("option", { name: "uploads" }));
		await screen.findByRole("button", { name: "sub" });
		const last = api.calls.at(-1);
		expect(last?.query.get("volume")).toBe("uploads");
		expect(last?.query.get("path")).toBe("/");
	});

	test("a single-volume backup has no volume picker", async () => {
		stubApi({ [LIST]: { body: listing([file("x")]) } });
		await renderWithProviders(
			<FilesDialog backup={backup()} onClose={() => {}} />,
		);
		await screen.findByText("x");
		expect(screen.queryByRole("combobox")).toBeNull();
	});

	test("a backup without volumes lists nothing and makes no request", async () => {
		const api = stubApi({});
		await renderWithProviders(
			<FilesDialog backup={backup({ volumes: null })} onClose={() => {}} />,
		);
		expect(await screen.findByText("This folder is empty.")).toBeVisible();
		expect(screen.queryByRole("link")).toBeNull();
		expect(api.calls).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});

	test("Close calls onClose", async () => {
		stubApi({ [LIST]: { body: listing([]) } });
		const onClose = mock();
		const user = userEvent.setup();
		await renderWithProviders(
			<FilesDialog backup={backup()} onClose={onClose} />,
		);
		await screen.findByText("This folder is empty.");
		await user.click(screen.getByRole("button", { name: "Close" }));
		expect(onClose).toHaveBeenCalled();
	});

	test("a new backup opens at the root of its own first volume", async () => {
		const api = stubApi({
			[LIST]: (req) =>
				Response.json(
					listing(
						new URL(req.url).searchParams.get("path") === "/"
							? [dir("sub")]
							: [file("inner")],
					),
				),
		});
		const user = userEvent.setup();
		const view = await renderWithProviders(
			<FilesDialog backup={backup()} onClose={() => {}} />,
		);
		await user.click(await screen.findByRole("button", { name: "sub" }));
		await screen.findByText("inner");
		view.unmount();
		await renderWithProviders(
			<FilesDialog
				backup={backup({ id: "b2", volumes: ["media"] })}
				onClose={() => {}}
			/>,
		);
		await screen.findByRole("button", { name: "sub" });
		const last = api.calls.at(-1);
		expect(last?.path).toBe("/volume-backups/b2/files");
		expect(last?.query.get("volume")).toBe("media");
		expect(last?.query.get("path")).toBe("/");
	});
});
