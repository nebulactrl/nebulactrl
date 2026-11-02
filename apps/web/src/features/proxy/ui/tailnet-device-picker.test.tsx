import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { TailscaleDevice } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { TailnetDevicePicker } from "./tailnet-device-picker";

/** The server's proxy settings, with the tailnet as the picker reads it. */
const proxy = (tailnet: {
	integrationConfigured: boolean;
	mode: "off" | "managed" | "host";
}) => ({ body: make("ServerProxy", { tailnet }) });

/** A 200 reply listing `items` as the tailnet's devices. */
const devices = (...items: TailscaleDevice[]) => ({
	body: make("DevicesOutputBody", { items }),
});

const nas = make("TailscaleDevice", {
	id: "dev-nas",
	hostname: "nas",
	name: "nas.tail1.ts.net",
	addresses: ["fd7a:115c::1", "100.64.0.2"],
	online: true,
	os: "linux",
	lastSeen: null,
	tags: null,
});
const laptop = make("TailscaleDevice", {
	...nas,
	id: "dev-laptop",
	hostname: "laptop",
	online: false,
	addresses: ["100.64.0.9"],
});
const v6only = make("TailscaleDevice", {
	...nas,
	id: "dev-v6",
	hostname: "v6-only",
	addresses: ["fd7a:115c::7"],
});
const noAddress = make("TailscaleDevice", {
	...nas,
	id: "dev-none",
	hostname: "ghost",
	addresses: null,
});

describe("TailnetDevicePicker", () => {
	test("lists the tailnet's devices and picks one by its IPv4 address", async () => {
		const api = stubApi({
			"GET /servers/:id/proxy": proxy({
				integrationConfigured: true,
				mode: "managed",
			}),
			"GET /integrations/tailscale/devices": devices(nas, laptop),
		});
		const picked: string[] = [];
		const user = userEvent.setup();
		await renderWithProviders(
			<TailnetDevicePicker serverId="s1" onPick={(a) => picked.push(a)} />,
		);
		await user.click(
			await screen.findByRole("combobox", { name: "Pick a tailnet device" }),
		);
		const option = await screen.findByRole("option", { name: /nas/ });
		expect(option).toHaveTextContent("100.64.0.2");
		expect(screen.getByRole("option", { name: /laptop/ })).toHaveTextContent(
			"100.64.0.9",
		);
		await user.click(option);
		expect(picked).toEqual(["100.64.0.2"]);
		expect(api.unmatched).toEqual([]);
		expect(api.calls[0]).toMatchObject({
			method: "GET",
			path: "/servers/s1/proxy",
		});
	});

	test("devices without any address are not offered", async () => {
		stubApi({
			"GET /servers/:id/proxy": proxy({
				integrationConfigured: true,
				mode: "host",
			}),
			"GET /integrations/tailscale/devices": devices(noAddress, v6only),
		});
		const user = userEvent.setup();
		await renderWithProviders(
			<TailnetDevicePicker serverId="s1" onPick={() => {}} />,
		);
		await user.click(await screen.findByRole("combobox"));
		expect(
			await screen.findByRole("option", { name: /v6-only/ }),
		).toHaveTextContent("fd7a:115c::7");
		expect(
			screen.queryByRole("option", { name: /ghost/ }),
		).not.toBeInTheDocument();
	});

	test("renders nothing when the Tailscale integration is not configured, and never asks for devices", async () => {
		const api = stubApi({
			"GET /servers/:id/proxy": proxy({
				integrationConfigured: false,
				mode: "managed",
			}),
		});
		await renderWithProviders(
			<TailnetDevicePicker serverId="s1" onPick={() => {}} />,
		);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});

	test("renders nothing when the server's tailnet mode is off", async () => {
		const api = stubApi({
			"GET /servers/:id/proxy": proxy({
				integrationConfigured: true,
				mode: "off",
			}),
		});
		await renderWithProviders(
			<TailnetDevicePicker serverId="s1" onPick={() => {}} />,
		);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
		expect(api.unmatched).toEqual([]);
	});

	test("renders nothing while no server is chosen and makes no request", async () => {
		const api = stubApi({});
		await renderWithProviders(
			<TailnetDevicePicker serverId="" onPick={() => {}} />,
		);
		expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
		expect(api.calls).toEqual([]);
		expect(api.unmatched).toEqual([]);
	});

	test("renders nothing when the tailnet has no usable devices", async () => {
		const api = stubApi({
			"GET /servers/:id/proxy": proxy({
				integrationConfigured: true,
				mode: "managed",
			}),
			"GET /integrations/tailscale/devices": devices(noAddress),
		});
		await renderWithProviders(
			<TailnetDevicePicker serverId="s1" onPick={() => {}} />,
		);
		await waitFor(() => expect(api.calls).toHaveLength(2));
		await waitFor(() =>
			expect(screen.queryByRole("combobox")).not.toBeInTheDocument(),
		);
	});

	test("renders nothing when the device list fails to load", async () => {
		const api = stubApi({
			"GET /servers/:id/proxy": proxy({
				integrationConfigured: true,
				mode: "managed",
			}),
			"GET /integrations/tailscale/devices": problem(502, {
				code: "tailscale_unreachable",
			}),
		});
		await renderWithProviders(
			<TailnetDevicePicker serverId="s1" onPick={() => {}} />,
		);
		await waitFor(() => expect(api.calls).toHaveLength(2));
		await waitFor(() =>
			expect(screen.queryByRole("combobox")).not.toBeInTheDocument(),
		);
	});
});
