import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { Server } from "#/api/models";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import {
	type HostAccess,
	HostAccessGroup,
	useHostAccess,
} from "./docker-host-access";
import {
	type DockerForm,
	type DockerIssue,
	type DockerPolicy,
	EMPTY_DOCKER_FORM,
	FALLBACK_POLICY,
	HOST_ACCESS_WARNINGS,
	MAX_DOCKER_DEVICES,
} from "./docker-options";

const server = (patch: Partial<Server> = {}): Server =>
	({
		id: "server-1",
		name: "hetzner-01",
		unrestricted: false,
		...patch,
	}) as Server;

const unrestricted: HostAccess = {
	kind: "unrestricted",
	server: server({ unrestricted: true }),
};
const restricted: HostAccess = { kind: "restricted", server: server() };
const none: HostAccess = { kind: "none", loading: false };

const policy: DockerPolicy = {
	...FALLBACK_POLICY,
	gpuCapabilities: ["compute", "video"],
};

let latest: DockerForm = EMPTY_DOCKER_FORM;

function Harness({
	access,
	initial = {},
	issues = [],
}: {
	access: HostAccess;
	initial?: Partial<DockerForm>;
	issues?: DockerIssue[];
}) {
	const [form, setForm] = useState<DockerForm>({
		...EMPTY_DOCKER_FORM,
		...initial,
	});
	latest = form;
	return (
		<HostAccessGroup
			form={form}
			issues={issues}
			policy={policy}
			access={access}
			update={(patch) => setForm({ ...form, ...patch })}
		/>
	);
}

const device = {
	hostPath: "/dev/dri/renderD128",
	containerPath: "",
	permissions: "",
};

describe("HostAccessGroup on an unrestricted server", () => {
	test("says host access is on for the server and enables every control", async () => {
		await renderWithProviders(<Harness access={unrestricted} />);
		expect(
			screen.getByText("Host access is on for hetzner-01"),
		).toBeInTheDocument();
		expect(
			screen.queryByText("The next deploy will fail"),
		).not.toBeInTheDocument();
		for (const name of [
			"Run in privileged mode",
			"Share the host's PID namespace",
			"Run without a seccomp profile",
			"Run without an AppArmor profile",
		]) {
			expect(screen.getByText(name)).toBeInTheDocument();
		}
		const switches = screen.getAllByRole("switch");
		expect(switches).toHaveLength(4);
		for (const toggle of switches) {
			expect(toggle).not.toHaveAttribute("aria-disabled", "true");
		}
		expect(screen.getByRole("button", { name: "Add device" })).toBeEnabled();
		expect(screen.getByLabelText("Runtime")).toBeEnabled();
		expect(screen.getByRole("button", { name: "All" })).toBeEnabled();
	});

	test("each switch sets its own option and shows its warning once on", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Harness access={unrestricted} />);
		expect(
			screen.queryByText(HOST_ACCESS_WARNINGS.privileged),
		).not.toBeInTheDocument();

		await user.click(screen.getByRole("switch", { name: "Privileged" }));
		expect(latest.privileged).toBe(true);
		expect(
			screen.getByText(HOST_ACCESS_WARNINGS.privileged),
		).toBeInTheDocument();

		await user.click(
			screen.getByRole("switch", { name: "Host PID namespace" }),
		);
		expect(latest.pidHost).toBe(true);
		expect(screen.getByText(HOST_ACCESS_WARNINGS.pidHost)).toBeInTheDocument();

		await user.click(screen.getByRole("switch", { name: "Seccomp" }));
		expect(latest.seccompUnconfined).toBe(true);
		expect(latest.apparmorUnconfined).toBe(false);
		expect(screen.getAllByText(HOST_ACCESS_WARNINGS.unconfined)).toHaveLength(
			1,
		);

		await user.click(screen.getByRole("switch", { name: "AppArmor" }));
		expect(latest.apparmorUnconfined).toBe(true);
		expect(screen.getAllByText(HOST_ACCESS_WARNINGS.unconfined)).toHaveLength(
			2,
		);

		await user.click(screen.getByRole("switch", { name: "Privileged" }));
		expect(latest.privileged).toBe(false);
		expect(
			screen.queryByText(HOST_ACCESS_WARNINGS.privileged),
		).not.toBeInTheDocument();
	});

	test("the runtime field edits the runtime and shows its problem", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness
				access={unrestricted}
				issues={[{ field: "runtime", message: "Runtime has odd characters." }]}
			/>,
		);
		const runtime = screen.getByLabelText("Runtime");
		expect(runtime).toHaveAttribute("aria-invalid", "true");
		expect(screen.getByText("Runtime has odd characters.")).toBeInTheDocument();
		await user.type(runtime, "runsc");
		expect(latest.runtime).toBe("runsc");
	});
});

describe("HostAccessGroup on a restricted server", () => {
	test("explains the server is not unrestricted, links to it and disables everything", async () => {
		await renderWithProviders(<Harness access={restricted} />);
		expect(
			screen.getByText(
				/work only on a server an instance admin made unrestricted/,
			),
		).toBeInTheDocument();
		expect(screen.getByText(/hetzner-01 is not\./)).toBeInTheDocument();
		expect(
			screen.getByRole("link", { name: "Open hetzner-01" }),
		).toHaveAttribute("href", "/servers/server-1/settings/danger");
		expect(
			screen.queryByText("Host access is on for hetzner-01"),
		).not.toBeInTheDocument();
		// Nothing is set, so there is nothing to warn about.
		expect(
			screen.queryByText("The next deploy will fail"),
		).not.toBeInTheDocument();
		const switches = screen.getAllByRole("switch");
		expect(switches).toHaveLength(4);
		for (const toggle of switches) {
			expect(toggle).toHaveAttribute("aria-disabled", "true");
		}
		expect(screen.getByRole("button", { name: "Add device" })).toBeDisabled();
		expect(screen.getByLabelText("Runtime")).toBeDisabled();
		expect(screen.getByRole("button", { name: "All" })).toBeDisabled();
		expect(screen.getByRole("button", { name: "Count" })).toBeDisabled();
	});

	test("options already set warn that the next deploy fails, and one key removes them", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness
				access={restricted}
				initial={{
					privileged: true,
					pidHost: true,
					seccompUnconfined: true,
					apparmorUnconfined: true,
					runtime: "runsc",
					devices: [device],
					gpus: { mode: "all", count: "", capabilities: ["video"] },
					capAdd: ["CHOWN", "NET_ADMIN"],
					hostname: "keep-me",
				}}
			/>,
		);
		expect(screen.getByText("The next deploy will fail")).toBeInTheDocument();
		expect(
			screen.getByText(/hetzner-01 is not unrestricted/),
		).toBeInTheDocument();

		await user.click(
			screen.getByRole("button", { name: "Remove host access options" }),
		);

		expect(latest.privileged).toBe(false);
		expect(latest.pidHost).toBe(false);
		expect(latest.seccompUnconfined).toBe(false);
		expect(latest.apparmorUnconfined).toBe(false);
		expect(latest.runtime).toBe("");
		expect(latest.devices).toEqual([]);
		expect(latest.gpus.mode).toBe("none");
		expect(latest.capAdd).toEqual(["CHOWN"]);
		// Options that work anywhere are untouched.
		expect(latest.hostname).toBe("keep-me");
		expect(
			screen.queryByText("The next deploy will fail"),
		).not.toBeInTheDocument();
	});

	test("only NET_ADMIN set is enough to warn", async () => {
		await renderWithProviders(
			<Harness access={restricted} initial={{ capAdd: ["NET_ADMIN"] }} />,
		);
		expect(screen.getByText("The next deploy will fail")).toBeInTheDocument();
	});
});

describe("HostAccessGroup with no server chosen", () => {
	test("asks to choose a server first and has no server link", async () => {
		await renderWithProviders(<Harness access={none} />);
		expect(
			screen.getByText(/Choose a server under General first\./),
		).toBeInTheDocument();
		expect(screen.queryByRole("link")).not.toBeInTheDocument();
		expect(screen.getByRole("switch", { name: "Privileged" })).toHaveAttribute(
			"aria-disabled",
			"true",
		);
	});

	test("options already set say no unrestricted server is chosen", async () => {
		await renderWithProviders(
			<Harness access={none} initial={{ privileged: true }} />,
		);
		expect(
			screen.getByText(/no unrestricted server is chosen/),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Remove host access options" }),
		).toBeInTheDocument();
	});
});

describe("devices", () => {
	test("adds, edits and removes rows", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Harness access={unrestricted} />);
		expect(
			screen.queryByRole("list", { name: "Devices" }),
		).not.toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "Add device" }));
		expect(latest.devices).toEqual([
			{ hostPath: "", containerPath: "", permissions: "" },
		]);
		// An empty row is not worth a warning.
		expect(
			screen.queryByText(HOST_ACCESS_WARNINGS.hardware),
		).not.toBeInTheDocument();

		await user.type(
			screen.getByLabelText("Device 1 host path"),
			"/dev/dri/renderD128",
		);
		await user.type(
			screen.getByLabelText("Device 1 container path"),
			"/dev/gpu",
		);
		await user.type(screen.getByLabelText("Device 1 permissions"), "rw");
		expect(latest.devices).toEqual([
			{
				hostPath: "/dev/dri/renderD128",
				containerPath: "/dev/gpu",
				permissions: "rw",
			},
		]);
		expect(screen.getByText(HOST_ACCESS_WARNINGS.hardware)).toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "Add device" }));
		expect(latest.devices).toHaveLength(2);
		await user.click(screen.getByRole("button", { name: "Remove device 1" }));
		expect(latest.devices).toEqual([
			{ hostPath: "", containerPath: "", permissions: "" },
		]);
		expect(
			screen.queryByText(HOST_ACCESS_WARNINGS.hardware),
		).not.toBeInTheDocument();
	});

	test("a row with only a container path still warns", async () => {
		await renderWithProviders(
			<Harness
				access={unrestricted}
				initial={{
					devices: [{ hostPath: "", containerPath: "/dev/x", permissions: "" }],
				}}
			/>,
		);
		expect(screen.getByText(HOST_ACCESS_WARNINGS.hardware)).toBeInTheDocument();
	});

	test("shows each row's problem and the list-wide one", async () => {
		await renderWithProviders(
			<Harness
				access={unrestricted}
				initial={{ devices: [device, { ...device, hostPath: "/etc/passwd" }] }}
				issues={[
					{
						field: "devices",
						row: 1,
						message: "/etc/passwd is not under /dev.",
					},
					{ field: "devices", message: "At most 16 devices." },
				]}
			/>,
		);
		expect(
			screen.getByText("/etc/passwd is not under /dev."),
		).toBeInTheDocument();
		expect(screen.getByText("At most 16 devices.")).toBeInTheDocument();
		expect(screen.getByLabelText("Device 1 host path")).toHaveAttribute(
			"aria-invalid",
			"false",
		);
		expect(screen.getByLabelText("Device 2 host path")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
	});

	test("stops adding at the maximum", async () => {
		await renderWithProviders(
			<Harness
				access={unrestricted}
				initial={{
					devices: Array.from({ length: MAX_DOCKER_DEVICES }, (_, i) => ({
						hostPath: `/dev/d${i}`,
						containerPath: "",
						permissions: "",
					})),
				}}
			/>,
		);
		expect(screen.getByRole("button", { name: "Add device" })).toBeDisabled();
	});

	test("on a restricted server the rows are visible but locked", async () => {
		await renderWithProviders(
			<Harness access={restricted} initial={{ devices: [device] }} />,
		);
		expect(screen.getByLabelText("Device 1 host path")).toBeDisabled();
		expect(screen.getByLabelText("Device 1 container path")).toBeDisabled();
		expect(screen.getByLabelText("Device 1 permissions")).toBeDisabled();
		expect(
			screen.getByRole("button", { name: "Remove device 1" }),
		).toBeDisabled();
	});
});

describe("GPUs", () => {
	test("none shows no capabilities, count or warning", async () => {
		await renderWithProviders(<Harness access={unrestricted} />);
		expect(
			screen.queryByRole("group", { name: "GPU capabilities" }),
		).not.toBeInTheDocument();
		expect(screen.queryByLabelText("Number of GPUs")).not.toBeInTheDocument();
		expect(
			screen.queryByText(HOST_ACCESS_WARNINGS.hardware),
		).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "None" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
	});

	test("all shows the toolkit capabilities, the default note and the warning", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Harness access={unrestricted} />);
		await user.click(screen.getByRole("button", { name: "All" }));
		expect(latest.gpus.mode).toBe("all");
		expect(screen.queryByLabelText("Number of GPUs")).not.toBeInTheDocument();
		const caps = screen.getByRole("group", { name: "GPU capabilities" });
		expect(
			within(caps)
				.getAllByRole("button")
				.map((button) => button.textContent),
		).toEqual(["compute", "video"]);
		expect(
			screen.getByText("No capability selected means the toolkit's default."),
		).toBeInTheDocument();
		expect(screen.getByText(HOST_ACCESS_WARNINGS.hardware)).toBeInTheDocument();
	});

	test("capabilities toggle on and off in sorted order", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness
				access={unrestricted}
				initial={{ gpus: { mode: "all", count: "", capabilities: [] } }}
			/>,
		);
		await user.click(screen.getByRole("button", { name: "video" }));
		await user.click(screen.getByRole("button", { name: "compute" }));
		expect(latest.gpus.capabilities).toEqual(["compute", "video"]);
		expect(screen.getByRole("button", { name: "video" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		await user.click(screen.getByRole("button", { name: "video" }));
		expect(latest.gpus.capabilities).toEqual(["compute"]);
		expect(screen.getByRole("button", { name: "video" })).toHaveAttribute(
			"aria-pressed",
			"false",
		);
	});

	test("count asks for the number and shows its problem", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness
				access={unrestricted}
				issues={[{ field: "gpus", message: "Use 1 to 16 GPUs." }]}
				initial={{ gpus: { mode: "none", count: "", capabilities: [] } }}
			/>,
		);
		// The problem belongs to the GPUs row even before the number is shown.
		expect(screen.getByText("Use 1 to 16 GPUs.")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Count" }));
		expect(latest.gpus.mode).toBe("count");
		const count = screen.getByLabelText("Number of GPUs");
		expect(count).toHaveAttribute("placeholder", "1-16");
		expect(count).toHaveAttribute("aria-invalid", "true");
		await user.type(count, "2");
		expect(latest.gpus.count).toBe("2");
	});

	test("going back to none hides the options but keeps nothing pressed", async () => {
		const user = userEvent.setup();
		await renderWithProviders(
			<Harness
				access={unrestricted}
				initial={{ gpus: { mode: "all", count: "", capabilities: ["video"] } }}
			/>,
		);
		await user.click(screen.getByRole("button", { name: "None" }));
		expect(latest.gpus.mode).toBe("none");
		expect(
			screen.queryByRole("group", { name: "GPU capabilities" }),
		).not.toBeInTheDocument();
	});

	test("capabilities come from the policy when known", async () => {
		await renderWithProviders(
			<Harness
				access={unrestricted}
				initial={{ gpus: { mode: "all", count: "", capabilities: [] } }}
			/>,
		);
		expect(
			screen.queryByRole("button", { name: "graphics" }),
		).not.toBeInTheDocument();
	});

	test("on a restricted server the chosen GPU capabilities are locked", async () => {
		await renderWithProviders(
			<Harness
				access={restricted}
				initial={{
					gpus: { mode: "count", count: "2", capabilities: ["video"] },
				}}
			/>,
		);
		expect(screen.getByLabelText("Number of GPUs")).toBeDisabled();
		expect(screen.getByRole("button", { name: "video" })).toBeDisabled();
		expect(screen.getByRole("button", { name: "compute" })).toBeDisabled();
	});
});

function Probe({ serverId }: { serverId: string }) {
	const access = useHostAccess(serverId);
	return (
		<p data-testid="access">
			{access.kind}
			{access.kind === "none" ? ` loading=${access.loading}` : ""}
			{access.kind !== "none" ? ` ${access.server.name}` : ""}
		</p>
	);
}

describe("useHostAccess", () => {
	const serverList = make("PageServer", {
		items: [
			make("Server", { id: "open", name: "lab", unrestricted: true }),
			make("Server", { id: "closed", name: "prod", unrestricted: false }),
		],
	});

	test("no server id is none, without asking whether it is loading", async () => {
		stubApi({ "GET /servers": { body: serverList } });
		await renderWithProviders(<Probe serverId="" />);
		expect(screen.getByTestId("access")).toHaveTextContent(
			"none loading=false",
		);
	});

	test("an unrestricted server is unrestricted", async () => {
		stubApi({ "GET /servers": { body: serverList } });
		await renderWithProviders(<Probe serverId="open" />);
		expect(await screen.findByText("unrestricted lab")).toBeInTheDocument();
	});

	test("a normal server is restricted", async () => {
		stubApi({ "GET /servers": { body: serverList } });
		await renderWithProviders(<Probe serverId="closed" />);
		expect(await screen.findByText("restricted prod")).toBeInTheDocument();
	});

	test("while the list loads the server is none and loading", async () => {
		stubApi({
			"GET /servers": () => new Promise<Response>(() => {}),
		});
		await renderWithProviders(<Probe serverId="open" />);
		expect(screen.getByTestId("access")).toHaveTextContent("none loading=true");
	});

	test("a server that is no longer listed is none and not loading", async () => {
		stubApi({ "GET /servers": { body: serverList } });
		await renderWithProviders(<Probe serverId="gone" />);
		expect(await screen.findByText("none loading=false")).toBeInTheDocument();
	});
});
