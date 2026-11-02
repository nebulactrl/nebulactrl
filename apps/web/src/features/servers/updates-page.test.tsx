import { describe, expect, test } from "bun:test";
import { screen } from "@testing-library/react";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { toServerView } from "./model";
import { ServerProvider } from "./server-context";
import { UpdatesPage } from "./updates-page";

const wire = make("Server", {
	id: "srv-7",
	name: "edge-7",
	status: "online",
	controlPlane: false,
	patches: {
		busy: "",
		known: false,
		supported: true,
		updates: 0,
		security: 0,
		rebootRequired: false,
		checkedAt: null,
	},
});

describe("UpdatesPage", () => {
	test("shows the host's updates for the page's server, then the agent's version card", async () => {
		const api = stubApi({
			"GET /me": {
				body: make("MeBody", {
					principal: { role: "owner", scopes: [] },
					instanceAdmin: true,
				}),
			},
			"GET /me/security": {
				body: make("SecurityBody", { isInstanceAdmin: true }),
			},
			"GET /servers/:serverId/patches": {
				body: {
					agentSupported: true,
					busy: "",
					checkedAt: null,
					controlPlane: false,
					known: false,
					manager: "",
					online: true,
					packages: [],
					productionServices: [],
					rebootPackages: [],
					rebootRequired: false,
					runs: [],
					security: 0,
					stale: false,
					supported: true,
					updates: 0,
				},
			},
			"GET /instance/upgrade": {
				body: {
					agentAutoUpgrade: false,
					behind: 1,
					controlPlaneVersion: "1.5.0",
					upgradable: 1,
					servers: [
						{
							serverId: "srv-7",
							name: "edge-7",
							agentVersion: "1.4.0",
							skew: "behind",
							status: "online",
							canSelfUpgrade: true,
							upgradable: true,
						},
					],
				},
			},
		});
		await renderWithProviders(
			<ServerProvider
				value={{ server: toServerView(wire), openDrain: () => {} }}
			>
				<UpdatesPage />
			</ServerProvider>,
		);
		expect(
			await screen.findByText("Operating system updates"),
		).toBeInTheDocument();
		expect(await screen.findByText("Agent version")).toBeInTheDocument();
		expect(screen.getByText("1.4.0")).toBeInTheDocument();
		expect(api.calls.some((c) => c.path === "/servers/srv-7/patches")).toBe(
			true,
		);
		expect(api.unmatched).toEqual([]);
	});
});
