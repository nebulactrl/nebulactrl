import { describe, expect, mock, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { EnableMeshDialog } from "./enable-mesh-dialog";
import { meshView } from "./test-data";

const plan = (overrides: Record<string, unknown> = {}) => ({
	applied: false,
	enabled: false,
	plan: {
		blockers: [],
		devicesAdded: 1,
		tunDevice: true,
		warnings: [],
		...overrides,
	},
});

function routes(planBody: unknown) {
	return {
		"PUT /servers/:id/mesh": (request: Request) =>
			new URL(request.url).searchParams.get("dryRun") === "true"
				? Response.json(planBody)
				: Response.json(plan({}), { status: 200 }),
		"GET /mesh": { body: meshView() },
	};
}

function renderDialog(
	open = true,
	onOpenChange: (open: boolean) => void = () => {},
) {
	return renderWithProviders(
		<EnableMeshDialog
			serverId="srv-1"
			serverName="web-02"
			open={open}
			onOpenChange={onOpenChange}
		/>,
	);
}

describe("EnableMeshDialog", () => {
	test("asks for nothing while closed", async () => {
		const api = stubApi({});
		await renderDialog(false);
		expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
		expect(api.calls).toEqual([]);
	});

	test("shows the plan as a dry run: devices added, /dev/net/tun and the access-policy effect", async () => {
		const api = stubApi(routes(plan({ devicesAdded: 1 })));
		await renderDialog();
		const dialog = await screen.findByRole("dialog");
		expect(
			within(dialog).getByText("Enable the mesh on web-02?"),
		).toBeInTheDocument();
		expect(
			await within(dialog).findByText("Adds 1 device"),
		).toBeInTheDocument();
		expect(within(dialog).getByText("/dev/net/tun")).toBeInTheDocument();
		expect(
			await within(dialog).findByText(/Access policy: nothing is written/),
		).toBeInTheDocument();
		expect(
			within(dialog).getByRole("button", {
				name: "Enable mesh · adds 1 device",
			}),
		).toBeEnabled();
		const dry = api.calls.find((c) => c.method === "PUT");
		expect(dry?.path).toBe("/servers/srv-1/mesh");
		expect(dry?.query.get("dryRun")).toBe("true");
		expect(dry?.body).toEqual({ enabled: true });
	});

	test("without /dev/net/tun the plan does not claim the server can host links, and warnings are shown", async () => {
		stubApi(
			routes(
				plan({
					devicesAdded: 2,
					tunDevice: false,
					warnings: ["Tagged devices may be billed."],
				}),
			),
		);
		await renderDialog();
		expect(await screen.findByText("Adds 2 devices")).toBeInTheDocument();
		expect(screen.queryByText("/dev/net/tun")).not.toBeInTheDocument();
		expect(
			screen.getByText("Tagged devices may be billed."),
		).toBeInTheDocument();
	});

	test("a plan that adds no device offers a plain Enable mesh button", async () => {
		stubApi(routes(plan({ devicesAdded: 0 })));
		await renderDialog();
		expect(await screen.findByText("Adds 0 devices")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Enable mesh" })).toBeEnabled();
	});

	test("a blocker is shown with its way forward and Enable stays disabled", async () => {
		stubApi(
			routes(
				plan({
					blockers: [
						{
							code: "mesh_not_configured",
							message: "No tailnet is connected.",
						},
					],
				}),
			),
		);
		await renderDialog();
		expect(
			await screen.findByText(/No tailnet is connected\./),
		).toBeInTheDocument();
		expect(
			screen.getByRole("link", {
				name: "Connect Tailscale or set up Headscale first",
			}),
		).toBeInTheDocument();
		expect(screen.getByRole("button", { name: /^Enable mesh/ })).toBeDisabled();
	});

	test("a plan that cannot be read shows the error and Enable stays disabled", async () => {
		stubApi({
			"PUT /servers/:id/mesh": problem(409, {
				code: "mesh_agent_too_old",
				detail: "old",
			}),
			"GET /mesh": { body: meshView() },
		});
		await renderDialog();
		expect(
			await screen.findByText(
				/The agent on this server is too old for the mesh/,
			),
		).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Enable mesh" })).toBeDisabled();
	});

	test("confirming sends the real enable with acceptance and an idempotency key, then closes and says so", async () => {
		let enableKey: string | null = null;
		let enableBody: unknown;
		const api = stubApi({
			"PUT /servers/:id/mesh": async (request) => {
				if (new URL(request.url).searchParams.get("dryRun") === "true") {
					return Response.json(plan({ devicesAdded: 1 }));
				}
				enableKey = request.headers.get("Idempotency-Key");
				enableBody = await request.json();
				return Response.json({ ...plan(), applied: true, enabled: true });
			},
			"GET /mesh": { body: meshView() },
		});
		const onOpenChange = mock((_open: boolean) => {});
		const user = userEvent.setup();
		await renderDialog(true, onOpenChange);
		await user.click(
			await screen.findByRole("button", {
				name: "Enable mesh · adds 1 device",
			}),
		);
		expect(
			await screen.findByText("web-02 is joining the mesh"),
		).toBeInTheDocument();
		expect(enableBody).toEqual({ enabled: true, acceptMeshChanges: true });
		expect(enableKey).toMatch(/^[0-9a-f-]{36}$/);
		expect(onOpenChange).toHaveBeenCalledWith(false);
		const puts = api.calls.filter((c) => c.method === "PUT");
		expect(puts).toHaveLength(2);
		expect(puts[1]?.query.get("dryRun")).toBeNull();
		expect(api.unmatched).toEqual([]);
	});

	test("a refused enable stays open with the reason under the plan", async () => {
		const onOpenChange = mock((_open: boolean) => {});
		stubApi({
			"PUT /servers/:id/mesh": (request) =>
				new URL(request.url).searchParams.get("dryRun") === "true"
					? Response.json(plan())
					: Response.json(
							{
								status: 409,
								title: "Conflict",
								detail: "paused",
								code: "mesh_paused",
							},
							{ status: 409 },
						),
			"GET /mesh": { body: meshView() },
		});
		const user = userEvent.setup();
		await renderDialog(true, onOpenChange);
		await user.click(
			await screen.findByRole("button", {
				name: "Enable mesh · adds 1 device",
			}),
		);
		expect(
			await screen.findByText(
				"The mesh is paused. Changes wait until it resumes.",
			),
		).toBeInTheDocument();
		expect(onOpenChange).not.toHaveBeenCalled();
		expect(
			screen.queryByText("web-02 is joining the mesh"),
		).not.toBeInTheDocument();
	});

	test("Cancel closes without enabling", async () => {
		const onOpenChange = mock((_open: boolean) => {});
		const api = stubApi(routes(plan()));
		const user = userEvent.setup();
		await renderDialog(true, onOpenChange);
		await screen.findByText("Adds 1 device");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(onOpenChange).toHaveBeenCalledWith(false);
		expect(api.calls.filter((c) => c.method === "PUT")).toHaveLength(1);
	});

	test("the access-policy line follows the mesh's mode", async () => {
		stubApi({
			...routes(plan()),
			"GET /mesh": { body: meshView({ aclMode: "managed" }) },
		});
		await renderDialog();
		expect(
			await screen.findByText(
				/the rules are regenerated and written to your tailnet's policy/,
			),
		).toBeInTheDocument();
	});
});
