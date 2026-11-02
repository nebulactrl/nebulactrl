import { describe, expect, test } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type ReactNode, useCallback, useState } from "react";
import type { Service } from "#/api/models";
import { type SettingsDraft, toDraft } from "#/features/service-settings/draft";
import type { DraftErrors } from "#/features/service-settings/fields";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import type { StackCheckState } from "./model";
import {
	ComposeSection,
	StackDeploySection,
	StackLimitsSection,
	StackNetworkingSection,
} from "./settings-sections";
import { COMPONENTS, makeStack } from "./test-fixtures";

const CHECKED: StackCheckState = {
	empty: false,
	pending: false,
	error: null,
	result: {
		components: COMPONENTS,
		violations: [],
		notes: [],
		unrestricted: false,
		composeAvailable: true,
	},
};

/** The settings form's state: the draft as the sections edit it. */
function Form({
	service,
	errors = {},
	children,
	onDraft,
}: {
	service: Service;
	errors?: DraftErrors;
	children(props: {
		draft: SettingsDraft;
		errors: DraftErrors;
		set<K extends keyof SettingsDraft>(key: K, value: SettingsDraft[K]): void;
	}): ReactNode;
	onDraft?: ((draft: SettingsDraft) => void) | undefined;
}) {
	const [draft, setDraft] = useState(() => toDraft(service));
	const set = useCallback(
		<K extends keyof SettingsDraft>(key: K, value: SettingsDraft[K]) =>
			setDraft((current) => {
				const next = { ...current, [key]: value };
				onDraft?.(next);
				return next;
			}),
		[onDraft],
	);
	return <>{children({ draft, errors, set })}</>;
}

/** The item at `index`; fails the test with a clear message when it is missing. */
function at<T>(items: readonly T[], index: number): T {
	const item = items[index];
	if (item === undefined) throw new Error(`expected an item at index ${index}`);
	return item;
}

/** The stack settings of a stack service; fails the test when it is not one. */
function stackOf(service: Service) {
	const stack = service.config.stack;
	if (!stack) throw new Error("expected a stack service");
	return stack;
}

function elementById(id: string): HTMLElement {
	const element = document.getElementById(id);
	if (!element) throw new Error(`expected an element with id ${id}`);
	return element;
}

describe("ComposeSection", () => {
	test("edits the draft's compose text and shows the live check under it", async () => {
		const user = userEvent.setup();
		stubApi({});
		let latest: SettingsDraft | undefined;
		const service = makeStack({ compose: "services: {}" });
		await renderWithProviders(
			<Form service={service} onDraft={(d) => (latest = d)}>
				{(props) => (
					<ComposeSection {...props} check={CHECKED} serverName="hetzner-01" />
				)}
			</Form>,
		);
		const editor = screen.getByLabelText("docker-compose.yml");
		expect(editor).toHaveValue("services: {}");
		expect(editor).toHaveAttribute("id", "set-compose");
		expect(screen.getByRole("status")).toHaveTextContent(
			"3 components. The Compose file passes the checks.",
		);
		await user.type(editor, "!");
		expect(latest?.stackCompose).toBe("services: {}!");
	});

	test("shows the form's error under the editor", async () => {
		stubApi({});
		await renderWithProviders(
			<Form
				service={makeStack({ compose: "" })}
				errors={{ stackCompose: "A Compose file is required." }}
			>
				{(props) => (
					<ComposeSection
						{...props}
						check={{ ...CHECKED, empty: true, result: null }}
						serverName={undefined}
					/>
				)}
			</Form>,
		);
		expect(screen.getByText("A Compose file is required.")).toBeInTheDocument();
		expect(screen.getByLabelText("docker-compose.yml")).toBeInvalid();
	});

	test("explains that saving stages the change", async () => {
		stubApi({});
		await renderWithProviders(
			<Form service={makeStack()}>
				{(props) => (
					<ComposeSection {...props} check={CHECKED} serverName={undefined} />
				)}
			</Form>,
		);
		expect(
			screen.getByRole("region", { name: "Compose file" }),
		).toHaveTextContent("Saving stages the change");
	});
});

describe("StackDeploySection", () => {
	test("shows the wait timeout and stop grace from the draft and edits them", async () => {
		const user = userEvent.setup();
		let latest: SettingsDraft | undefined;
		const service = makeStack();
		service.config.stack = { ...stackOf(service), waitSeconds: 120 };
		service.config.stopGraceSeconds = 15;
		await renderWithProviders(
			<Form service={service} onDraft={(d) => (latest = d)}>
				{(props) => <StackDeploySection {...props} />}
			</Form>,
		);
		const wait = screen.getByLabelText("Wait timeout");
		const grace = screen.getByLabelText("Stop grace period");
		expect(wait).toHaveValue("120");
		expect(grace).toHaveValue("15");
		await user.clear(wait);
		await user.type(wait, "600");
		expect(latest?.stackWaitSeconds).toBe("600");
		await user.clear(grace);
		await user.type(grace, "5");
		expect(latest?.stopGraceSeconds).toBe("5");
	});

	test("an empty wait timeout shows the 300 default as its placeholder", async () => {
		await renderWithProviders(
			<Form service={makeStack()}>
				{(props) => <StackDeploySection {...props} />}
			</Form>,
		);
		expect(screen.getByLabelText("Wait timeout")).toHaveAttribute(
			"placeholder",
			"300",
		);
		expect(screen.getByLabelText("Wait timeout")).toHaveValue("");
	});

	test("shows the field errors", async () => {
		await renderWithProviders(
			<Form
				service={makeStack()}
				errors={{
					stackWaitSeconds: "Wait timeout must be a whole number.",
					stopGraceSeconds: "Stop grace must be positive.",
				}}
			>
				{(props) => <StackDeploySection {...props} />}
			</Form>,
		);
		expect(
			screen.getByText("Wait timeout must be a whole number."),
		).toBeInTheDocument();
		expect(
			screen.getByText("Stop grace must be positive."),
		).toBeInTheDocument();
	});
});

describe("StackLimitsSection", () => {
	test("without components it says to add services first", async () => {
		const service = makeStack({ components: [], name: "shop" });
		await renderWithProviders(
			<Form service={service}>
				{(props) => (
					<StackLimitsSection {...props} service={service} components={[]} />
				)}
			</Form>,
		);
		expect(
			screen.getByText(
				"shop has no components yet. Add services to the Compose file first.",
			),
		).toBeInTheDocument();
		expect(screen.queryByLabelText("Memory (MB)")).not.toBeInTheDocument();
	});

	test("lists a memory and CPU field per component, filled from the draft", async () => {
		const service = makeStack();
		service.config.stack = {
			...stackOf(service),
			resources: { web: { memoryMb: 512, cpus: 1.5 } },
		};
		await renderWithProviders(
			<Form service={service}>
				{(props) => (
					<StackLimitsSection
						{...props}
						service={service}
						components={COMPONENTS}
					/>
				)}
			</Form>,
		);
		expect(screen.getAllByLabelText("Memory (MB)")).toHaveLength(3);
		expect(document.getElementById("limits-web-memory")).toHaveValue("512");
		expect(document.getElementById("limits-web-cpus")).toHaveValue("1.5");
		expect(document.getElementById("limits-db-memory")).toHaveValue("");
		expect(document.getElementById("limits-db-memory")).toHaveAttribute(
			"placeholder",
			"no limit",
		);
		// The image is shown with the component's name.
		expect(screen.getByText("nginx:1.27")).toBeInTheDocument();
	});

	test("typing a limit updates only that component's draft", async () => {
		const user = userEvent.setup();
		let latest: SettingsDraft | undefined;
		const service = makeStack();
		await renderWithProviders(
			<Form service={service} onDraft={(d) => (latest = d)}>
				{(props) => (
					<StackLimitsSection
						{...props}
						service={service}
						components={COMPONENTS}
					/>
				)}
			</Form>,
		);
		await user.type(elementById("limits-db-memory"), "256");
		await user.type(elementById("limits-db-cpus"), "2");
		expect(latest?.stackResources.db).toEqual({ memoryMb: "256", cpus: "2" });
		expect(latest?.stackResources.web).toBeUndefined();
	});

	test("out-of-range limits show their messages and mark the field invalid", async () => {
		const service = makeStack();
		service.config.stack = {
			...stackOf(service),
			resources: { db: { memoryMb: 2, cpus: 999 } },
		};
		await renderWithProviders(
			<Form service={service}>
				{(props) => (
					<StackLimitsSection
						{...props}
						service={service}
						components={COMPONENTS}
					/>
				)}
			</Form>,
		);
		expect(
			screen.getByText("Memory must be a whole number from 6 to 1048576 MB."),
		).toBeInTheDocument();
		expect(
			screen.getByText("CPUs must be a number between 0.01 and 256."),
		).toBeInTheDocument();
		expect(document.getElementById("limits-db-memory")).toBeInvalid();
		expect(document.getElementById("limits-web-memory")).toBeValid();
	});

	test("drops the limits of a component the compose text no longer declares", async () => {
		let latest: SettingsDraft | undefined;
		const service = makeStack();
		service.config.stack = {
			...stackOf(service),
			resources: {
				db: { memoryMb: 128 },
				gone: { memoryMb: 64 },
			},
		};
		await renderWithProviders(
			<Form service={service} onDraft={(d) => (latest = d)}>
				{(props) => (
					<StackLimitsSection
						{...props}
						service={service}
						components={[at(COMPONENTS, 0), at(COMPONENTS, 1)]}
					/>
				)}
			</Form>,
		);
		await waitFor(() =>
			expect(Object.keys(latest?.stackResources ?? {})).toEqual(["db", "web"]),
		);
		expect(latest?.stackResources.db?.memoryMb).toBe("128");
		expect(latest?.stackResources.web).toEqual({ memoryMb: "", cpus: "" });
	});

	test("keeps the limits while the compose text declares no components at all", async () => {
		let latest: SettingsDraft | undefined;
		const service = makeStack();
		service.config.stack = {
			...stackOf(service),
			resources: { db: { memoryMb: 128 } },
		};
		await renderWithProviders(
			<Form service={service} onDraft={(d) => (latest = d)}>
				{(props) => (
					<StackLimitsSection {...props} service={service} components={[]} />
				)}
			</Form>,
		);
		expect(latest).toBeUndefined();
	});
});

describe("StackNetworkingSection", () => {
	const ROUTES = {
		"GET /networks": { body: make("ListNetworksOutputBody", { items: [] }) },
		"GET /integrations/cloudflare": {
			body: make("CloudflareConnectionView", {
				configured: false,
				status: "",
			}),
		},
		"GET /servers": { body: make("PageServer", { items: [] }) },
	};

	function section(service: Service, onDraft?: (d: SettingsDraft) => void) {
		return renderWithProviders(
			<Form service={service} onDraft={onDraft}>
				{(props) => (
					<StackNetworkingSection
						{...props}
						service={service}
						components={service.config.stack?.components ?? []}
					/>
				)}
			</Form>,
		);
	}

	test("a stack without components cannot have a domain", async () => {
		stubApi(ROUTES);
		await section(makeStack({ components: [] }));
		expect(
			screen.getByText("A stack needs components before it can have a domain."),
		).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "Generate domain" }),
		).toBeDisabled();
		expect(screen.getByLabelText("New domain")).toBeDisabled();
	});

	test("with domain-less components it offers adding or generating one", async () => {
		stubApi(ROUTES);
		await section(makeStack());
		expect(
			screen.getByText(/No domain yet\. Add one for a component/),
		).toBeInTheDocument();
		// Several components: the user must pick which one to generate for.
		const generate = screen.getByRole("button", { name: "Generate domain" });
		expect(generate).toBeDisabled();
		expect(generate).toHaveAttribute(
			"title",
			"Pick the component the domain serves",
		);
	});

	test("a single component is chosen automatically and Generate domain posts it", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			...ROUTES,
			"POST /services/:serviceId/domains/generate": {
				status: 201,
				body: make("DomainView", {
					hostname: "shop.node-1.example.com",
					process: "web",
				}),
			},
		});
		await section(
			makeStack({ id: "stack-7", components: [at(COMPONENTS, 1)] }),
		);
		const generate = screen.getByRole("button", { name: "Generate domain" });
		expect(generate).toBeEnabled();
		await user.click(generate);
		expect(await screen.findByText("Domain generated")).toBeInTheDocument();
		expect(
			screen.getByText("Staged until the next deploy."),
		).toBeInTheDocument();
		const call = api.calls.find((c) => c.method === "POST");
		expect(call?.path).toBe("/services/stack-7/domains/generate");
		expect(call?.query.get("component")).toBe("web");
		expect(api.unmatched).toEqual([]);
	});

	test("picking a component enables Generate domain for it", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			...ROUTES,
			"POST /services/:serviceId/domains/generate": {
				status: 201,
				body: make("DomainView", {
					hostname: "shop.node-1.example.com",
					process: "web",
				}),
			},
		});
		await section(makeStack({ id: "stack-7" }));
		await user.click(
			screen.getByLabelText("Component to generate a domain for"),
		);
		await user.click(await screen.findByRole("option", { name: "worker" }));
		const generate = screen.getByRole("button", { name: "Generate domain" });
		expect(generate).toBeEnabled();
		await user.click(generate);
		await waitFor(() =>
			expect(api.calls.some((c) => c.method === "POST")).toBe(true),
		);
		expect(
			api.calls.find((c) => c.method === "POST")?.query.get("component"),
		).toBe("worker");
	});

	test("a failed generation shows the server's message", async () => {
		const user = userEvent.setup();
		stubApi({
			...ROUTES,
			"POST /services/:serviceId/domains/generate": problem(409, {
				title: "Conflict",
				status: 409,
				detail: "The server has no base domain.",
			}),
		});
		await section(makeStack({ components: [at(COMPONENTS, 1)] }));
		await user.click(screen.getByRole("button", { name: "Generate domain" }));
		expect(
			await screen.findByText(/The server has no base domain\./),
		).toBeInTheDocument();
		expect(screen.queryByText("Domain generated")).not.toBeInTheDocument();
	});

	test("lists the stack's domains with the component they route to", async () => {
		stubApi(ROUTES);
		await section(
			makeStack({
				domains: [
					{
						id: "d1",
						hostname: "shop.example.com",
						component: "web",
						targetPort: 80,
						pathPrefix: "",
						status: "active",
					} as Service["domains"][number],
				],
			}),
		);
		expect(screen.queryByText(/No domain yet/)).not.toBeInTheDocument();
		expect(
			(await screen.findAllByText(/shop\.example\.com/)).length,
		).toBeGreaterThan(0);
	});

	test("the outbound switch reflects and edits blockEgress", async () => {
		const user = userEvent.setup();
		stubApi(ROUTES);
		let latest: SettingsDraft | undefined;
		await section(makeStack(), (d) => (latest = d));
		const toggle = screen.getByRole("switch", {
			name: "Block outbound internet",
		});
		expect(toggle).not.toBeChecked();
		expect(
			screen.getByText("Allowed: can reach the internet"),
		).toBeInTheDocument();
		await user.click(toggle);
		expect(latest?.blockEgress).toBe(true);
		expect(
			screen.getByText("Blocked: no outbound internet"),
		).toBeInTheDocument();
	});

	test("a stack on a shared network cannot block outbound until it detaches", async () => {
		stubApi(ROUTES);
		const service = makeStack({
			networks: [
				{ id: "net-1", name: "shared", alias: "shop.shared" },
			] as Service["networks"],
		});
		await section(service);
		expect(
			screen.getByRole("switch", { name: "Block outbound internet" }),
		).toHaveAttribute("aria-disabled", "true");
		expect(
			screen.getByText(/Detach this service from its shared networks first/),
		).toBeInTheDocument();
	});

	test("an already blocked stack can be switched back even with networks", async () => {
		stubApi(ROUTES);
		const service = makeStack({
			networks: [
				{ id: "net-1", name: "shared", alias: "shop.shared" },
			] as Service["networks"],
		});
		service.config.blockEgress = true;
		await section(service);
		expect(
			screen.getByRole("switch", { name: "Block outbound internet" }),
		).not.toHaveAttribute("aria-disabled", "true");
		expect(
			screen.queryByText(/Detach this service from its shared networks first/),
		).not.toBeInTheDocument();
	});

	test("includes the add-domain form and the shared networks", async () => {
		stubApi({
			...ROUTES,
			"GET /networks": {
				body: make("ListNetworksOutputBody", {
					items: [
						make("NetworkView", {
							id: "net-1",
							name: "backend-net",
							serverId: "server-1",
						}),
					],
				}),
			},
		});
		await section(makeStack());
		expect(screen.getByLabelText("New domain")).toBeEnabled();
		expect(await screen.findByText("backend-net")).toBeInTheDocument();
		expect(
			within(screen.getByRole("region", { name: "Networking" })).getByText(
				"Domains",
			),
		).toBeInTheDocument();
	});
});
