import { describe, expect, test } from "bun:test";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { ApiError } from "#/api/client-config";
import { makeService } from "#/features/canvas/lib/fixtures";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import {
	buildDockerOptions,
	type DockerForm,
	dockerErrorMessage,
	EMPTY_DOCKER_FORM,
	REQUIRES_UNRESTRICTED,
} from "./docker-options";
import { DockerOptionsSection } from "./docker-options-section";
import { type SettingsDraft, toDraft } from "./draft";

const catalog = {
	safeCapabilities: ["CHOWN", "NET_BIND_SERVICE", "SYS_PTRACE"],
	warnCapabilities: ["SYS_PTRACE"],
	unrestrictedCapabilities: ["NET_ADMIN", "SYS_ADMIN"],
	knownCapabilities: [
		"CHOWN",
		"NET_ADMIN",
		"NET_BIND_SERVICE",
		"NET_RAW",
		"SYS_ADMIN",
		"SYS_PTRACE",
	],
	allowedSysctls: ["net.core.somaxconn", "net.ipv4.ip_forward"],
	hostSysctlKeys: ["vm.max_map_count", "fs.file-max"],
	gpuCapabilities: ["compute", "video"],
	reservedLabels: ["com.docker.", "nebulactrl."],
	stopSignals: ["SIGTERM", "SIGINT", "SIGUSR1"],
	unrestrictedFields: [],
};

const service = makeService({ id: "svc-1", slug: "api", serverId: "server-1" });

interface Setup {
	docker?: Partial<DockerForm>;
	policy?: "ok" | "pending" | "failed";
	unrestricted?: boolean;
	serverId?: string;
	files?: { path: string; mode: string; content: string }[];
	volumes?: { mountPath: string }[];
	serverError?: string | undefined;
}

// Role queries skip the accessibility-tree check (`hidden`): computing styles
// for every ancestor makes them take seconds on a form this size.
type RoleArgs = Parameters<typeof screen.getByRole>;
const withHidden = (args: RoleArgs): RoleArgs => [
	args[0],
	{ hidden: true, ...args[1] },
];
const getByRole = (...args: RoleArgs) => screen.getByRole(...withHidden(args));
const queryByRole = (...args: RoleArgs) =>
	screen.queryByRole(...withHidden(args));
const getAllByRole = (...args: RoleArgs) =>
	screen.getAllByRole(...withHidden(args));
const findByRole = (...args: RoleArgs) =>
	screen.findByRole(...withHidden(args));

let latest: SettingsDraft;

function Harness({
	initial,
	serverError,
}: {
	initial: SettingsDraft;
	serverError?: string | undefined;
}) {
	const [draft, setDraft] = useState(initial);
	latest = draft;
	return (
		<DockerOptionsSection
			draft={draft}
			errors={{}}
			set={(key, value) => setDraft((prev) => ({ ...prev, [key]: value }))}
			service={service}
			serverError={serverError}
		/>
	);
}

const capsGroup = () => getByRole("group", { name: "Capabilities" });
const dropGroup = () => getByRole("group", { name: "Dropped capabilities" });
const chipNames = (group: HTMLElement) =>
	[...group.querySelectorAll("button")].map((button) => button.textContent);
const chip = (group: HTMLElement, name: string) =>
	within(group).getByRole("button", { name, hidden: true });

async function setup(options: Setup = {}) {
	const policy = options.policy ?? "ok";
	const api = stubApi({
		"GET /instance/docker-options":
			policy === "ok"
				? { body: catalog }
				: policy === "failed"
					? problem(500, { title: "Boom", code: "internal" })
					: () => new Promise<Response>(() => {}),
		"GET /servers": {
			body: make("PageServer", {
				items: [
					make("Server", {
						id: "server-1",
						name: "hetzner-01",
						unrestricted: options.unrestricted ?? false,
					}),
				],
			}),
		},
		"GET /services/:serviceId/volumes": {
			body: {
				items: (options.volumes ?? []).map((volume, i) => ({
					id: `vol-${i}`,
					name: `vol-${i}`,
					dockerName: `d-${i}`,
					mountPath: volume.mountPath,
				})),
			},
		},
	});
	const initial: SettingsDraft = {
		...toDraft(service),
		serverId: options.serverId ?? "server-1",
		files: options.files ?? [],
		docker: { ...EMPTY_DOCKER_FORM, ...options.docker },
	};
	await renderWithProviders(
		<Harness initial={initial} serverError={options.serverError} />,
	);
	if (policy === "ok") {
		await within(
			await findByRole("group", { name: "Capabilities" }),
		).findByRole("button", { name: "NET_BIND_SERVICE" });
	}
	return { api, user: userEvent.setup() };
}

const type = (label: string, value: string) =>
	fireEvent.change(screen.getByLabelText(label), { target: { value } });

const pressed = (button: HTMLElement) => button.getAttribute("aria-pressed");

describe("hardened preset", () => {
	test("applying it turns the hardening options on and keeps the rest", async () => {
		const { user } = await setup({ docker: { hostname: "keep" } });
		expect(
			screen.queryByText("The hardening options below are on."),
		).not.toBeInTheDocument();

		await user.click(getByRole("button", { name: "Apply Hardened preset" }));

		expect(latest.docker.capDrop).toEqual(["ALL"]);
		expect(latest.docker.noNewPrivileges).toBe(true);
		expect(latest.docker.readOnlyRootfs).toBe(true);
		expect(latest.docker.pidsLimit).toBe("512");
		expect(latest.docker.hostname).toBe("keep");
		expect(buildDockerOptions(latest.docker)).toEqual({
			capDrop: ["ALL"],
			noNewPrivileges: true,
			readOnlyRootfs: true,
			pidsLimit: 512,
			hostname: "keep",
			tmpfs: [{ path: "/tmp", sizeMb: 64 }],
		});
		const done = getByRole("button", { name: "Hardened" });
		expect(done).toBeDisabled();
		expect(
			screen.getByText("The hardening options below are on."),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Process limit")).toHaveValue("512");
		expect(screen.getByLabelText("tmpfs 1 path")).toHaveValue("/tmp");
	});

	test("an existing tmpfs mount is kept as it is, with no /tmp added", async () => {
		const { user } = await setup({
			docker: { tmpfs: [{ path: "/var/cache", sizeMb: "8", mode: "" }] },
		});
		await user.click(getByRole("button", { name: "Apply Hardened preset" }));
		expect(latest.docker.tmpfs).toEqual([
			{ path: "/var/cache", sizeMb: "8", mode: "" },
		]);
	});

	test("a form that already has every hardening option shows the preset as done", async () => {
		await setup({
			docker: {
				capDrop: ["ALL"],
				noNewPrivileges: true,
				readOnlyRootfs: true,
				pidsLimit: "100",
				tmpfs: [{ path: "/tmp", sizeMb: "", mode: "" }],
			},
		});
		expect(getByRole("button", { name: "Hardened" })).toBeDisabled();
	});

	test("one missing option keeps the preset available", async () => {
		await setup({
			docker: {
				capDrop: ["ALL"],
				noNewPrivileges: true,
				readOnlyRootfs: true,
			},
		});
		expect(
			getByRole("button", { name: "Apply Hardened preset" }),
		).toBeEnabled();
	});
});

describe("dropped capabilities", () => {
	test("offers ALL and the policy's known capabilities", async () => {
		await setup();
		expect(chipNames(dropGroup())).toEqual([
			"ALL",
			"CHOWN",
			"NET_ADMIN",
			"NET_BIND_SERVICE",
			"NET_RAW",
			"SYS_ADMIN",
			"SYS_PTRACE",
		]);
	});

	test("dropping ALL locks the other chips; taking it off frees them", async () => {
		const { user } = await setup();
		await user.click(chip(dropGroup(), "ALL"));
		expect(latest.docker.capDrop).toEqual(["ALL"]);
		expect(pressed(chip(dropGroup(), "ALL"))).toBe("true");
		const chown = chip(dropGroup(), "CHOWN");
		expect(chown).toBeDisabled();
		expect(chown).toHaveAttribute("title", "ALL already drops everything");

		await user.click(chip(dropGroup(), "ALL"));
		expect(latest.docker.capDrop).toEqual([]);
		expect(chip(dropGroup(), "CHOWN")).toBeEnabled();
	});

	test("dropped capabilities are kept sorted", async () => {
		const { user } = await setup();
		await user.click(chip(dropGroup(), "NET_RAW"));
		await user.click(chip(dropGroup(), "CHOWN"));
		expect(latest.docker.capDrop).toEqual(["CHOWN", "NET_RAW"]);
		await user.click(chip(dropGroup(), "NET_RAW"));
		expect(latest.docker.capDrop).toEqual(["CHOWN"]);
	});

	test("a capability that is added cannot also be dropped", async () => {
		await setup({ docker: { capAdd: ["CHOWN"] } });
		const chown = chip(dropGroup(), "CHOWN");
		expect(chown).toBeDisabled();
		expect(chown).toHaveAttribute("title", "CHOWN is added under Capabilities");
	});

	test("a dropped name the policy does not know stays visible with its problem", async () => {
		const { user } = await setup({ docker: { capDrop: ["BOGUS"] } });
		expect(chip(dropGroup(), "BOGUS")).toBeInTheDocument();
		expect(
			screen.getByText(
				"BOGUS is not a Linux capability; use ALL or a name such as NET_RAW.",
			),
		).toBeInTheDocument();
		await user.click(chip(dropGroup(), "BOGUS"));
		expect(latest.docker.capDrop).toEqual([]);
		expect(queryByRole("button", { name: "BOGUS" })).not.toBeInTheDocument();
	});

	test("adding and dropping the same capability is reported", async () => {
		await setup({ docker: { capAdd: ["CHOWN"], capDrop: ["CHOWN"] } });
		expect(
			screen.getByText(
				"CHOWN is in both the added and the dropped capabilities.",
			),
		).toBeInTheDocument();
	});

	test("says the names are loading while the policy is pending", async () => {
		await setup({ policy: "pending" });
		expect(
			screen.getByText("Loading the capability names…"),
		).toBeInTheDocument();
		expect(
			queryByRole("group", { name: "Dropped capabilities" }),
		).not.toBeInTheDocument();
	});

	test("when the policy fails only ALL can be dropped", async () => {
		await setup({ policy: "failed" });
		await screen.findByText(
			"Could not load the allowed capabilities. Reload to try again.",
		);
		expect(chipNames(dropGroup())).toEqual(["ALL"]);
	});
});

describe("added capabilities", () => {
	test("on a restricted server NET_ADMIN and SYS_ADMIN are locked, with the reason", async () => {
		await setup({ unrestricted: false });
		for (const cap of ["NET_ADMIN", "SYS_ADMIN"]) {
			const button = chip(capsGroup(), cap);
			expect(button).toBeDisabled();
			expect(button).toHaveAttribute(
				"title",
				"Works only on an unrestricted server (see Host access below)",
			);
		}
		expect(chip(capsGroup(), "CHOWN")).toBeEnabled();
		expect(
			screen.getByText(/NET_ADMIN, SYS_ADMIN/, { selector: "span" }),
		).toBeInTheDocument();
	});

	test("on an unrestricted server they can be added, with a warning", async () => {
		const { user } = await setup({ unrestricted: true });
		await waitFor(() => expect(chip(capsGroup(), "NET_ADMIN")).toBeEnabled());
		expect(
			screen.queryByText(/Works only on an unrestricted server \(see Host/, {
				selector: "p",
			}),
		).not.toBeInTheDocument();
		await user.click(chip(capsGroup(), "NET_ADMIN"));
		expect(latest.docker.capAdd).toEqual(["NET_ADMIN"]);
		expect(
			screen.getByText(/NET_ADMIN widens the container's power over the host/),
		).toBeInTheDocument();
	});

	test("a capability already added stays removable on a restricted server", async () => {
		const { user } = await setup({
			unrestricted: false,
			docker: { capAdd: ["NET_ADMIN"] },
		});
		const button = chip(capsGroup(), "NET_ADMIN");
		expect(button).toBeEnabled();
		expect(pressed(button)).toBe("true");
		await user.click(button);
		expect(latest.docker.capAdd).toEqual([]);
	});

	test("added capabilities are kept sorted and SYS_PTRACE warns", async () => {
		const { user } = await setup();
		await user.click(chip(capsGroup(), "SYS_PTRACE"));
		await user.click(chip(capsGroup(), "CHOWN"));
		expect(latest.docker.capAdd).toEqual(["CHOWN", "SYS_PTRACE"]);
		expect(
			screen.getByText(/SYS_PTRACE lets processes in the container inspect/),
		).toBeInTheDocument();
		await user.click(chip(capsGroup(), "SYS_PTRACE"));
		expect(latest.docker.capAdd).toEqual(["CHOWN"]);
		expect(
			screen.queryByText(/SYS_PTRACE lets processes/),
		).not.toBeInTheDocument();
	});

	test("a capability the policy does not list stays visible with its problem", async () => {
		const { user } = await setup({ docker: { capAdd: ["FOO"] } });
		expect(
			screen.getByText("FOO is not an allowed capability."),
		).toBeInTheDocument();
		await user.click(chip(capsGroup(), "FOO"));
		expect(latest.docker.capAdd).toEqual([]);
		expect(queryByRole("button", { name: "FOO" })).not.toBeInTheDocument();
	});

	test("says the capabilities are loading while the policy is pending", async () => {
		await setup({ policy: "pending" });
		expect(
			screen.getByText("Loading the allowed capabilities…"),
		).toBeInTheDocument();
		expect(
			queryByRole("group", { name: "Capabilities" }),
		).not.toBeInTheDocument();
	});

	test("a failed policy with nothing added asks to reload", async () => {
		await setup({ policy: "failed" });
		expect(
			await screen.findByText(
				"Could not load the allowed capabilities. Reload to try again.",
			),
		).toBeInTheDocument();
	});

	test("a failed policy still shows what is already added", async () => {
		await setup({ policy: "failed", docker: { capAdd: ["CHOWN"] } });
		await findByRole("group", { name: "Capabilities" });
		expect(
			screen.queryByText(/Could not load the allowed capabilities/),
		).not.toBeInTheDocument();
		expect(pressed(chip(capsGroup(), "CHOWN"))).toBe("true");
		// The built-in list still names what needs an unrestricted server.
		expect(chip(capsGroup(), "NET_ADMIN")).toBeDisabled();
	});
});

describe("switches and the process limit", () => {
	test("each switch sets its own option", async () => {
		const { user } = await setup();
		await user.click(getByRole("switch", { name: "No new privileges" }));
		expect(latest.docker.noNewPrivileges).toBe(true);
		expect(latest.docker.init).toBe(false);
		await user.click(getByRole("switch", { name: "Init process" }));
		expect(latest.docker.init).toBe(true);
		await user.click(getByRole("switch", { name: "Read-only root" }));
		expect(latest.docker.readOnlyRootfs).toBe(true);
		await user.click(getByRole("switch", { name: "No new privileges" }));
		expect(latest.docker.noNewPrivileges).toBe(false);
	});

	test("switches show the saved state", async () => {
		await setup({
			docker: { noNewPrivileges: true, init: true, readOnlyRootfs: false },
		});
		expect(getByRole("switch", { name: "No new privileges" })).toBeChecked();
		expect(getByRole("switch", { name: "Init process" })).toBeChecked();
		expect(getByRole("switch", { name: "Read-only root" })).not.toBeChecked();
	});

	test("the process limit edits the draft and shows a bad value", async () => {
		await setup();
		type("Process limit", "256");
		expect(latest.docker.pidsLimit).toBe("256");
		expect(screen.getByLabelText("Process limit")).toHaveAttribute(
			"aria-invalid",
			"false",
		);
		type("Process limit", "lots");
		expect(
			screen.getByText(
				/The limit is 0 \(none\) or 1 to \d+ processes and threads\./,
			),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Process limit")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
	});
});

describe("read-only root warnings", () => {
	test("a read-only root without a tmpfs warns until one is added", async () => {
		const { user } = await setup({ docker: { readOnlyRootfs: true } });
		expect(
			screen.getByText(
				/The root filesystem is read-only and no tmpfs is mounted/,
			),
		).toBeInTheDocument();
		await user.click(getByRole("button", { name: "Add tmpfs" }));
		expect(
			screen.getByText(
				/The root filesystem is read-only and no tmpfs is mounted/,
			),
		).toBeInTheDocument();
		type("tmpfs 1 path", "/tmp");
		expect(screen.queryByText(/no tmpfs is mounted/)).not.toBeInTheDocument();
	});

	test("a writable root has no warning", async () => {
		await setup({ docker: { readOnlyRootfs: false } });
		expect(screen.queryByText(/no tmpfs is mounted/)).not.toBeInTheDocument();
	});

	test("a config file outside every volume says saving will fail", async () => {
		await setup({
			docker: {
				readOnlyRootfs: true,
				tmpfs: [{ path: "/tmp", sizeMb: "", mode: "" }],
			},
			files: [{ path: "/etc/app.conf", mode: "", content: "x" }],
		});
		const alert = await screen.findByText(
			/Saving fails while the root is read-only/,
		);
		expect(alert).toHaveTextContent(
			"the config file /etc/app.conf is written into the root filesystem",
		);
	});

	test("files on a volume are not reported, only the ones outside", async () => {
		await setup({
			docker: {
				readOnlyRootfs: true,
				tmpfs: [{ path: "/tmp", sizeMb: "", mode: "" }],
			},
			files: [
				{ path: "/data/app.conf", mode: "", content: "" },
				{ path: "/etc/a.conf", mode: "", content: "" },
				{ path: "/etc/b.conf", mode: "", content: "" },
			],
			volumes: [{ mountPath: "/data" }],
		});
		const alert = await screen.findByText(
			/Saving fails while the root is read-only/,
		);
		expect(alert).toHaveTextContent(
			"the config files /etc/a.conf, /etc/b.conf are written into the root filesystem",
		);
		expect(alert).not.toHaveTextContent("/data/app.conf");
	});

	test("a tmpfs on a file mount is an error on that row", async () => {
		await setup({
			docker: { tmpfs: [{ path: "/etc/app.conf", sizeMb: "", mode: "" }] },
			files: [{ path: "/etc/app.conf", mode: "", content: "" }],
		});
		expect(
			screen.getByText("/etc/app.conf is also a file mount."),
		).toBeInTheDocument();
	});
});

describe("ulimits", () => {
	test("adds the first unused resource, then the second, then stops", async () => {
		const { user } = await setup();
		const add = getByRole("button", { name: "Add ulimit" });
		await user.click(add);
		expect(latest.docker.ulimits).toEqual([
			{ name: "nofile", soft: "", hard: "" },
		]);
		await user.click(add);
		expect(latest.docker.ulimits.map((row) => row.name)).toEqual([
			"nofile",
			"memlock",
		]);
		expect(add).toBeDisabled();
	});

	test("edits soft and hard limits and removes a row", async () => {
		const { user } = await setup({
			docker: {
				ulimits: [
					{ name: "nofile", soft: "1024", hard: "2048" },
					{ name: "memlock", soft: "unlimited", hard: "unlimited" },
				],
			},
		});
		type("Ulimit 1 soft limit", "4096");
		type("Ulimit 1 hard limit", "8192");
		expect(latest.docker.ulimits[0]).toEqual({
			name: "nofile",
			soft: "4096",
			hard: "8192",
		});
		expect(latest.docker.ulimits[1]).toEqual({
			name: "memlock",
			soft: "unlimited",
			hard: "unlimited",
		});
		await user.click(getByRole("button", { name: "Remove ulimit 1" }));
		expect(latest.docker.ulimits).toEqual([
			{ name: "memlock", soft: "unlimited", hard: "unlimited" },
		]);
		expect(screen.getByLabelText("Ulimit 1 soft limit")).toHaveValue(
			"unlimited",
		);
	});

	test("changes the resource of a row", async () => {
		const { user } = await setup({
			docker: { ulimits: [{ name: "nofile", soft: "1", hard: "2" }] },
		});
		await user.click(getByRole("combobox", { name: "Ulimit 1 resource" }));
		await user.click(await findByRole("option", { name: "memlock" }));
		expect(latest.docker.ulimits).toEqual([
			{ name: "memlock", soft: "1", hard: "2" },
		]);
	});

	test("shows the problem of the row that has one", async () => {
		await setup({
			docker: {
				ulimits: [
					{ name: "nofile", soft: "10", hard: "5" },
					{ name: "memlock", soft: "1", hard: "2" },
				],
			},
		});
		expect(screen.getByText("Soft must not exceed hard.")).toBeInTheDocument();
		expect(screen.getByLabelText("Ulimit 1 soft limit")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
		expect(screen.getByLabelText("Ulimit 2 soft limit")).toHaveAttribute(
			"aria-invalid",
			"false",
		);
	});
});

describe("simple text options", () => {
	test("shared memory, entrypoint, hostname and user edit the draft", async () => {
		await setup();
		type("Shared memory", "256");
		type("Entrypoint", '/app/entry.sh "two words"');
		type("Hostname", "web-1");
		type("User", "1000:1000");
		expect(latest.docker.shmSizeMb).toBe("256");
		expect(latest.docker.entrypoint).toBe('/app/entry.sh "two words"');
		expect(latest.docker.hostname).toBe("web-1");
		expect(latest.docker.user).toBe("1000:1000");
		expect(buildDockerOptions(latest.docker)).toEqual({
			shmSizeMb: 256,
			entrypoint: ["/app/entry.sh", "two words"],
			hostname: "web-1",
			user: "1000:1000",
		});
	});

	test("the hostname placeholder is the service slug", async () => {
		await setup();
		expect(screen.getByLabelText("Hostname")).toHaveAttribute(
			"placeholder",
			"api",
		);
	});

	test("each field shows its own validation problem", async () => {
		await setup({
			docker: {
				shmSizeMb: "0",
				hostname: "Bad_Name",
				user: "a b",
				entrypoint: "x\0y",
				stopSignal: "SIGFOO",
			},
		});
		expect(
			screen.getByText(/Shared memory must be 1 to \d+ MiB\./),
		).toBeInTheDocument();
		expect(screen.getByText(/The hostname is one label/)).toBeInTheDocument();
		expect(screen.getByText(/The user is uid\[:gid\]/)).toBeInTheDocument();
		expect(
			screen.getByText("The entrypoint cannot contain a NUL character."),
		).toBeInTheDocument();
		expect(
			screen.getByText("Pick one of SIGTERM, SIGINT, SIGUSR1."),
		).toBeInTheDocument();
		for (const label of ["Shared memory", "Hostname", "User", "Entrypoint"]) {
			expect(screen.getByLabelText(label)).toHaveAttribute(
				"aria-invalid",
				"true",
			);
		}
	});
});

describe("stop signal", () => {
	test("shows the image default, then a chosen signal from the policy", async () => {
		const { user } = await setup();
		const trigger = getByRole("combobox", { name: "Stop signal" });
		expect(trigger).toHaveTextContent("Image default");
		await user.click(trigger);
		expect(getAllByRole("option").map((option) => option.textContent)).toEqual([
			"Image default",
			"SIGTERM",
			"SIGINT",
			"SIGUSR1",
		]);
		await user.click(getByRole("option", { name: "SIGINT" }));
		expect(latest.docker.stopSignal).toBe("SIGINT");
		expect(trigger).toHaveTextContent("SIGINT");
	});

	test("choosing the image default clears the signal", async () => {
		const { user } = await setup({ docker: { stopSignal: "SIGTERM" } });
		await user.click(getByRole("combobox", { name: "Stop signal" }));
		await user.click(getByRole("option", { name: "Image default" }));
		expect(latest.docker.stopSignal).toBe("");
	});

	test("before the policy loads the built-in signals are offered", async () => {
		const { user } = await setup({ policy: "pending" });
		await user.click(getByRole("combobox", { name: "Stop signal" }));
		expect(getAllByRole("option").map((option) => option.textContent)).toEqual([
			"Image default",
			"SIGTERM",
			"SIGINT",
			"SIGQUIT",
			"SIGHUP",
			"SIGUSR1",
			"SIGUSR2",
			"SIGWINCH",
			"SIGKILL",
		]);
	});
});

describe("list options", () => {
	test("supplementary groups: add, edit, remove and validate", async () => {
		const { user } = await setup({ docker: { groupAdd: ["109", "abc"] } });
		expect(
			screen.getByText("abc is not a numeric group ID from 1 to 65535."),
		).toBeInTheDocument();
		type("Group ID 2", "110");
		expect(latest.docker.groupAdd).toEqual(["109", "110"]);
		await user.click(getByRole("button", { name: "Add group" }));
		expect(latest.docker.groupAdd).toEqual(["109", "110", ""]);
		await user.click(getByRole("button", { name: "Remove group ID 1" }));
		expect(latest.docker.groupAdd).toEqual(["110", ""]);
	});

	test("supplementary groups stop at the maximum", async () => {
		await setup({
			docker: { groupAdd: ["1", "2", "3", "4", "5", "6", "7", "8"] },
		});
		expect(getByRole("button", { name: "Add group" })).toBeDisabled();
	});

	test("DNS servers: add up to three, validate, remove", async () => {
		const { user } = await setup({
			docker: { dns: ["1.1.1.1", "dns.example"] },
		});
		expect(
			screen.getByText(
				"dns.example must be an IP address (hostnames are not accepted).",
			),
		).toBeInTheDocument();
		type("DNS server 2", "8.8.8.8");
		await user.click(getByRole("button", { name: "Add DNS server" }));
		expect(latest.docker.dns).toEqual(["1.1.1.1", "8.8.8.8", ""]);
		expect(getByRole("button", { name: "Add DNS server" })).toBeDisabled();
		await user.click(getByRole("button", { name: "Remove DNS server 3" }));
		expect(latest.docker.dns).toEqual(["1.1.1.1", "8.8.8.8"]);
		expect(getByRole("button", { name: "Add DNS server" })).toBeEnabled();
	});

	test("extra hosts: add, edit name and address, validate, remove", async () => {
		const { user } = await setup();
		await user.click(getByRole("button", { name: "Add host" }));
		expect(latest.docker.extraHosts).toEqual([{ name: "", address: "" }]);
		type("Extra host 1 name", "db.internal");
		type("Extra host 1 address", "nowhere");
		expect(latest.docker.extraHosts).toEqual([
			{ name: "db.internal", address: "nowhere" },
		]);
		expect(
			screen.getByText(
				"The address must be an IPv4 or IPv6 address, or host-gateway.",
			),
		).toBeInTheDocument();
		type("Extra host 1 address", "host-gateway");
		expect(
			screen.queryByText(/The address must be an IPv4/),
		).not.toBeInTheDocument();
		await user.click(getByRole("button", { name: "Remove extra host 1" }));
		expect(latest.docker.extraHosts).toEqual([]);
	});

	test("labels: add, edit, validate reserved prefixes, remove", async () => {
		const { user } = await setup();
		await user.click(getByRole("button", { name: "Add label" }));
		type("Label 1 key", "com.docker.compose");
		type("Label 1 value", "x");
		expect(latest.docker.labels).toEqual([
			{ key: "com.docker.compose", value: "x" },
		]);
		expect(
			screen.getByText(
				"com.docker.* labels are managed by NebulaCtrl and cannot be set.",
			),
		).toBeInTheDocument();
		type("Label 1 key", "team");
		expect(
			screen.queryByText(/managed by NebulaCtrl and cannot/),
		).not.toBeInTheDocument();
		expect(buildDockerOptions(latest.docker)?.labels).toEqual({ team: "x" });
		await user.click(getByRole("button", { name: "Remove label 1" }));
		expect(latest.docker.labels).toEqual([]);
	});

	test("tmpfs mounts: add, edit path, size and mode, validate, remove", async () => {
		const { user } = await setup();
		await user.click(getByRole("button", { name: "Add tmpfs" }));
		type("tmpfs 1 path", "relative");
		expect(
			screen.getByText(/The path must be absolute and clean/),
		).toBeInTheDocument();
		type("tmpfs 1 path", "/var/cache/app");
		type("tmpfs 1 size in MiB", "32");
		type("tmpfs 1 mode", "1777");
		expect(latest.docker.tmpfs).toEqual([
			{ path: "/var/cache/app", sizeMb: "32", mode: "1777" },
		]);
		expect(
			screen.queryByText(/The path must be absolute/),
		).not.toBeInTheDocument();
		type("tmpfs 1 mode", "99");
		expect(
			screen.getByText(
				"The mode is octal with three or four digits, like 1777.",
			),
		).toBeInTheDocument();
		await user.click(getByRole("button", { name: "Remove tmpfs mount 1" }));
		expect(latest.docker.tmpfs).toEqual([]);
	});
});

describe("sysctls and host requirements", () => {
	test("a sysctl is picked from the policy's catalog and given a value", async () => {
		const { user } = await setup();
		await user.click(getByRole("button", { name: "Add sysctl" }));
		expect(latest.docker.sysctls).toEqual([{ key: "", value: "" }]);
		const trigger = getByRole("combobox", { name: "Sysctls 1 key" });
		expect(trigger).toHaveTextContent("Choose a sysctl");
		await user.click(trigger);
		expect(getAllByRole("option").map((option) => option.textContent)).toEqual([
			"net.core.somaxconn",
			"net.ipv4.ip_forward",
		]);
		await user.click(getByRole("option", { name: "net.core.somaxconn" }));
		type("Sysctls 1 value", "1024");
		expect(latest.docker.sysctls).toEqual([
			{ key: "net.core.somaxconn", value: "1024" },
		]);
		expect(buildDockerOptions(latest.docker)?.sysctls).toEqual({
			"net.core.somaxconn": "1024",
		});
	});

	test("a key already used in another row cannot be picked again", async () => {
		const { user } = await setup({
			docker: {
				sysctls: [
					{ key: "net.core.somaxconn", value: "1" },
					{ key: "", value: "" },
				],
			},
		});
		await user.click(getByRole("combobox", { name: "Sysctls 2 key" }));
		const used = await findByRole("option", { name: "net.core.somaxconn" });
		expect(used).toHaveAttribute("aria-disabled", "true");
		expect(
			getByRole("option", { name: "net.ipv4.ip_forward" }),
		).not.toHaveAttribute("aria-disabled", "true");
	});

	test("a key the catalog dropped stays on its row and is reported", async () => {
		await setup({
			docker: { sysctls: [{ key: "kernel.old", value: "1" }] },
		});
		expect(getByRole("combobox", { name: "Sysctls 1 key" })).toHaveTextContent(
			"kernel.old",
		);
		expect(
			screen.getByText("kernel.old is not an allowed sysctl."),
		).toBeInTheDocument();
	});

	test("a host-changing sysctl points to host requirements", async () => {
		await setup({
			docker: { sysctls: [{ key: "vm.max_map_count", value: "1" }] },
		});
		expect(
			screen.getByText(
				/vm\.max_map_count changes the host and cannot be set from a container/,
			),
		).toBeInTheDocument();
	});

	test("a sysctl row can be removed and its value is validated", async () => {
		const { user } = await setup({
			docker: { sysctls: [{ key: "net.core.somaxconn", value: "many" }] },
		});
		expect(
			screen.getByText(
				/The value is a number, or numbers separated by single spaces/,
			),
		).toBeInTheDocument();
		await user.click(getByRole("button", { name: "Remove sysctl 1" }));
		expect(latest.docker.sysctls).toEqual([]);
	});

	test("host requirements use their own catalog, as numbers", async () => {
		const { user } = await setup();
		await user.click(getByRole("button", { name: "Add requirement" }));
		await user.click(
			getByRole("combobox", { name: "Host requirements 1 key" }),
		);
		await user.click(getByRole("option", { name: "vm.max_map_count" }));
		type("Host requirements 1 value", "262144");
		expect(buildDockerOptions(latest.docker)?.hostSysctls).toEqual({
			"vm.max_map_count": 262144,
		});
		type("Host requirements 1 value", "0");
		expect(
			screen.getByText(/The minimum is a whole number from 1 to/),
		).toBeInTheDocument();
	});

	test("host requirements stop at the number of known parameters", async () => {
		await setup({
			docker: {
				hostSysctls: [
					{ key: "vm.max_map_count", value: "1" },
					{ key: "fs.file-max", value: "1" },
				],
			},
		});
		expect(getByRole("button", { name: "Add requirement" })).toBeDisabled();
		expect(getByRole("button", { name: "Add sysctl" })).toBeEnabled();
	});

	test("while the policy loads the catalogs say so and adding is off", async () => {
		await setup({ policy: "pending" });
		expect(screen.getAllByText("Loading the supported keys…")).toHaveLength(2);
		expect(getByRole("button", { name: "Add sysctl" })).toBeDisabled();
		expect(getByRole("button", { name: "Add requirement" })).toBeDisabled();
	});

	test("when the policy fails the catalogs ask to reload", async () => {
		await setup({ policy: "failed" });
		expect(
			await screen.findAllByText(
				"Could not load the supported keys. Reload to try again.",
			),
		).toHaveLength(2);
		expect(getByRole("button", { name: "Add sysctl" })).toBeDisabled();
	});
});

describe("server error", () => {
	test("shows what the control plane said about the options", async () => {
		const message = dockerErrorMessage(
			new ApiError(422, REQUIRES_UNRESTRICTED, "refused", {
				fields: ["privileged", "capAdd NET_ADMIN"],
			}),
		);
		expect(message).toBe(
			"These options need a server an instance admin made Unrestricted: privileged mode and the NET_ADMIN capability. Choose such a server, or remove these options.",
		);
		await setup({ serverError: message });
		const alert = getByRole("alert");
		expect(alert).toHaveTextContent(message ?? "");
	});

	test("without a server error there is no alert", async () => {
		await setup();
		expect(queryByRole("alert")).not.toBeInTheDocument();
	});
});

describe("host access group", () => {
	const summary = () => screen.getByText("Host access").closest("details");

	test("is closed on a restricted server and open on an unrestricted one", async () => {
		await setup({ unrestricted: false });
		await waitFor(() =>
			expect(screen.getByText(/hetzner-01 is not\./)).toBeInTheDocument(),
		);
		expect(summary()?.open).toBe(false);
	});

	test("opens by itself on an unrestricted server", async () => {
		await setup({ unrestricted: true });
		await waitFor(() => expect(summary()?.open).toBe(true));
		expect(
			screen.getByText("Host access is on for hetzner-01"),
		).toBeInTheDocument();
	});

	test("opens on a restricted server when host-access options are set, and offers to remove them", async () => {
		const { user } = await setup({
			unrestricted: false,
			docker: { privileged: true, hostname: "keep" },
		});
		await waitFor(() => expect(summary()?.open).toBe(true));
		expect(screen.getByText("The next deploy will fail")).toBeInTheDocument();
		await user.click(
			getByRole("button", { name: "Remove host access options" }),
		);
		expect(latest.docker.privileged).toBe(false);
		expect(latest.docker.hostname).toBe("keep");
		expect(
			screen.queryByText("The next deploy will fail"),
		).not.toBeInTheDocument();
	});

	test("a service with no server chosen asks to choose one", async () => {
		await setup({ serverId: "" });
		expect(
			screen.getByText(/Choose a server under General first\./),
		).toBeInTheDocument();
	});

	test("privileged mode can be switched on once the server is unrestricted", async () => {
		const { user } = await setup({ unrestricted: true });
		const privileged = getByRole("switch", { name: "Privileged" });
		await waitFor(() =>
			expect(privileged).not.toHaveAttribute("aria-disabled", "true"),
		);
		await user.click(privileged);
		expect(latest.docker.privileged).toBe(true);
		expect(buildDockerOptions(latest.docker)).toEqual({ privileged: true });
	});
});
