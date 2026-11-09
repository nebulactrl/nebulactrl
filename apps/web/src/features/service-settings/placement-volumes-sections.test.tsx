import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import type { Server } from "#/api/gen/types.gen";
import type { Service } from "#/api/models";
import { makeService } from "#/features/canvas/lib/fixtures";
import { serverWire } from "#/features/servers/server.fixture";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import { type SettingsDraft, toDraft } from "./draft";
import type { DraftErrors } from "./fields";
import { PlacementSection, VolumesSection } from "./placement-volumes-sections";

/** A /servers item: online, accepting workloads, no metrics sample yet. */
function server(over: Partial<Server> & { id: string; name: string }): Server {
	return serverWire({ latest: null, ...over });
}

const SAMPLE = {
	cpuPercent: 12.4,
	memUsed: 512,
	memTotal: 2048,
	diskUsed: 30,
	diskTotal: 120,
	sampledAt: "2026-01-01T00:00:00Z",
};

const POLICY = {
	allowedSysctls: [],
	gpuCapabilities: [],
	hostSysctlKeys: [],
	knownCapabilities: [],
	reservedLabels: [],
	safeCapabilities: [],
	stopSignals: [],
	unrestrictedCapabilities: ["NET_ADMIN"],
	unrestrictedFields: [],
	warnCapabilities: [],
};

interface Mounted {
	draft: () => SettingsDraft;
}

function PlacementHarness({
	target,
	draftOver,
	errors,
	serverError,
	seen,
}: {
	target: Service;
	draftOver: Partial<SettingsDraft> | undefined;
	errors: DraftErrors;
	serverError: string | undefined;
	seen: Mounted;
}) {
	const [draft, setDraft] = useState<SettingsDraft>({
		...toDraft(target),
		...draftOver,
	});
	seen.draft = () => draft;
	return (
		<PlacementSection
			service={target}
			draft={draft}
			errors={errors}
			serverError={serverError}
			set={(key, value) => setDraft((d) => ({ ...d, [key]: value }))}
		/>
	);
}

describe("PlacementSection", () => {
	const service = makeService({
		id: "svc-1",
		serverId: "srv-a",
		server: {
			id: "srv-a",
			name: "alpha",
			status: "online",
			lastSeenAt: null,
		},
	});

	function mount(
		options: {
			servers?: Server[];
			draftOver?: Partial<SettingsDraft>;
			service?: typeof service;
			errors?: DraftErrors;
			serverError?: string;
			serversReply?: () => Response | Promise<Response>;
		} = {},
	) {
		const api = stubApi({
			"GET /servers": options.serversReply ?? {
				body: { items: options.servers ?? [] },
			},
			"GET /instance/docker-options": { body: POLICY },
		});
		const target = options.service ?? service;
		const seen: Mounted = { draft: () => toDraft(target) };
		return {
			api,
			seen,
			ui: (
				<PlacementHarness
					target={target}
					draftOver={options.draftOver}
					errors={options.errors ?? {}}
					serverError={options.serverError}
					seen={seen}
				/>
			),
		};
	}

	async function openList() {
		const user = userEvent.setup();
		await user.click(await screen.findByRole("combobox", { name: "Server" }));
		return user;
	}

	test("shows the loading placeholder until servers arrive", async () => {
		const { promise: gate, resolve: release } = Promise.withResolvers<void>();
		const { ui } = mount({
			draftOver: { serverId: "" },
			service: makeService({ serverId: null, server: null }),
			serversReply: async () => {
				await gate;
				return Response.json({ items: [] });
			},
		});
		await renderWithProviders(ui);
		expect(screen.getByRole("combobox", { name: "Server" })).toHaveTextContent(
			"Loading servers…",
		);
		release();
		await waitFor(() =>
			expect(
				screen.getByRole("combobox", { name: "Server" }),
			).toHaveTextContent("Choose a server"),
		);
	});

	test("shows the current server by name and status on the trigger", async () => {
		const { ui, api } = mount({
			servers: [
				server({ id: "srv-a", name: "alpha" }),
				server({ id: "srv-b", name: "beta" }),
			],
		});
		await renderWithProviders(ui);
		await waitFor(() =>
			expect(
				screen.getByRole("combobox", { name: "Server" }),
			).toHaveTextContent("alpha (online)"),
		);
		expect(api.calls.some((c) => c.path === "/servers")).toBe(true);
		expect(
			api.calls.find((c) => c.path === "/servers")?.query.get("limit"),
		).toBe("200");
	});

	test("lists usable servers with their headroom and hides revoked and pending ones", async () => {
		const { ui } = mount({
			servers: [
				server({ id: "srv-a", name: "alpha", latest: SAMPLE }),
				server({ id: "srv-b", name: "beta" }),
				server({ id: "srv-c", name: "gamma", status: "offline" }),
				server({ id: "srv-d", name: "delta", status: "revoked" }),
				server({ id: "srv-e", name: "epsilon", status: "pending" }),
			],
		});
		await renderWithProviders(ui);
		await openList();
		const alpha = await screen.findByRole("option", { name: /alpha/ });
		expect(alpha).toHaveTextContent("CPU 12% · mem 25% · disk 25%");
		expect(screen.getByRole("option", { name: /beta/ })).toHaveTextContent(
			"no sample yet",
		);
		expect(screen.getByRole("option", { name: /gamma/ })).toHaveTextContent(
			"offline",
		);
		expect(screen.queryByRole("option", { name: /delta/ })).toBeNull();
		expect(screen.queryByRole("option", { name: /epsilon/ })).toBeNull();
		expect(screen.getAllByRole("option")).toHaveLength(3);
	});

	test("a draining, drained or workload-refusing server is shown but cannot be chosen", async () => {
		const { ui, seen } = mount({
			servers: [
				server({ id: "srv-a", name: "alpha" }),
				server({ id: "srv-b", name: "draining-1", drainState: "draining" }),
				server({ id: "srv-c", name: "drained-1", drainState: "drained" }),
				server({ id: "srv-d", name: "closed-1", acceptWorkloads: false }),
				server({
					id: "srv-e",
					name: "builder-1",
					acceptWorkloads: false,
					buildEnabled: true,
				}),
			],
		});
		await renderWithProviders(ui);
		const user = await openList();
		const draining = await screen.findByRole("option", { name: /draining-1/ });
		expect(draining).toHaveAttribute("aria-disabled", "true");
		expect(draining).toHaveTextContent("being drained: not accepting services");
		expect(screen.getByRole("option", { name: /drained-1/ })).toHaveTextContent(
			"drained: not accepting services",
		);
		expect(screen.getByRole("option", { name: /closed-1/ })).toHaveTextContent(
			"does not accept workloads: not accepting services",
		);
		expect(screen.getByRole("option", { name: /builder-1/ })).toHaveTextContent(
			"build server: not accepting services",
		);
		expect(screen.getByRole("option", { name: /alpha/ })).not.toHaveAttribute(
			"aria-disabled",
			"true",
		);

		await user.click(draining);
		expect(seen.draft().serverId).toBe("srv-a");
	});

	test("choosing another server stages it and warns that the move needs a deploy", async () => {
		const { ui, seen } = mount({
			servers: [
				server({ id: "srv-a", name: "alpha" }),
				server({ id: "srv-b", name: "beta" }),
			],
		});
		await renderWithProviders(ui);
		await waitFor(() =>
			expect(
				screen.getByRole("combobox", { name: "Server" }),
			).toHaveTextContent("alpha"),
		);
		expect(screen.queryByText("Move to server beta")).toBeNull();

		const user = await openList();
		await user.click(await screen.findByRole("option", { name: /beta/ }));

		expect(seen.draft().serverId).toBe("srv-b");
		expect(await screen.findByText("Move to server beta")).toBeInTheDocument();
		const alert = screen.getByText(/Creates a new deployment on beta/);
		expect(alert).toHaveTextContent("volumes are not migrated");
		expect(alert).toHaveTextContent(
			"the current container keeps serving from alpha",
		);
	});

	test("moving back to the current server removes the warning", async () => {
		const { ui, seen } = mount({
			servers: [
				server({ id: "srv-a", name: "alpha" }),
				server({ id: "srv-b", name: "beta" }),
			],
			draftOver: { serverId: "srv-b" },
		});
		await renderWithProviders(ui);
		expect(await screen.findByText("Move to server beta")).toBeInTheDocument();
		const user = await openList();
		await user.click(await screen.findByRole("option", { name: /alpha/ }));
		expect(seen.draft().serverId).toBe("srv-a");
		await waitFor(() =>
			expect(screen.queryByText("Move to server beta")).toBeNull(),
		);
	});

	test("a service without a server yet is not warned about a move", async () => {
		const { ui, seen } = mount({
			service: makeService({ serverId: null, server: null }),
			draftOver: { serverId: "" },
			servers: [server({ id: "srv-b", name: "beta" })],
		});
		await renderWithProviders(ui);
		const user = await openList();
		await user.click(await screen.findByRole("option", { name: /beta/ }));
		expect(seen.draft().serverId).toBe("srv-b");
		expect(screen.queryByText(/Move to server/)).toBeNull();
	});

	test("falls back to 'its server' when the service has no server record", async () => {
		const { ui } = mount({
			service: makeService({ serverId: "srv-a", server: null }),
			draftOver: { serverId: "srv-b" },
			servers: [
				server({ id: "srv-a", name: "alpha" }),
				server({ id: "srv-b", name: "beta" }),
			],
		});
		await renderWithProviders(ui);
		expect(
			await screen.findByText(/keeps serving from its server until/),
		).toBeInTheDocument();
	});

	test("Docker options that need an unrestricted server name them and disable other servers", async () => {
		const base = toDraft(service);
		const { ui } = mount({
			draftOver: { docker: { ...base.docker, privileged: true } },
			servers: [
				server({ id: "srv-a", name: "alpha" }),
				server({ id: "srv-b", name: "beta", unrestricted: true }),
				server({ id: "srv-c", name: "gamma" }),
			],
		});
		await renderWithProviders(ui);
		expect(
			await screen.findByText(
				/The Docker options of this service \(privileged mode\) need a server an instance admin made Unrestricted/,
			),
		).toBeInTheDocument();
		await openList();
		// The server it runs on stays choosable even if restricted; others are not.
		const alpha = await screen.findByRole("option", { name: /alpha/ });
		expect(alpha).not.toHaveAttribute("aria-disabled", "true");
		const gamma = screen.getByRole("option", { name: /gamma/ });
		expect(gamma).toHaveAttribute("aria-disabled", "true");
		expect(gamma).toHaveTextContent("needs an unrestricted server");
		const beta = screen.getByRole("option", { name: /beta/ });
		expect(beta).not.toHaveAttribute("aria-disabled", "true");
	});

	test("lists every field that needs the unrestricted tier, including capabilities from the policy", async () => {
		const base = toDraft(service);
		const { ui } = mount({
			draftOver: {
				docker: {
					...base.docker,
					pidHost: true,
					capAdd: ["net_admin", "CHOWN"],
				},
			},
			servers: [server({ id: "srv-a", name: "alpha", unrestricted: true })],
		});
		await renderWithProviders(ui);
		await waitFor(() =>
			expect(
				screen.getByText(/The Docker options of this service/),
			).toHaveTextContent("(the host PID namespace, the NET_ADMIN capability)"),
		);
	});

	test("without such options no unrestricted notice is shown and every server is choosable", async () => {
		const { ui } = mount({
			servers: [
				server({ id: "srv-a", name: "alpha" }),
				server({ id: "srv-b", name: "beta" }),
			],
		});
		await renderWithProviders(ui);
		await openList();
		await screen.findByRole("option", { name: /beta/ });
		expect(screen.queryByText(/instance admin made Unrestricted/)).toBeNull();
		for (const option of screen.getAllByRole("option")) {
			expect(option).not.toHaveAttribute("aria-disabled", "true");
		}
	});

	test("shows a server-side refusal (serverError) as an alert", async () => {
		const { ui } = mount({
			servers: [server({ id: "srv-a", name: "alpha" })],
			serverError:
				"Server beta is not unrestricted: privileged mode is refused.",
		});
		await renderWithProviders(ui);
		expect(
			await screen.findByText(
				"Server beta is not unrestricted: privileged mode is refused.",
			),
		).toBeInTheDocument();
	});

	test("shows no refusal alert without a serverError", async () => {
		const { ui } = mount({ servers: [server({ id: "srv-a", name: "alpha" })] });
		await renderWithProviders(ui);
		await screen.findByRole("combobox", { name: "Server" });
		expect(screen.queryByRole("alert")).toBeNull();
	});

	test("shows the draft's serverId error on the select", async () => {
		const { ui } = mount({
			servers: [server({ id: "srv-a", name: "alpha" })],
			errors: { serverId: "Pick a server." },
		});
		await renderWithProviders(ui);
		expect(await screen.findByText("Pick a server.")).toBeInTheDocument();
		expect(screen.getByRole("combobox", { name: "Server" })).toHaveAttribute(
			"aria-invalid",
			"true",
		);
	});
});

interface VolumeRow {
	id: string;
	name: string;
	mountPath: string;
	dockerName: string;
	restoreLabel?: string;
	owner?: string;
}

function volume(over: Partial<VolumeRow> = {}): VolumeRow {
	return {
		id: "vol-1",
		name: "data",
		mountPath: "/var/lib/data",
		dockerName: "nebula-svc-1-data",
		...over,
	};
}

describe("VolumesSection", () => {
	const service = makeService({ id: "svc-1" });

	test("says data is lost on every deploy when there are no volumes", async () => {
		const api = stubApi({
			"GET /services/:serviceId/volumes": { body: { items: [] } },
			"GET /services/:serviceId/retired-volumes": { body: { items: [] } },
		});
		await renderWithProviders(<VolumesSection service={service} />);
		expect(
			await screen.findByText(
				"No volumes. Data inside the container is lost on every deploy.",
			),
		).toBeInTheDocument();
		expect(screen.queryByRole("list", { name: "Volumes" })).toBeNull();
		expect(api.calls.map((c) => c.path)).toContain("/services/svc-1/volumes");
		expect(screen.getByRole("button", { name: "Add" })).toBeDisabled();
	});

	test("lists each volume with its mount path, names and restore note", async () => {
		stubApi({
			"GET /services/:serviceId/volumes": {
				body: {
					items: [
						volume(),
						volume({
							id: "vol-2",
							name: "cache",
							mountPath: "/cache",
							dockerName: "nebula-svc-1-cache",
							restoreLabel: "Restored from backup 2026-10-01",
						}),
					],
				},
			},
			"GET /services/:serviceId/retired-volumes": { body: { items: [] } },
		});
		await renderWithProviders(<VolumesSection service={service} />);
		const list = await screen.findByRole("list", { name: "Volumes" });
		const rows = within(list).getAllByRole("listitem");
		expect(rows).toHaveLength(2);
		expect(rows[0]).toHaveTextContent("/var/lib/data");
		expect(rows[0]).toHaveTextContent("data · nebula-svc-1-data");
		expect(rows[0]).not.toHaveTextContent("Restored");
		expect(rows[1]).toHaveTextContent("/cache");
		expect(rows[1]).toHaveTextContent("cache · nebula-svc-1-cache");
		expect(rows[1]).toHaveTextContent("Restored from backup 2026-10-01");
	});

	test("lists the volumes a restore retired under the form", async () => {
		stubApi({
			"GET /services/:serviceId/volumes": { body: { items: [] } },
			"GET /services/:serviceId/retired-volumes": {
				body: {
					items: [
						{
							id: "rv-1",
							serviceId: "svc-1",
							dockerName: "nebula-data-old-1",
							volumeName: "data",
							createdAt: "2026-10-30T10:00:00Z",
						},
					],
				},
			},
		});
		await renderWithProviders(<VolumesSection service={service} />);
		const list = await screen.findByRole("list", { name: "Retired volumes" });
		expect(list).toHaveTextContent("nebula-data-old-1");
	});

	test("adds a volume with trimmed name and path, then clears the form, refreshes and toasts", async () => {
		const user = userEvent.setup();
		let items: VolumeRow[] = [];
		const api = stubApi({
			"GET /services/:serviceId/volumes": () => Response.json({ items }),
			"GET /services/:serviceId/retired-volumes": { body: { items: [] } },
			"POST /services/:serviceId/volumes": () => {
				items = [volume({ name: "data", mountPath: "/var/lib/data" })];
				return Response.json(items[0], { status: 201 });
			},
		});
		await renderWithProviders(<VolumesSection service={service} />);
		await screen.findByText(/No volumes/);

		const add = screen.getByRole("button", { name: "Add" });
		await user.type(screen.getByLabelText("Volume name"), "  data ");
		expect(add).toBeDisabled();
		await user.type(screen.getByLabelText("Mount path"), " /var/lib/data  ");
		expect(add).toBeEnabled();
		await user.click(add);

		const post = await waitFor(() => {
			const found = api.calls.find((c) => c.method === "POST");
			expect(found).toBeDefined();
			return found;
		});
		expect(post?.path).toBe("/services/svc-1/volumes");
		expect(post?.body).toEqual({ name: "data", mountPath: "/var/lib/data" });

		expect(await screen.findByText("Volume added")).toBeInTheDocument();
		expect(
			screen.getByText("Staged until the next deploy."),
		).toBeInTheDocument();
		const list = await screen.findByRole("list", { name: "Volumes" });
		expect(list).toHaveTextContent("/var/lib/data");
		expect(screen.getByLabelText("Volume name")).toHaveValue("");
		expect(screen.getByLabelText("Mount path")).toHaveValue("");
		expect(api.unmatched).toEqual([]);
	});

	test("sends the optional owner and shows it on the volume", async () => {
		const user = userEvent.setup();
		let items: VolumeRow[] = [];
		const api = stubApi({
			"GET /services/:serviceId/volumes": () => Response.json({ items }),
			"GET /services/:serviceId/retired-volumes": { body: { items: [] } },
			"POST /services/:serviceId/volumes": () => {
				items = [
					volume({ name: "dist", mountPath: "/dist", owner: "10001:10001" }),
				];
				return Response.json(items[0], { status: 201 });
			},
		});
		await renderWithProviders(<VolumesSection service={service} />);
		await screen.findByText(/No volumes/);

		await user.type(screen.getByLabelText("Volume name"), "dist");
		await user.type(screen.getByLabelText("Mount path"), "/dist");
		await user.type(screen.getByLabelText("Owner (uid:gid)"), " 10001:10001 ");
		await user.click(screen.getByRole("button", { name: "Add" }));

		const post = await waitFor(() => {
			const found = api.calls.find((c) => c.method === "POST");
			expect(found).toBeDefined();
			return found;
		});
		expect(post?.body).toEqual({
			name: "dist",
			mountPath: "/dist",
			owner: "10001:10001",
		});
		const list = await screen.findByRole("list", { name: "Volumes" });
		expect(list).toHaveTextContent("owner 10001:10001");
		expect(screen.getByLabelText("Owner (uid:gid)")).toHaveValue("");
		expect(api.unmatched).toEqual([]);
	});

	test("Enter in a field submits the form", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"GET /services/:serviceId/volumes": { body: { items: [] } },
			"GET /services/:serviceId/retired-volumes": { body: { items: [] } },
			"POST /services/:serviceId/volumes": { status: 201, body: volume() },
		});
		await renderWithProviders(<VolumesSection service={service} />);
		await user.type(await screen.findByLabelText("Volume name"), "logs");
		await user.type(screen.getByLabelText("Mount path"), "/logs{Enter}");
		await waitFor(() =>
			expect(api.calls.filter((c) => c.method === "POST")).toHaveLength(1),
		);
		expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
			name: "logs",
			mountPath: "/logs",
		});
	});

	test("a blank (whitespace only) name or path never sends a request", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"GET /services/:serviceId/volumes": { body: { items: [] } },
			"GET /services/:serviceId/retired-volumes": { body: { items: [] } },
		});
		await renderWithProviders(<VolumesSection service={service} />);
		await user.type(await screen.findByLabelText("Volume name"), "data");
		await user.type(screen.getByLabelText("Mount path"), "   {Enter}");
		expect(screen.getByRole("button", { name: "Add" })).toBeDisabled();
		expect(api.calls.filter((c) => c.method === "POST")).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});

	test("shows the control plane's refusal and keeps what was typed", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"GET /services/:serviceId/volumes": { body: { items: [] } },
			"GET /services/:serviceId/retired-volumes": { body: { items: [] } },
			"POST /services/:serviceId/volumes": {
				status: 409,
				body: {
					status: 409,
					code: "volume_exists",
					title: "Conflict",
					detail: "a volume named data already exists",
				},
			},
		});
		await renderWithProviders(<VolumesSection service={service} />);
		await user.type(await screen.findByLabelText("Volume name"), "data");
		await user.type(screen.getByLabelText("Mount path"), "/data");
		await user.click(screen.getByRole("button", { name: "Add" }));
		expect(
			await screen.findByText("a volume named data already exists"),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Volume name")).toHaveValue("data");
		expect(screen.getByLabelText("Mount path")).toHaveValue("/data");
		expect(screen.queryByText("Volume added")).toBeNull();
		expect(api.calls.filter((c) => c.method === "POST")).toHaveLength(1);
	});

	test("removing asks first, then DELETEs that volume and refreshes the list", async () => {
		const user = userEvent.setup();
		let items = [
			volume(),
			volume({ id: "vol-2", name: "cache", mountPath: "/cache" }),
		];
		const api = stubApi({
			"GET /services/:serviceId/volumes": () => Response.json({ items }),
			"GET /services/:serviceId/retired-volumes": { body: { items: [] } },
			"DELETE /volumes/:volumeId": () => {
				items = items.filter((v) => v.id !== "vol-2");
				return new Response(null, { status: 204 });
			},
		});
		await renderWithProviders(<VolumesSection service={service} />);
		await user.click(
			await screen.findByRole("button", { name: "Remove volume cache" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		expect(dialog).toHaveTextContent("Remove volume cache?");
		expect(dialog).toHaveTextContent("The mount is removed on the next deploy");
		expect(api.calls.filter((c) => c.method === "DELETE")).toEqual([]);

		await user.click(
			within(dialog).getByRole("button", { name: "Remove volume" }),
		);

		await waitFor(() =>
			expect(
				screen.queryByRole("button", { name: "Remove volume cache" }),
			).toBeNull(),
		);
		const deletes = api.calls.filter((c) => c.method === "DELETE");
		expect(deletes.map((c) => c.path)).toEqual(["/volumes/vol-2"]);
		expect(
			screen.getByRole("button", { name: "Remove volume data" }),
		).toBeInTheDocument();
	});

	test("cancelling the removal sends nothing", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"GET /services/:serviceId/volumes": { body: { items: [volume()] } },
			"GET /services/:serviceId/retired-volumes": { body: { items: [] } },
		});
		await renderWithProviders(<VolumesSection service={service} />);
		await user.click(
			await screen.findByRole("button", { name: "Remove volume data" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
		expect(api.calls.filter((c) => c.method === "DELETE")).toEqual([]);
		expect(
			screen.getByRole("button", { name: "Remove volume data" }),
		).toBeInTheDocument();
	});

	test("a failed removal keeps the dialog open with the reason", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			"GET /services/:serviceId/volumes": { body: { items: [volume()] } },
			"GET /services/:serviceId/retired-volumes": { body: { items: [] } },
			"DELETE /volumes/:volumeId": {
				status: 409,
				body: {
					status: 409,
					code: "volume_in_use",
					title: "Conflict",
					detail: "volume is mounted by a running container",
				},
			},
		});
		await renderWithProviders(<VolumesSection service={service} />);
		await user.click(
			await screen.findByRole("button", { name: "Remove volume data" }),
		);
		const dialog = await screen.findByRole("alertdialog");
		await user.click(
			within(dialog).getByRole("button", { name: "Remove volume" }),
		);
		expect(
			await within(dialog).findByText(
				"volume is mounted by a running container",
			),
		).toBeInTheDocument();
		expect(screen.getByRole("alertdialog")).toBeInTheDocument();
		expect(api.calls.filter((c) => c.method === "DELETE")).toHaveLength(1);
	});
});
