import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import type { Facts, UnrestrictedSettings } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { HostDetails } from "./host-details";
import { toServerView } from "./model";

const facts = (over: Partial<Facts> = {}): Facts => ({
	arch: "arm64",
	cgroupVersion: "2",
	cpus: 4,
	diskTotal: 100,
	dockerVersion: "27.1.1",
	kernel: "6.8.0-45-generic",
	memTotal: 8 * 1024 ** 3,
	os: "ubuntu 24.04",
	port443Free: true,
	port80Free: true,
	...over,
});

const serverWith = (over: {
	facts?: Facts | null;
	protocolVersion?: number | null;
}) =>
	toServerView(
		make("Server", {
			id: "srv_1",
			name: "edge-1",
			status: "online",
			controlPlane: false,
			facts: over.facts ?? null,
			protocolVersion: "protocolVersion" in over ? over.protocolVersion : 3,
		}),
	);

const compose = (
	over: Partial<UnrestrictedSettings> = {},
): UnrestrictedSettings => ({
	available: true,
	composeVersion: "2.29.1",
	dockerServices: 0,
	stacks: 0,
	unrestricted: false,
	...over,
});

const GET = "GET /servers/:serverId/unrestricted";

/** The value shown beside a label in the ruled rows. */
function shown(label: string): string | null {
	const term = screen.getByText(label, { selector: "dt" });
	const row = term.parentElement as HTMLElement;
	return within(row).getByRole("definition").textContent;
}

describe("HostDetails", () => {
	test("lists what the host reported: protocol, kernel, cgroup and ports", async () => {
		stubApi({ [GET]: { body: compose() } });
		await renderWithProviders(
			<HostDetails server={serverWith({ facts: facts() })} />,
		);
		expect(shown("Protocol")).toBe("v3");
		expect(shown("Kernel")).toBe("6.8.0-45-generic");
		expect(shown("cgroup")).toBe("v2");
		expect(shown("Ports 80 / 443")).toBe("free / free");
	});

	test("says which of ports 80 and 443 are in use", async () => {
		stubApi({ [GET]: { body: compose() } });
		await renderWithProviders(
			<HostDetails
				server={serverWith({ facts: facts({ port80Free: false }) })}
			/>,
		);
		expect(shown("Ports 80 / 443")).toBe("in use / free");
	});

	test("both ports in use", async () => {
		stubApi({ [GET]: { body: compose() } });
		await renderWithProviders(
			<HostDetails
				server={serverWith({
					facts: facts({ port80Free: false, port443Free: false }),
				})}
			/>,
		);
		expect(shown("Ports 80 / 443")).toBe("in use / in use");
	});

	test("a host that never connected shows only an empty protocol and no facts", async () => {
		stubApi({
			[GET]: problem(403, { title: "Forbidden" }),
		});
		await renderWithProviders(
			<HostDetails
				server={serverWith({ facts: null, protocolVersion: null })}
			/>,
		);
		expect(shown("Protocol")).toBe("–");
		expect(screen.queryByText("Kernel")).not.toBeInTheDocument();
		expect(screen.queryByText("cgroup")).not.toBeInTheDocument();
		expect(screen.queryByText("Ports 80 / 443")).not.toBeInTheDocument();
	});

	test("shows the Compose plugin version and that stacks can run", async () => {
		stubApi({ [GET]: { body: compose() } });
		await renderWithProviders(
			<HostDetails server={serverWith({ facts: facts() })} />,
		);
		await waitFor(() =>
			expect(shown("Compose plugin")).toBe("2.29.1, stacks can run here"),
		);
	});

	test("an old Compose plugin is flagged as too old for stacks", async () => {
		stubApi({
			[GET]: { body: compose({ composeVersion: "2.20.0", available: false }) },
		});
		await renderWithProviders(
			<HostDetails server={serverWith({ facts: facts() })} />,
		);
		await waitFor(() =>
			expect(shown("Compose plugin")).toBe(
				"2.20.0, too old for stacks (2.24 or newer needed)",
			),
		);
	});

	test("no Compose plugin says stacks cannot be deployed", async () => {
		stubApi({
			[GET]: { body: compose({ composeVersion: "", available: false }) },
		});
		await renderWithProviders(
			<HostDetails server={serverWith({ facts: facts() })} />,
		);
		await waitFor(() =>
			expect(shown("Compose plugin")).toBe(
				"not installed, so stacks cannot be deployed here",
			),
		);
	});

	test("a caller who cannot read the Compose fact simply gets no row", async () => {
		const api = stubApi({
			[GET]: problem(403, { title: "Forbidden" }),
		});
		await renderWithProviders(
			<HostDetails server={serverWith({ facts: facts() })} />,
		);
		await waitFor(() => expect(api.calls).toHaveLength(1));
		expect(screen.queryByText("Compose plugin")).not.toBeInTheDocument();
		expect(shown("Kernel")).toBe("6.8.0-45-generic");
	});
});
