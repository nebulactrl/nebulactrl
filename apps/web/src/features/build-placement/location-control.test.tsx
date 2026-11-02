import { describe, expect, test } from "bun:test";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { stubApi } from "#/test/api";
import { renderWithProviders } from "#/test/render";
import {
	LocationControl,
	type LocationScope,
	type LocationValue,
} from "./location-control";
import { resolveBuild } from "./model";
import { wireMe, wireRegistry, wireServer } from "./test-fixtures";

interface Backend {
	servers?: unknown[];
	registries?: unknown[];
	scopes?: string[];
}

function backend({
	servers = [],
	registries = [],
	scopes = ["admin"],
}: Backend) {
	return stubApi({
		"GET /me": { body: wireMe(scopes) },
		"GET /servers": { body: { items: servers } },
		"GET /registries": { body: { items: registries } },
	});
}

function Harness({
	scope,
	initial,
	parentLabel = null,
	team = { buildLocation: "target", registryId: null },
	seen,
}: {
	scope: LocationScope;
	initial: Partial<LocationValue>;
	parentLabel?: string | null;
	team?: {
		buildLocation: "target" | "build_server";
		registryId: string | null;
	};
	seen?: LocationValue[];
}) {
	const [value, setValue] = useState<LocationValue>({
		location: scope === "team" ? "target" : "inherit",
		buildServerId: "",
		registryId: "",
		...initial,
	});
	const level = {
		buildLocation: value.location,
		buildServerId: value.buildServerId,
		registryId: value.registryId,
	};
	return (
		<LocationControl
			scope={scope}
			idPrefix="t"
			value={value}
			onChange={(next) => {
				seen?.push(next);
				setValue(next);
			}}
			effective={resolveBuild({
				[scope === "service" ? "service" : "project"]:
					scope === "team" ? undefined : level,
				team:
					scope === "team"
						? {
								buildLocation:
									value.location === "build_server" ? "build_server" : "target",
								registryId: value.registryId || null,
							}
						: team,
			})}
			registryParentLabel={parentLabel}
		/>
	);
}

const buildServer = wireServer({ id: "b1", name: "build-01" });
const ghcr = wireRegistry({ id: "reg-1", name: "GHCR" });

describe("LocationControl: location", () => {
	test("a team chooses between two locations; Inherit is not offered", async () => {
		backend({});
		await renderWithProviders(<Harness scope="team" initial={{}} />);
		expect(
			screen.getByRole("group", { name: "Build location of the team" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", {
				name: "On the app's server",
				pressed: true,
			}),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "On a build server" }),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Inherit" }),
		).not.toBeInTheDocument();
	});

	test("a project can inherit and the sentence says what it inherits", async () => {
		backend({});
		await renderWithProviders(
			<Harness
				scope="project"
				initial={{}}
				team={{ buildLocation: "build_server", registryId: "reg-1" }}
			/>,
		);
		expect(
			screen.getByRole("button", { name: "Inherit", pressed: true }),
		).toBeInTheDocument();
		expect(screen.getByRole("status")).toHaveTextContent(
			"Uses the team setting: builds run on a build server.",
		);
	});

	test("choosing Inherit reports an empty pin and registry", async () => {
		backend({ servers: [buildServer], registries: [ghcr] });
		const seen: LocationValue[] = [];
		await renderWithProviders(
			<Harness
				scope="service"
				initial={{
					location: "build_server",
					buildServerId: "b1",
					registryId: "reg-1",
				}}
				seen={seen}
			/>,
		);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Inherit" }));
		expect(seen.at(-1)).toEqual({
			location: "inherit",
			buildServerId: "",
			registryId: "",
		});
	});

	test("picking a location reports it and updates the consequence", async () => {
		backend({});
		const seen: LocationValue[] = [];
		await renderWithProviders(
			<Harness scope="project" initial={{}} seen={seen} />,
		);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "On the app's server" }));
		expect(seen).toEqual([
			{ location: "target", buildServerId: "", registryId: "" },
		]);
		expect(screen.getByRole("status")).toHaveTextContent(
			"The image is built on the server that runs the app and used from there. No registry is needed.",
		);
	});
});

describe("LocationControl: build server pin", () => {
	test("is only offered below the team, once builds run on a build server", async () => {
		backend({ servers: [buildServer] });
		await renderWithProviders(
			<Harness scope="project" initial={{ location: "target" }} />,
		);
		expect(
			screen.queryByRole("combobox", { name: "Build server" }),
		).not.toBeInTheDocument();
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "On a build server" }));
		expect(
			await screen.findByRole("combobox", { name: "Build server" }),
		).toBeInTheDocument();
	});

	test("a team has no pin", async () => {
		backend({ servers: [buildServer] });
		await renderWithProviders(
			<Harness scope="team" initial={{ location: "build_server" }} />,
		);
		await screen.findByRole("combobox", { name: "Registry" });
		expect(
			screen.queryByRole("combobox", { name: "Build server" }),
		).not.toBeInTheDocument();
	});

	test("defaults to Automatic and explains it; pinning a server reports its id", async () => {
		backend({
			servers: [
				buildServer,
				wireServer({ id: "w1", name: "web-02", buildEnabled: false }),
				wireServer({ id: "r1", name: "revoked-01", status: "revoked" }),
			],
		});
		const seen: LocationValue[] = [];
		await renderWithProviders(
			<Harness
				scope="project"
				initial={{ location: "build_server" }}
				seen={seen}
			/>,
		);
		const pin = await screen.findByRole("combobox", { name: "Build server" });
		expect(pin).toHaveTextContent("Automatic");
		expect(
			screen.getByText(/Automatic picks one eligible build server/),
		).toBeInTheDocument();
		const user = userEvent.setup();
		await user.click(pin);
		expect(
			screen.queryByRole("option", { name: /web-02/ }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole("option", { name: /revoked-01/ }),
		).not.toBeInTheDocument();
		await user.click(await screen.findByRole("option", { name: /build-01/ }));
		expect(seen.at(-1)).toMatchObject({ buildServerId: "b1" });
		expect(pin).toHaveTextContent("build-01");
		expect(
			screen.getByText(/Always builds here\. If it is offline/),
		).toBeInTheDocument();
	});

	test("choosing Automatic again clears the pin", async () => {
		backend({ servers: [buildServer] });
		const seen: LocationValue[] = [];
		await renderWithProviders(
			<Harness
				scope="service"
				initial={{ location: "build_server", buildServerId: "b1" }}
				seen={seen}
			/>,
		);
		const pin = await screen.findByRole("combobox", { name: "Build server" });
		await waitForText(pin, "build-01");
		const user = userEvent.setup();
		await user.click(pin);
		await user.click(await screen.findByRole("option", { name: /Automatic/ }));
		expect(seen.at(-1)).toMatchObject({ buildServerId: "" });
	});

	test("a build server that is not online says why in the list", async () => {
		backend({
			servers: [
				buildServer,
				wireServer({ id: "b2", name: "build-02", status: "offline" }),
				wireServer({
					id: "b3",
					name: "build-03",
					facts: { ...wireServer().facts, capabilities: [] },
				}),
			],
		});
		await renderWithProviders(
			<Harness scope="project" initial={{ location: "build_server" }} />,
		);
		await userEvent
			.setup()
			.click(await screen.findByRole("combobox", { name: "Build server" }));
		expect(
			await screen.findByRole("option", { name: "build-02 offline" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("option", { name: "build-03 agent needs an update" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("option", { name: "build-01" }),
		).toBeInTheDocument();
	});

	test("warns when the pinned server is no longer a build server", async () => {
		backend({ servers: [buildServer] });
		await renderWithProviders(
			<Harness
				scope="project"
				initial={{ location: "build_server", buildServerId: "gone" }}
			/>,
		);
		expect(
			await screen.findByText(/The pinned server is no longer a build server/),
		).toBeInTheDocument();
	});

	test("does not warn about a pin that is still a build server", async () => {
		backend({ servers: [buildServer] });
		await renderWithProviders(
			<Harness
				scope="project"
				initial={{ location: "build_server", buildServerId: "b1" }}
			/>,
		);
		await screen.findByRole("combobox", { name: "Build server" });
		expect(
			screen.queryByText(/no longer a build server/),
		).not.toBeInTheDocument();
	});
});

describe("LocationControl: no build server", () => {
	test("points to the servers page when no server builds", async () => {
		backend({
			servers: [wireServer({ id: "w1", name: "web-02", buildEnabled: false })],
		});
		await renderWithProviders(
			<Harness scope="team" initial={{ location: "build_server" }} />,
		);
		expect(await screen.findByText("No build server yet")).toBeInTheDocument();
		expect(screen.getByRole("link", { name: "Open servers" })).toHaveAttribute(
			"href",
			"/servers",
		);
	});

	test("does not claim there is none while servers are loading or builds stay on the target", async () => {
		backend({ servers: [] });
		await renderWithProviders(
			<Harness scope="team" initial={{ location: "target" }} />,
		);
		await screen.findByRole("combobox", { name: "Registry" });
		expect(screen.queryByText("No build server yet")).not.toBeInTheDocument();
	});
});

describe("LocationControl: registry", () => {
	test("lists registries by name and place, and reports the choice", async () => {
		backend({
			servers: [buildServer],
			registries: [
				ghcr,
				wireRegistry({
					id: "reg-b",
					name: "Built-in",
					kind: "builtin",
					host: "registry.local",
					namespace: "",
					builtin: { status: "ready", statusMessage: "", gcState: "idle" },
				}),
			],
		});
		const seen: LocationValue[] = [];
		await renderWithProviders(
			<Harness
				scope="team"
				initial={{ location: "build_server" }}
				seen={seen}
			/>,
		);
		const select = await screen.findByRole("combobox", { name: "Registry" });
		expect(select).toHaveTextContent("No registry");
		const user = userEvent.setup();
		await user.click(select);
		expect(
			await screen.findByRole("option", { name: "Built-in (built-in)" }),
		).toBeInTheDocument();
		await user.click(
			screen.getByRole("option", { name: "GHCR (ghcr.io/acme)" }),
		);
		expect(seen.at(-1)).toMatchObject({ registryId: "reg-1" });
		expect(select).toHaveTextContent("GHCR (ghcr.io/acme)");
		expect(
			screen.getByRole("link", { name: "Manage image registries" }),
		).toHaveAttribute("href", "/settings/image-registries");
	});

	test("choosing the empty entry clears the registry", async () => {
		backend({ servers: [buildServer], registries: [ghcr] });
		const seen: LocationValue[] = [];
		await renderWithProviders(
			<Harness
				scope="team"
				initial={{ location: "build_server", registryId: "reg-1" }}
				seen={seen}
			/>,
		);
		const select = await screen.findByRole("combobox", { name: "Registry" });
		await waitForText(select, "GHCR");
		const user = userEvent.setup();
		await user.click(select);
		await user.click(
			await screen.findByRole("option", { name: "No registry" }),
		);
		expect(seen.at(-1)).toMatchObject({ registryId: "" });
	});

	test("an inheriting project names the parent's registry on the empty entry", async () => {
		backend({ servers: [buildServer], registries: [ghcr] });
		await renderWithProviders(
			<Harness
				scope="project"
				initial={{ location: "build_server" }}
				parentLabel="team default: GHCR"
			/>,
		);
		expect(
			await screen.findByRole("combobox", { name: "Registry" }),
		).toHaveTextContent("Inherit (team default: GHCR)");
	});

	test("a project with no parent label just says Inherit", async () => {
		backend({ servers: [buildServer], registries: [ghcr] });
		await renderWithProviders(
			<Harness scope="project" initial={{ location: "build_server" }} />,
		);
		const select = await screen.findByRole("combobox", { name: "Registry" });
		expect(select).toHaveTextContent(/^Inherit/);
		expect(select).not.toHaveTextContent("(");
	});

	test("an inheriting project builds on the target: no registry field", async () => {
		backend({ servers: [buildServer], registries: [ghcr] });
		await renderWithProviders(<Harness scope="project" initial={{}} />);
		await screen.findByRole("status");
		expect(
			screen.queryByRole("combobox", { name: "Registry" }),
		).not.toBeInTheDocument();
	});

	test("a project on the target that already has a registry still shows it", async () => {
		backend({ registries: [ghcr] });
		await renderWithProviders(
			<Harness
				scope="project"
				initial={{ location: "target", registryId: "reg-1" }}
			/>,
		);
		expect(
			await screen.findByRole("combobox", { name: "Registry" }),
		).toBeInTheDocument();
	});

	test("without the admin scope the registry list is not offered and no registry warning shows", async () => {
		const api = backend({ servers: [buildServer], scopes: ["read"] });
		await renderWithProviders(
			<Harness scope="team" initial={{ location: "build_server" }} />,
		);
		await screen.findByRole("status");
		await waitFor(() =>
			expect(api.calls.some((c) => c.path === "/servers")).toBe(true),
		);
		expect(
			screen.queryByRole("combobox", { name: "Registry" }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByText(/No registry, so builds stay/),
		).not.toBeInTheDocument();
		expect(api.calls.some((c) => c.path === "/registries")).toBe(false);
	});
});

describe("LocationControl: missing registry warning", () => {
	test("a team on a build server without a registry is told builds stay on the app's server", async () => {
		backend({ servers: [buildServer], registries: [ghcr] });
		await renderWithProviders(
			<Harness scope="team" initial={{ location: "build_server" }} />,
		);
		expect(
			await screen.findByText(
				"No registry, so builds stay on the app's server",
			),
		).toBeInTheDocument();
		expect(
			screen.getByText(/Pick one above, or add a registry first\./),
		).toBeInTheDocument();
	});

	test("an inheriting project is told to pick one here or on the team", async () => {
		backend({ servers: [buildServer], registries: [ghcr] });
		await renderWithProviders(
			<Harness
				scope="project"
				initial={{}}
				team={{ buildLocation: "build_server", registryId: null }}
			/>,
		);
		expect(
			await screen.findByText(/Pick one here, or set one on the team\./),
		).toBeInTheDocument();
	});

	test("an inheriting service names the project or team", async () => {
		backend({ servers: [buildServer], registries: [ghcr] });
		await renderWithProviders(
			<Harness
				scope="service"
				initial={{ location: "inherit" }}
				team={{ buildLocation: "build_server", registryId: null }}
			/>,
		);
		expect(
			await screen.findByText(
				/Pick one here, or set one on the project or team\./,
			),
		).toBeInTheDocument();
	});

	test("goes away once a registry is chosen", async () => {
		backend({ servers: [buildServer], registries: [ghcr] });
		await renderWithProviders(
			<Harness
				scope="team"
				initial={{ location: "build_server", registryId: "reg-1" }}
			/>,
		);
		await screen.findByRole("combobox", { name: "Registry" });
		expect(
			screen.queryByText(/No registry, so builds stay/),
		).not.toBeInTheDocument();
	});
});

function waitForText(element: HTMLElement, text: string) {
	return waitFor(() => expect(element).toHaveTextContent(text));
}
