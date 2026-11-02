import { describe, expect, test } from "bun:test";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { emptySource, type SourceDraft } from "./lib";
import { SourceStep } from "./source-step";

const NO_ACCOUNTS = {
	"GET /git/connections": {
		body: make("ListConnectionsOutputBody", { items: [] }),
	},
};

function Host({
	initial,
	knownPaths,
	seen,
}: {
	initial: SourceDraft;
	knownPaths: string[];
	seen: { draft: SourceDraft };
}) {
	const [draft, setDraft] = useState(initial);
	seen.draft = draft;
	return (
		<SourceStep draft={draft} onChange={setDraft} knownPaths={knownPaths} />
	);
}

async function step(
	initial: SourceDraft = emptySource(),
	routes: Parameters<typeof stubApi>[0] = NO_ACCOUNTS,
	knownPaths: string[] = [],
) {
	const api = stubApi(routes);
	const seen = { draft: initial };
	const view = await renderWithProviders(
		<Host initial={initial} knownPaths={knownPaths} seen={seen} />,
	);
	return { ...view, seen, api };
}

const file = (name: string, content: string) =>
	new File([content], name, { type: "text/plain" });

describe("import mode", () => {
	test("explains each mode and switches between them", async () => {
		const user = userEvent.setup();
		const { seen } = await step();
		expect(
			screen.getByText(/Each Compose service becomes its own service/),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Run as stack" }));
		expect(seen.draft.importMode).toBe("stack");
		expect(
			screen.getByText(/runs as it is with docker compose, as one service/),
		).toBeInTheDocument();
		await user.click(
			screen.getByRole("button", { name: "Split into services" }),
		);
		expect(seen.draft.importMode).toBe("split");
	});
});

describe("Paste tab", () => {
	test("typing the Compose file updates the draft and forgets an uploaded name", async () => {
		const user = userEvent.setup();
		const { seen } = await step({
			...emptySource(),
			compose: "old",
			composeName: "docker-compose.yml",
		});
		const box = screen.getByRole("textbox", { name: "docker-compose.yml" });
		expect(box).toHaveValue("old");
		await user.type(box, "!");
		expect(seen.draft.compose).toBe("old!");
		expect(seen.draft.composeName).toBe("");
	});

	test("the .env box is folded away until opened; it says when one is added", async () => {
		const user = userEvent.setup();
		const { seen } = await step();
		const toggle = screen.getByRole("button", { name: /Environment file/ });
		expect(toggle).toHaveAttribute("aria-expanded", "false");
		expect(screen.queryByText("added")).not.toBeInTheDocument();
		await user.click(toggle);
		expect(toggle).toHaveAttribute("aria-expanded", "true");
		const env = screen.getByRole("textbox", {
			name: "Environment file (.env)",
		});
		await user.type(env, "TAG=1");
		expect(seen.draft.env).toBe("TAG=1");
		expect(screen.getByText("added")).toBeInTheDocument();
	});

	test("starts open when an .env is already present", async () => {
		await step({ ...emptySource(), env: "A=1" });
		expect(
			screen.getByRole("button", { name: /Environment file/ }),
		).toHaveAttribute("aria-expanded", "true");
		expect(
			screen.getByRole("textbox", { name: "Environment file (.env)" }),
		).toHaveValue("A=1");
	});
});

describe("Upload tab", () => {
	async function upload() {
		const user = userEvent.setup();
		const view = await step();
		await user.click(screen.getByRole("tab", { name: "Upload" }));
		return { user, ...view };
	}
	const picker = () =>
		screen.getByLabelText("Choose files") as HTMLInputElement;

	test("a compose file and an .env are classified by name", async () => {
		const { user, seen } = await upload();
		await user.upload(picker(), [
			file("docker-compose.yml", "services: {}"),
			file(".env", "A=1"),
		]);
		await waitFor(() => expect(seen.draft.compose).toBe("services: {}"));
		expect(seen.draft.composeName).toBe("docker-compose.yml");
		expect(seen.draft.env).toBe("A=1");
		expect(seen.draft.envName).toBe(".env");
		expect(screen.getByText("docker-compose.yml")).toBeInTheDocument();
		expect(screen.getByText(/Compose file · 12 B/)).toBeInTheDocument();
		expect(screen.getByText(/Environment · 3 B/)).toBeInTheDocument();
	});

	test("any other file is an extra whose path in the Compose file can be edited", async () => {
		const { user, seen } = await upload();
		await user.upload(picker(), file("nginx.conf", "worker_processes 1;"));
		const path = await screen.findByRole("textbox", {
			name: "Path in the Compose file",
		});
		expect(path).toHaveValue("./nginx.conf");
		expect(seen.draft.uploads).toHaveLength(1);
		expect(seen.draft.uploads[0]).toMatchObject({
			name: "nginx.conf",
			content: "worker_processes 1;",
			pathEdited: false,
		});
		fireEvent.change(path, { target: { value: "./conf/nginx.conf" } });
		expect(seen.draft.uploads[0]).toMatchObject({
			path: "./conf/nginx.conf",
			pathEdited: true,
		});
	});

	test("an extra is matched to the path the plan asked for", async () => {
		const user = userEvent.setup();
		await step(emptySource(), NO_ACCOUNTS, ["./config/app.env"]);
		await user.click(screen.getByRole("tab", { name: "Upload" }));
		await user.upload(picker(), file("app.env", "X=1"));
		expect(
			await screen.findByRole("textbox", { name: "Path in the Compose file" }),
		).toHaveValue("./config/app.env");
	});

	test("uploading the same extra twice keeps one row with the new content", async () => {
		const { user, seen } = await upload();
		await user.upload(picker(), file("a.conf", "one"));
		await screen.findByText("a.conf");
		await user.upload(picker(), file("a.conf", "two!"));
		await waitFor(() => expect(seen.draft.uploads[0]?.content).toBe("two!"));
		expect(seen.draft.uploads).toHaveLength(1);
	});

	test("a file over 256 KB is refused with its name", async () => {
		const { user, seen } = await upload();
		await user.upload(picker(), [
			file("big.conf", "x".repeat(262145)),
			file("docker-compose.yml", "services: {}"),
		]);
		expect(
			await screen.findByText(
				"big.conf is larger than 256 KB and was not added.",
			),
		).toBeInTheDocument();
		// The other file in the same pick is still taken.
		expect(seen.draft.compose).toBe("services: {}");
		expect(seen.draft.uploads).toEqual([]);
	});

	test("several refused files use the plural", async () => {
		const { user } = await upload();
		await user.upload(picker(), [
			file("a.bin", "x".repeat(262145)),
			file("b.bin", "x".repeat(262145)),
		]);
		expect(
			await screen.findByText(/^a\.bin, b\.bin are larger than 256 KB and/),
		).toBeInTheDocument();
	});

	test("dropping files works like choosing them; the zone highlights while dragging", async () => {
		const { seen } = await upload();
		const zone = screen
			.getByText("Drop docker-compose.yml here")
			.closest("div") as HTMLElement;
		fireEvent.dragOver(zone);
		expect(zone.className).toContain("bg-accent-wash");
		fireEvent.dragLeave(zone);
		expect(zone.className).not.toContain("bg-accent-wash");

		fireEvent.dragOver(zone);
		fireEvent.drop(zone, {
			dataTransfer: { files: [file("compose.yaml", "services: {a: {}}")] },
		});
		await waitFor(() => expect(seen.draft.compose).toBe("services: {a: {}}"));
		expect(zone.className).not.toContain("bg-accent-wash");
	});

	test("a yml that is not named like a Compose file is the Compose file only when none exists", async () => {
		const { user, seen } = await upload();
		await user.upload(picker(), file("stack.yml", "services: {}"));
		await waitFor(() => expect(seen.draft.composeName).toBe("stack.yml"));
		await user.upload(picker(), file("other.yml", "x: 1"));
		await waitFor(() => expect(seen.draft.uploads).toHaveLength(1));
		expect(seen.draft.uploads[0]?.name).toBe("other.yml");
		expect(seen.draft.composeName).toBe("stack.yml");
	});

	test("each uploaded row can be removed", async () => {
		const { user, seen } = await upload();
		await user.upload(picker(), [
			file("docker-compose.yml", "services: {}"),
			file(".env", "A=1"),
			file("x.conf", "c"),
		]);
		await screen.findByText("x.conf");
		await user.click(screen.getByRole("button", { name: "Remove x.conf" }));
		expect(seen.draft.uploads).toEqual([]);
		await user.click(screen.getByRole("button", { name: "Remove .env" }));
		expect(seen.draft.env).toBe("");
		expect(seen.draft.envName).toBe("");
		await user.click(
			screen.getByRole("button", { name: "Remove docker-compose.yml" }),
		);
		expect(seen.draft.compose).toBe("");
		expect(seen.draft.composeName).toBe("");
		expect(screen.queryByRole("list")).not.toBeInTheDocument();
	});

	test("a Compose file already in the Paste tab is mentioned on the Upload tab", async () => {
		const user = userEvent.setup();
		await step({ ...emptySource(), compose: "services: {}" });
		await user.click(screen.getByRole("tab", { name: "Upload" }));
		expect(
			screen.getByText(
				"A Compose file is already in the Paste tab and will be used.",
			),
		).toBeInTheDocument();
	});
});

describe("Git tab", () => {
	const account = make("ConnectionView", {
		id: "c1",
		name: "acme-org",
		provider: "github",
		status: "active",
		canListRepos: true,
		accountLogin: "acme",
	});

	test("without an account the reader types a URL, branch and path", async () => {
		const user = userEvent.setup();
		const { seen } = await step();
		await user.click(screen.getByRole("tab", { name: "From git" }));
		await user.type(
			screen.getByRole("textbox", { name: "Repository URL" }),
			"https://github.com/acme/shop",
		);
		await user.type(screen.getByRole("textbox", { name: "Branch" }), "main");
		await user.type(
			screen.getByRole("textbox", { name: "Compose file path" }),
			"deploy/compose.yml",
		);
		expect(seen.draft.mode).toBe("git");
		expect(seen.draft.git).toMatchObject({
			url: "https://github.com/acme/shop",
			branch: "main",
			path: "deploy/compose.yml",
		});
		expect(
			screen.getByText(/Connect a git account in Team settings/),
		).toBeInTheDocument();
		// Only the URL form exists with no account to pick.
		expect(
			screen.queryByRole("group", { name: "Where the repository comes from" }),
		).not.toBeInTheDocument();
	});

	test("with an account the reader can pick a repository or switch to a URL", async () => {
		const user = userEvent.setup();
		const { seen } = await step(emptySource(), {
			"GET /git/connections": {
				body: make("ListConnectionsOutputBody", { items: [account] }),
			},
			"GET /git/connections/:id/repos": {
				body: make("ReposOutputBody", {
					items: [
						make("Repo", {
							fullName: "acme/shop",
							defaultBranch: "trunk",
							cloneUrl: "https://github.com/acme/shop.git",
							description: "",
							private: false,
						}),
					],
				}),
			},
			"GET /git/connections/:id/branches": {
				body: { items: ["trunk", "dev"] },
			},
		});
		await user.click(screen.getByRole("tab", { name: "From git" }));
		const kind = await screen.findByRole("group", {
			name: "Where the repository comes from",
		});
		await user.click(await screen.findByRole("button", { name: /acme\/shop/ }));
		expect(seen.draft.git).toMatchObject({
			connectionId: "c1",
			repo: "acme/shop",
			branch: "trunk",
		});

		await user.click(
			within(kind).getByRole("button", { name: "Public repository URL" }),
		);
		expect(seen.draft.git.mode).toBe("url");
		expect(
			screen.getByRole("textbox", { name: "Repository URL" }),
		).toBeInTheDocument();
		expect(
			screen.queryByText(/Connect a git account in Team settings/),
		).not.toBeInTheDocument();
	});
});
