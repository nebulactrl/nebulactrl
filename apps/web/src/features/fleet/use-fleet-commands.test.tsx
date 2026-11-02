import { describe, expect, test } from "bun:test";
import { useRouterState } from "@tanstack/react-router";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Server } from "#/api/gen/types.gen";
import { useCommandRegistry } from "#/components/shell/palette-registry";
import { stubApi } from "#/test/api";
import { make } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { useFleetCommands } from "./use-fleet-commands";

const serverRow = (
	id: string,
	name: string,
	status: Server["status"],
	drainState: Server["drainState"],
) =>
	make("Server", {
		id,
		name,
		status,
		drainState,
		drainMessage: "",
		serviceCount: 0,
		acceptWorkloads: true,
	});

/** Mounts the hook and renders what the palette would see, plus the current URL. */
function Probe() {
	useFleetCommands();
	const commands = useCommandRegistry();
	const location = useRouterState({
		select: (state) => `${state.location.pathname}${state.location.searchStr}`,
	});
	return (
		<>
			<output aria-label="location">{location}</output>
			<ul>
				{commands.map((command) => (
					<li key={command.id}>
						<button type="button" onClick={command.run}>
							{command.label}
						</button>
						<span>
							{command.id}|{command.group}|
							{command.searchOnly ? "search" : "always"}
						</span>
					</li>
				))}
			</ul>
		</>
	);
}

function Registry() {
	const commands = useCommandRegistry();
	return <output aria-label="count">{commands.length}</output>;
}

describe("useFleetCommands", () => {
	test("always offers 'Open fleet', which navigates to the servers fleet view", async () => {
		stubApi({ "GET /servers": { body: make("PageServer", { items: [] }) } });
		const user = userEvent.setup();
		await renderWithProviders(<Probe />, { path: "/somewhere" });
		expect(screen.getByText("go:fleet|Go to|always")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Open fleet" }));
		expect(screen.getByLabelText("location")).toHaveTextContent(
			"/servers?view=fleet",
		);
	});

	test("adds a search-only 'Drain <name>' for each server that can be drained", async () => {
		stubApi({
			"GET /servers": {
				body: make("PageServer", {
					items: [
						serverRow("s1", "alpha", "online", "none"),
						serverRow("s2", "beta", "offline", "failed"),
						serverRow("s3", "gamma", "online", "drained"),
					],
				}),
			},
		});
		await renderWithProviders(<Probe />);
		expect(
			await screen.findByRole("button", { name: "Drain alpha" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Drain beta" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Drain gamma" }),
		).toBeInTheDocument();
		expect(screen.getByText("drain:s1|Action|search")).toBeInTheDocument();
	});

	test("leaves out servers that are draining, revoked or still pending", async () => {
		stubApi({
			"GET /servers": {
				body: make("PageServer", {
					items: [
						serverRow("s1", "alpha", "online", "none"),
						serverRow("s2", "busy", "online", "draining"),
						serverRow("s3", "dead", "revoked", "none"),
						serverRow("s4", "new", "pending", "none"),
					],
				}),
			},
		});
		await renderWithProviders(<Probe />);
		await screen.findByRole("button", { name: "Drain alpha" });
		expect(
			screen.queryByRole("button", { name: "Drain busy" }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Drain dead" }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Drain new" }),
		).not.toBeInTheDocument();
	});

	test("running a drain command opens that server's page with the drain sheet", async () => {
		stubApi({
			"GET /servers": {
				body: make("PageServer", {
					items: [serverRow("s1", "alpha", "online", "none")],
				}),
			},
		});
		const user = userEvent.setup();
		await renderWithProviders(<Probe />);
		await user.click(
			await screen.findByRole("button", { name: "Drain alpha" }),
		);
		expect(screen.getByLabelText("location")).toHaveTextContent(
			"/servers/s1?drain=true",
		);
	});

	test("unregisters its commands when unmounted", async () => {
		stubApi({ "GET /servers": { body: make("PageServer", { items: [] }) } });
		const { unmount } = await renderWithProviders(<Probe />);
		unmount();
		await renderWithProviders(<Registry />);
		expect(screen.getByLabelText("count")).toHaveTextContent("0");
	});
});
