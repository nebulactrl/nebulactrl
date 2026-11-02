import { afterEach, describe, expect, test } from "bun:test";
import {
	act,
	renderHook,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { FileListing } from "#/api/gen/types.gen";
import {
	type ApiReply,
	type ApiRoutes,
	type ApiStub,
	stubApi,
} from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { FilesSheet } from "./files-sheet";
import {
	closeFiles,
	closeShell,
	openFiles,
	useShellProcess,
	useShellReplica,
	useShellTarget,
} from "./store";
import { fileEntry, listing, serviceView, terminalInfo } from "./test-support";

afterEach(() => {
	act(() => {
		closeFiles();
		closeShell();
	});
});

const root = listing([
	fileEntry("etc", { type: "dir", mode: "0755" }),
	fileEntry("app.log", {
		size: 2048,
		mode: "0644",
		modifiedAt: "2026-01-02T03:04:05Z",
	}),
	fileEntry("current", { type: "link", linkTarget: "/var/app" }),
	fileEntry("agent.sock", { type: "other" }),
]);

/** Serves listings by the requested path; unknown paths 404. */
function listings(byPath: Record<string, FileListing>): ApiReply {
	return (request) => {
		const path = new URL(request.url).searchParams.get("path") ?? "/";
		const found = byPath[path];
		return found
			? Response.json(found)
			: Response.json(
					{ title: "Not Found", detail: `No such directory: ${path}` },
					{ status: 404 },
				);
	};
}

async function openBrowser(
	routes: ApiRoutes = {},
	options?: Parameters<typeof openFiles>[1],
) {
	const api = stubApi({
		"GET /services/:serviceId": { body: serviceView() },
		"GET /services/:serviceId/terminal": { body: terminalInfo() },
		"GET /services/:serviceId/files": listings({ "/": root }),
		...routes,
	});
	const user = userEvent.setup();
	await renderWithProviders(<FilesSheet />);
	act(() => openFiles("svc-1", options));
	return { api, user };
}

const dirCalls = (api: ApiStub) =>
	api.calls.filter((c) => c.path === "/services/svc-1/files");

describe("FilesSheet listing", () => {
	test("renders nothing until opened", async () => {
		stubApi({});
		await renderWithProviders(<FilesSheet />);
		expect(screen.queryByRole("dialog")).toBeNull();
	});

	test("lists entries with their type, size, modified time and mode", async () => {
		const { api } = await openBrowser();
		const dialog = await screen.findByRole("dialog");
		expect(await within(dialog).findByText("Files · api")).toBeInTheDocument();
		expect(
			await within(dialog).findByRole("button", { name: /^etc$/ }),
		).toBeInTheDocument();

		const table = within(dialog).getByRole("table");
		const row = (name: string) =>
			within(table).getByText(name).closest("tr") as HTMLElement;
		expect(row("app.log")).toHaveTextContent("2 KiB");
		expect(row("app.log")).toHaveTextContent("rw-r--r--");
		expect(row("app.log").querySelector("time")).toHaveAttribute(
			"datetime",
			"2026-01-02T03:04:05Z",
		);
		expect(row("etc")).toHaveTextContent("rwxr-xr-x");
		expect(row("current")).toHaveTextContent("→ /var/app");
		// The first request is the root of replica 0, with no process.
		expect(Object.fromEntries(dirCalls(api)[0]?.query ?? [])).toEqual({
			replica: "0",
			path: "/",
		});
	});

	test("only regular files get a download link, to the file's content URL", async () => {
		await openBrowser();
		const link = await screen.findByRole("link", { name: "Download app.log" });
		expect(link).toHaveAttribute(
			"href",
			"/api/v1/services/svc-1/files/content?replica=0&path=%2Fapp.log",
		);
		expect(link).toHaveAttribute("download", "app.log");
		expect(screen.getAllByRole("link", { name: /^Download / })).toHaveLength(1);
	});

	test("entries that are neither files, folders nor links are plain text, not buttons", async () => {
		await openBrowser();
		await screen.findByText("agent.sock");
		expect(screen.queryByRole("button", { name: /agent\.sock/ })).toBeNull();
	});

	test("an empty folder says so", async () => {
		await openBrowser({
			"GET /services/:serviceId/files": { body: listing([]) },
		});
		expect(
			await screen.findByText("This folder is empty."),
		).toBeInTheDocument();
		expect(screen.queryByRole("table")).toBeNull();
	});

	test("a listing with null entries is an empty folder", async () => {
		await openBrowser({
			"GET /services/:serviceId/files": { body: listing(null) },
		});
		expect(
			await screen.findByText("This folder is empty."),
		).toBeInTheDocument();
	});

	test("a truncated listing says how many entries it shows", async () => {
		await openBrowser({
			"GET /services/:serviceId/files": {
				body: listing([fileEntry("a"), fileEntry("b")], { truncated: true }),
			},
		});
		expect(
			await screen.findByText("Only the first 2 entries are listed."),
		).toBeInTheDocument();
	});

	test("a folder that does not load shows the reason, and Retry loads it again", async () => {
		let calls = 0;
		const user = userEvent.setup();
		stubApi({
			"GET /services/:serviceId": { body: serviceView() },
			"GET /services/:serviceId/terminal": { body: terminalInfo() },
			"GET /services/:serviceId/files": () => {
				calls += 1;
				return calls === 1
					? Response.json(
							{ title: "Bad Gateway", detail: "The container did not answer." },
							{ status: 502 },
						)
					: Response.json(root);
			},
		});
		await renderWithProviders(<FilesSheet />);
		act(() => openFiles("svc-1"));
		expect(
			await screen.findByText("This folder did not load"),
		).toBeInTheDocument();
		expect(
			screen.getByText("The container did not answer."),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: /Retry|Try again/i }));
		expect(
			await screen.findByRole("button", { name: /^etc$/ }),
		).toBeInTheDocument();
	});

	test("shows placeholders while the folder loads", async () => {
		await openBrowser({
			"GET /services/:serviceId/files": () => new Promise<Response>(() => {}),
		});
		const dialog = await screen.findByRole("dialog");
		expect(within(dialog).getByRole("status")).toBeInTheDocument();
		expect(within(dialog).queryByRole("table")).toBeNull();
	});

	test("opens on the replica it was asked for", async () => {
		const { api } = await openBrowser({}, { replica: 2 });
		await screen.findByRole("button", { name: /^etc$/ });
		expect(dirCalls(api)[0]?.query.get("replica")).toBe("2");
	});
});

describe("FilesSheet navigation", () => {
	const tree = {
		"/": root,
		"/etc": listing([fileEntry("nginx", { type: "dir" })], { path: "/etc" }),
		"/etc/nginx": listing([fileEntry("nginx.conf")], { path: "/etc/nginx" }),
	};

	test("clicking a folder opens it and the path bar follows", async () => {
		const { api, user } = await openBrowser({
			"GET /services/:serviceId/files": listings(tree),
		});
		await user.click(await screen.findByRole("button", { name: /^etc$/ }));
		await user.click(await screen.findByRole("button", { name: /^nginx$/ }));
		expect(await screen.findByText("nginx.conf")).toBeInTheDocument();
		const path = screen.getByRole("navigation", { name: "Path" });
		expect(
			within(path)
				.getAllByRole("button")
				.map((b) => b.textContent),
		).toEqual(["/", "etc", "nginx"]);
		expect(within(path).getByRole("button", { name: "nginx" })).toHaveAttribute(
			"aria-current",
			"page",
		);
		expect(dirCalls(api).map((c) => c.query.get("path"))).toEqual([
			"/",
			"/etc",
			"/etc/nginx",
		]);
	});

	test("a path bar segment jumps back to that folder", async () => {
		const { user } = await openBrowser({
			"GET /services/:serviceId/files": listings(tree),
		});
		await user.click(await screen.findByRole("button", { name: /^etc$/ }));
		await user.click(await screen.findByRole("button", { name: /^nginx$/ }));
		await screen.findByText("nginx.conf");
		const path = screen.getByRole("navigation", { name: "Path" });
		await user.click(within(path).getByRole("button", { name: "etc" }));
		expect(
			await screen.findByRole("button", { name: /^nginx$/ }),
		).toBeInTheDocument();
		expect(screen.queryByText("nginx.conf")).toBeNull();
	});

	test("Up goes to the parent and is disabled at the root", async () => {
		const { user } = await openBrowser({
			"GET /services/:serviceId/files": listings(tree),
		});
		const up = await screen.findByRole("button", { name: "Up" });
		expect(up).toBeDisabled();
		await user.click(await screen.findByRole("button", { name: /^etc$/ }));
		await screen.findByRole("button", { name: /^nginx$/ });
		expect(up).toBeEnabled();
		await user.click(up);
		expect(
			await screen.findByRole("button", { name: /^etc$/ }),
		).toBeInTheDocument();
		await waitFor(() => expect(up).toBeDisabled());
	});

	test("following a link requests the link's path", async () => {
		const { api, user } = await openBrowser({
			"GET /services/:serviceId/files": listings({
				...tree,
				"/current": listing([fileEntry("target.txt")], { path: "/current" }),
			}),
		});
		await user.click(await screen.findByRole("button", { name: /current/ }));
		expect(await screen.findByText("target.txt")).toBeInTheDocument();
		expect(dirCalls(api).at(-1)?.query.get("path")).toBe("/current");
	});

	test("closing the panel resets it to the root", async () => {
		const { api, user } = await openBrowser({
			"GET /services/:serviceId/files": listings(tree),
		});
		await user.click(await screen.findByRole("button", { name: /^etc$/ }));
		await screen.findByRole("button", { name: /^nginx$/ });
		await user.click(screen.getByRole("button", { name: "Close" }));
		await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
		act(() => openFiles("svc-1"));
		expect(
			await screen.findByRole("button", { name: /^etc$/ }),
		).toBeInTheDocument();
		expect(dirCalls(api).at(-1)?.query.get("path")).toBe("/");
	});
});

describe("FilesSheet process and replica", () => {
	const replicas = [
		{ index: 0, process: "web", running: true, state: "running" },
		{ index: 0, process: "worker", running: true, state: "running" },
		{ index: 1, process: "worker", running: true, state: "running" },
	];

	test("a service with processes starts on the process it was opened for and lists its replicas", async () => {
		const { api } = await openBrowser(
			{
				"GET /services/:serviceId/terminal": {
					body: terminalInfo({ replicas }),
				},
			},
			{ process: "worker", replica: 1 },
		);
		expect(
			await screen.findByRole("combobox", { name: "Process" }),
		).toHaveTextContent("worker");
		expect(screen.getByRole("combobox", { name: "Replica" })).toHaveTextContent(
			"Replica 1",
		);
		await waitFor(() =>
			expect(dirCalls(api).at(-1)?.query.get("process")).toBe("worker"),
		);
		expect(dirCalls(api).at(-1)?.query.get("replica")).toBe("1");
	});

	test("switching replica lists that replica from the root", async () => {
		const { api, user } = await openBrowser(
			{
				"GET /services/:serviceId/terminal": {
					body: terminalInfo({ replicas }),
				},
			},
			{ process: "worker", replica: 0 },
		);
		const select = await screen.findByRole("combobox", { name: "Replica" });
		await user.click(select);
		await user.click(await screen.findByRole("option", { name: "Replica 1" }));
		await waitFor(() =>
			expect(dirCalls(api).at(-1)?.query.get("replica")).toBe("1"),
		);
		expect(dirCalls(api).at(-1)?.query.get("path")).toBe("/");
	});

	test("switching process lists that process's own replica from the root", async () => {
		const { api, user } = await openBrowser(
			{
				"GET /services/:serviceId/terminal": {
					body: terminalInfo({ replicas }),
				},
			},
			{ process: "worker", replica: 1 },
		);
		const select = await screen.findByRole("combobox", { name: "Process" });
		await user.click(select);
		await user.click(await screen.findByRole("option", { name: "web" }));
		await waitFor(() =>
			expect(dirCalls(api).at(-1)?.query.get("process")).toBe("web"),
		);
		expect(dirCalls(api).at(-1)?.query.get("path")).toBe("/");
		// web has no replica 1: the preselection belonged to worker.
		expect(dirCalls(api).at(-1)?.query.get("replica")).toBe("0");
	});

	test("a stack's replicas are named by component", async () => {
		await openBrowser({
			"GET /services/:serviceId": {
				body: serviceView({
					kind: "stack",
					config: {
						healthcheck: { type: "http" },
						stack: {
							components: [
								{ name: "web", index: 0 },
								{ name: "db", index: 1 },
							],
						},
					},
				}),
			},
			"GET /services/:serviceId/terminal": {
				body: terminalInfo({
					replicas: [
						{ index: 0, process: "", running: true, state: "running" },
						{ index: 1, process: "", running: true, state: "running" },
					],
				}),
			},
		});
		const select = await screen.findByRole("combobox", { name: "Component" });
		await waitFor(() => expect(select).toHaveTextContent("web"));
	});

	test("Shell opens the terminal on the same process and replica", async () => {
		const { user } = await openBrowser(
			{
				"GET /services/:serviceId/terminal": {
					body: terminalInfo({ replicas }),
				},
			},
			{ process: "worker", replica: 1 },
		);
		const shell = renderHook(() => ({
			target: useShellTarget(),
			process: useShellProcess(),
			replica: useShellReplica(),
		}));
		await screen.findByRole("combobox", { name: "Process" });
		await user.click(screen.getByRole("button", { name: "Shell" }));
		expect(shell.result.current).toEqual({
			target: "svc-1",
			process: "worker",
			replica: 1,
		});
	});
});

describe("FilesSheet preview", () => {
	const content =
		(body: BodyInit, status = 200): ApiReply =>
		() =>
			new Response(body, { status });

	test("clicking a file shows its text with the path and size, and a download link", async () => {
		const { api, user } = await openBrowser({
			"GET /services/:serviceId/files/content": content("line one\nline two\n"),
		});
		await user.click(await screen.findByRole("button", { name: /^app\.log$/ }));
		const dialog = await screen.findByRole("dialog", { name: "app.log" });
		expect(await within(dialog).findByText(/line one/)).toHaveTextContent(
			"line two",
		);
		expect(dialog).toHaveTextContent("/app.log · 2 KiB");
		const download = within(dialog).getByRole("link", { name: "Download" });
		expect(download).toHaveAttribute(
			"href",
			"/api/v1/services/svc-1/files/content?replica=0&path=%2Fapp.log",
		);
		const content_ = api.calls.find(
			(c) => c.path === "/services/svc-1/files/content",
		);
		expect(Object.fromEntries(content_?.query ?? [])).toEqual({
			replica: "0",
			path: "/app.log",
			preview: "true",
		});
	});

	test("Close dismisses the preview", async () => {
		const { user } = await openBrowser({
			"GET /services/:serviceId/files/content": content("hi"),
		});
		await user.click(await screen.findByRole("button", { name: /^app\.log$/ }));
		const dialog = await screen.findByRole("dialog", { name: "app.log" });
		await within(dialog).findByText("hi");
		await user.click(within(dialog).getByRole("button", { name: "Close" }));
		await waitFor(() =>
			expect(screen.queryByRole("dialog", { name: "app.log" })).toBeNull(),
		);
	});

	test("a binary file by name is not read, only offered for download", async () => {
		const { api, user } = await openBrowser({
			"GET /services/:serviceId/files": {
				body: listing([fileEntry("logo.png", { size: 100 })]),
			},
		});
		await user.click(await screen.findByRole("button", { name: /logo\.png/ }));
		expect(
			await screen.findByText(/This looks like a binary file\./),
		).toBeInTheDocument();
		expect(api.calls.filter((c) => c.path.endsWith("/files/content"))).toEqual(
			[],
		);
	});

	test("a file over the preview limit is not read", async () => {
		const { api, user } = await openBrowser({
			"GET /services/:serviceId/terminal": {
				body: terminalInfo({ previewBytes: 1024 }),
			},
			"GET /services/:serviceId/files": {
				body: listing([fileEntry("huge.log", { size: 5000 })]),
			},
		});
		await user.click(await screen.findByRole("button", { name: /huge\.log/ }));
		expect(
			await screen.findByText(/Files over 1 KiB are not previewed\./),
		).toBeInTheDocument();
		expect(api.calls.filter((c) => c.path.endsWith("/files/content"))).toEqual(
			[],
		);
	});

	test("content with a NUL byte is reported as not text", async () => {
		const { user } = await openBrowser({
			"GET /services/:serviceId/files/content": content(
				new Uint8Array([79, 75, 0, 1]),
			),
		});
		await user.click(await screen.findByRole("button", { name: /^app\.log$/ }));
		expect(
			await screen.findByText("This file is not text. Download it instead."),
		).toBeInTheDocument();
	});

	test("a read that fails shows the server's reason", async () => {
		const { user } = await openBrowser({
			"GET /services/:serviceId/files/content": () =>
				Response.json(
					{ code: "not_found", detail: "The file vanished." },
					{ status: 404 },
				),
		});
		await user.click(await screen.findByRole("button", { name: /^app\.log$/ }));
		expect(await screen.findByText("The file vanished.")).toBeInTheDocument();
	});

	test("a file in a process is read from that process", async () => {
		const { api, user } = await openBrowser(
			{
				"GET /services/:serviceId/terminal": {
					body: terminalInfo({
						replicas: [
							{ index: 0, process: "worker", running: true, state: "running" },
						],
					}),
				},
				"GET /services/:serviceId/files/content": content("x"),
			},
			{ process: "worker" },
		);
		await user.click(await screen.findByRole("button", { name: /^app\.log$/ }));
		await screen.findByText("x");
		expect(
			api.calls
				.find((c) => c.path.endsWith("/files/content"))
				?.query.get("process"),
		).toBe("worker");
	});
});

describe("FilesSheet upload", () => {
	const uploadInput = () =>
		document.querySelector('input[type="file"]') as HTMLInputElement;

	test("uploads the chosen file into the current folder, says so and reloads the listing", async () => {
		const { api, user } = await openBrowser({
			"POST /services/:serviceId/files/upload": {
				body: { path: "/in.json", size: 7 },
			},
		});
		await screen.findByRole("button", { name: /^etc$/ });
		const before = dirCalls(api).length;
		await user.upload(
			uploadInput(),
			new File(['{"k":1}'], "in.json", { type: "application/json" }),
		);
		expect(await screen.findByText("Uploaded")).toBeInTheDocument();
		expect(screen.getByText("/in.json")).toBeInTheDocument();
		const post = api.calls.find((c) => c.method === "POST");
		expect(post?.path).toBe("/services/svc-1/files/upload");
		expect(Object.fromEntries(post?.query ?? [])).toEqual({
			replica: "0",
			path: "/",
			name: "in.json",
		});
		expect(post?.body).toEqual({ k: 1 });
		await waitFor(() => expect(dirCalls(api).length).toBeGreaterThan(before));
	});

	test("Upload is disabled with the reason when the folder is read-only", async () => {
		await openBrowser({
			"GET /services/:serviceId/files": {
				body: listing([fileEntry("a")], {
					writable: false,
					readOnlyReason: "This directory is read-only.",
				}),
			},
		});
		const button = await screen.findByRole("button", { name: "Upload" });
		expect(button).toBeDisabled();
		expect(button).toHaveAttribute("title", "This directory is read-only.");
		// The same reason is also spelled out under the toolbar.
		expect(await screen.findByRole("status")).toHaveTextContent(
			"This directory is read-only.",
		);
	});

	test("a file over the limit is refused in the browser without any request", async () => {
		const { api, user } = await openBrowser({
			"GET /services/:serviceId/terminal": {
				body: terminalInfo({ maxUploadBytes: 1 << 20 }),
			},
		});
		await screen.findByRole("button", { name: /^etc$/ });
		const big = new File([new Uint8Array((1 << 20) + 1)], "big.bin");
		await user.upload(uploadInput(), big);
		expect(await screen.findByText("Cannot upload")).toBeInTheDocument();
		expect(
			screen.getByText("Uploads are limited to 1 MB."),
		).toBeInTheDocument();
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);
	});

	test("a failed upload shows the server's reason", async () => {
		const { user } = await openBrowser({
			"POST /services/:serviceId/files/upload": {
				status: 409,
				body: { code: "exists", detail: "That file already exists." },
			},
		});
		await screen.findByRole("button", { name: /^etc$/ });
		await user.upload(uploadInput(), new File(["{}"], "a.json"));
		expect(await screen.findByText("Upload failed")).toBeInTheDocument();
		expect(screen.getByText("That file already exists.")).toBeInTheDocument();
	});

	test("Upload opens the file chooser", async () => {
		const { user } = await openBrowser();
		await screen.findByRole("button", { name: /^etc$/ });
		let clicked = 0;
		uploadInput().addEventListener("click", (e) => {
			e.preventDefault();
			clicked += 1;
		});
		await user.click(screen.getByRole("button", { name: "Upload" }));
		expect(clicked).toBe(1);
	});
});

describe("FilesSheet production uploads", () => {
	const readOnly = () => ({
		"GET /services/:serviceId/terminal": {
			body: terminalInfo({ production: true }),
		},
		"GET /services/:serviceId/files": {
			body: listing([fileEntry("a")], {
				writable: false,
				readOnlyReason: "Production is read-only.",
			}),
		},
	});

	test("without a production service there is no Allow button", async () => {
		await openBrowser({
			"GET /services/:serviceId/files": {
				body: listing([fileEntry("a")], {
					writable: false,
					readOnlyReason: "Volume is read-only.",
				}),
			},
		});
		expect(await screen.findByText("Volume is read-only.")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Allow uploads…" })).toBeNull();
	});

	test("a read-only production service explains why and offers to allow uploads", async () => {
		await openBrowser(readOnly());
		expect(
			await screen.findByText("Production is read-only."),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Allow uploads…" }),
		).toBeInTheDocument();
	});

	test("cancelling the confirmation changes nothing", async () => {
		const { api, user } = await openBrowser(readOnly());
		await user.click(
			await screen.findByRole("button", { name: "Allow uploads…" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent("Allow uploads in production?");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(api.calls.filter((c) => c.method === "PUT")).toEqual([]);
	});

	test("confirming turns the project setting on and reloads the folder and the info", async () => {
		let allowed = false;
		const { api, user } = await openBrowser({
			...readOnly(),
			"PUT /projects/:projectId/terminal-settings": () => {
				allowed = true;
				return Response.json({ allowProductionFileWrites: true });
			},
			"GET /services/:serviceId/files": () =>
				Response.json(
					allowed
						? listing([fileEntry("a")])
						: listing([fileEntry("a")], {
								writable: false,
								readOnlyReason: "Production is read-only.",
							}),
				),
		});
		await user.click(
			await screen.findByRole("button", { name: "Allow uploads…" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Allow uploads" }),
		);
		expect(
			await screen.findByText("Uploads allowed in production"),
		).toBeInTheDocument();
		const put = api.calls.find((c) => c.method === "PUT");
		expect(put?.path).toBe("/projects/proj-1/terminal-settings");
		expect(put?.body).toEqual({ allowProductionFileWrites: true });
		await waitFor(() =>
			expect(screen.getByRole("button", { name: "Upload" })).toBeEnabled(),
		);
		expect(screen.queryByText("Production is read-only.")).toBeNull();
	});

	test("a member who cannot change the setting is told so", async () => {
		const { user } = await openBrowser({
			...readOnly(),
			"PUT /projects/:projectId/terminal-settings": problem(403, {
				code: "forbidden",
				detail: "Admins only.",
			}),
		});
		await user.click(
			await screen.findByRole("button", { name: "Allow uploads…" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Allow uploads" }),
		);
		expect(
			await screen.findByText("Only an owner or admin can change this"),
		).toBeInTheDocument();
		// The dialog stays open with the reason.
		expect(within(dialog).getByText("Admins only.")).toBeInTheDocument();
	});

	test("any other failure to change it is reported generically", async () => {
		const { user } = await openBrowser({
			...readOnly(),
			"PUT /projects/:projectId/terminal-settings": problem(500, {
				title: "Internal Server Error",
				detail: "Database down.",
			}),
		});
		await user.click(
			await screen.findByRole("button", { name: "Allow uploads…" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Allow uploads" }),
		);
		expect(
			await screen.findByText("Could not change the setting"),
		).toBeInTheDocument();
	});
});
