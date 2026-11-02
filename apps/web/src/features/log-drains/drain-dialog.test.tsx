import { test as bunTest, describe, expect, mock } from "bun:test";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { LogDrainView } from "#/api/gen/types.gen";
import { stubApi } from "#/test/api";
import { problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import { DrainDialog } from "./drain-dialog";
import { drainView, TEAM_ROUTES } from "./test-fixtures";

/** Form tests type into a large dialog; allow for a loaded CI machine. */
const test = (
	name: string,
	fn: (() => void) | (() => Promise<unknown>),
): void => {
	bunTest(name, fn, 30_000);
};

const CREATED = { status: 201, body: drainView() };

async function openDialog(drain: LogDrainView | null = null) {
	const onOpenChange = mock((_open: boolean) => {});
	await renderWithProviders(
		<DrainDialog open onOpenChange={onOpenChange} drain={drain} />,
	);
	return onOpenChange;
}

/** Types a long value in one go: pastes it into the labelled field. */
async function paste(
	user: ReturnType<typeof userEvent.setup>,
	label: string,
	text: string,
) {
	await user.click(screen.getByLabelText(label));
	await user.paste(text);
}

const submit = (name: "Add drain" | "Save" = "Add drain") =>
	screen.getByRole("button", { name });

/** The one drain write the dialog made. */
function write(api: ReturnType<typeof stubApi>) {
	const writes = api.calls.filter((c) => c.method !== "GET");
	expect(writes).toHaveLength(1);
	return writes[0] as (typeof api.calls)[number];
}

describe("DrainDialog: adding a drain", () => {
	test("opens on Loki with a disabled button that says what is missing", async () => {
		const api = stubApi({ ...TEAM_ROUTES });
		await openDialog();
		expect(screen.getByRole("dialog", { name: "Add log drain" })).toBeVisible();
		expect(screen.getByRole("radio", { name: /^Loki/ })).toBeChecked();
		expect(submit()).toBeDisabled();
		expect(submit()).toHaveAttribute("title", "Give the drain a name.");
		await userEvent.setup().type(screen.getByLabelText("Name"), "Grafana");
		expect(submit()).toHaveAttribute("title", "Enter the push URL.");
		expect(api.calls.some((c) => c.method !== "GET")).toBe(false);
	});

	test("sends a Loki drain with its URL, tenant, labels and credentials", async () => {
		const user = userEvent.setup();
		const api = stubApi({ ...TEAM_ROUTES, "POST /log-drains": CREATED });
		const onOpenChange = await openDialog();
		await user.type(screen.getByLabelText("Name"), "  Grafana  ");
		await paste(user, "Push URL", "https://logs.grafana.net/loki/api/v1/push");
		await user.type(screen.getByLabelText("Tenant ID (optional)"), "acme");
		await user.click(screen.getByRole("button", { name: "Add label" }));
		await user.type(screen.getByLabelText("Labels: name"), "env");
		await user.type(screen.getByLabelText("Labels: value of env"), "prod");
		await user.type(screen.getByLabelText("User name"), "12345");
		await user.type(screen.getByLabelText("Password"), "glc_secret");
		await user.click(submit());
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		const call = write(api);
		expect([call.method, call.path]).toEqual(["POST", "/log-drains"]);
		expect(call.body).toEqual({
			name: "Grafana",
			kind: "loki",
			enabled: true,
			config: {
				loki: {
					url: "https://logs.grafana.net/loki/api/v1/push",
					tenantId: "acme",
					labels: { env: "prod" },
				},
			},
			secrets: { username: "12345", password: "glc_secret" },
			allProjects: true,
			projectIds: [],
			serviceIds: [],
			query: "",
			bufferMb: 256,
		});
		expect(await screen.findByText("Log drain added")).toBeVisible();
		expect(api.unmatched).toEqual([]);
	});

	test("sends an OTLP drain with a bearer token and resource attributes", async () => {
		const user = userEvent.setup();
		const api = stubApi({ ...TEAM_ROUTES, "POST /log-drains": CREATED });
		const onOpenChange = await openDialog();
		await user.click(screen.getByRole("radio", { name: /^OpenTelemetry/ }));
		expect(screen.queryByLabelText("Push URL")).toBeNull();
		await user.type(screen.getByLabelText("Name"), "Collector");
		await paste(user, "Logs URL", "https://otel.example.com/v1/logs");
		await user.click(screen.getByRole("button", { name: "Add attribute" }));
		await user.type(
			screen.getByLabelText("Resource attributes: name"),
			"region",
		);
		await user.type(
			screen.getByLabelText("Resource attributes: value of region"),
			"eu",
		);
		await user.type(screen.getByLabelText("Bearer token"), "tok");
		await user.click(submit());
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		expect(write(api).body).toMatchObject({
			name: "Collector",
			kind: "otlp",
			config: {
				otlp: {
					url: "https://otel.example.com/v1/logs",
					resourceAttributes: { region: "eu" },
				},
			},
			secrets: { token: "tok" },
		});
	});

	test("sends a syslog drain with protocol, facility and TLS settings and no secrets", async () => {
		const user = userEvent.setup();
		const api = stubApi({ ...TEAM_ROUTES, "POST /log-drains": CREATED });
		const onOpenChange = await openDialog();
		await user.click(screen.getByRole("radio", { name: /^Syslog/ }));
		expect(screen.queryByText("Authentication")).toBeNull();
		await user.type(screen.getByLabelText("Name"), "Papertrail");
		await user.type(screen.getByLabelText("Address"), "logs.example.com:6514");
		await user.type(screen.getByLabelText("App name (optional)"), "nebula");
		await user.type(screen.getByLabelText("Facility (optional)"), "16");
		await user.click(
			screen.getByRole("checkbox", { name: /Accept any certificate/ }),
		);
		await user.click(submit());
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		expect(write(api).body).toMatchObject({
			kind: "syslog",
			config: {
				syslog: {
					address: "logs.example.com:6514",
					protocol: "tcp+tls",
					appName: "nebula",
					facility: 16,
					tlsInsecureSkipVerify: true,
				},
			},
			secrets: {},
		});
	});

	test("a syslog address without a port or with a bad facility blocks the button", async () => {
		const user = userEvent.setup();
		stubApi({ ...TEAM_ROUTES });
		await openDialog();
		await user.click(screen.getByRole("radio", { name: /^Syslog/ }));
		await user.type(screen.getByLabelText("Name"), "x");
		await user.type(screen.getByLabelText("Address"), "logs.example.com");
		expect(submit()).toBeDisabled();
		expect(submit()).toHaveAttribute(
			"title",
			"Enter the address as host:port, for example logs.example.com:6514.",
		);
		await user.type(screen.getByLabelText("Address"), ":514");
		await user.type(screen.getByLabelText("Facility (optional)"), "30");
		expect(submit()).toHaveAttribute(
			"title",
			"The facility is a number from 0 to 23.",
		);
		await user.clear(screen.getByLabelText("Facility (optional)"));
		expect(submit()).toBeEnabled();
	});

	test("an HTTP preset plus the person's key makes a Datadog drain", async () => {
		const user = userEvent.setup();
		const api = stubApi({ ...TEAM_ROUTES, "POST /log-drains": CREATED });
		const onOpenChange = await openDialog();
		await user.click(screen.getByRole("radio", { name: /^HTTP JSON/ }));
		await user.click(screen.getByRole("button", { name: "Datadog" }));
		expect(screen.getByLabelText("Name")).toHaveValue("Datadog");
		// The preset fills the header's name but not its value, so it cannot be sent yet.
		expect(submit()).toBeDisabled();
		expect(submit()).toHaveAttribute(
			"title",
			"The DD-API-KEY header needs a value.",
		);
		await user.type(
			screen.getByLabelText("Headers: value of DD-API-KEY"),
			"dd-key",
		);
		await user.click(submit());
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		expect(write(api).body).toEqual({
			name: "Datadog",
			kind: "http",
			enabled: true,
			config: {
				http: {
					url: "https://http-intake.logs.datadoghq.com/api/v2/logs",
					method: "POST",
					format: "json_array",
					extraFields: { ddsource: "nebulactrl" },
				},
			},
			secrets: { headers: { "DD-API-KEY": "dd-key" } },
			allProjects: true,
			projectIds: [],
			serviceIds: [],
			query: "",
			bufferMb: 256,
		});
	});

	test("validation messages follow the first thing wrong", async () => {
		const user = userEvent.setup();
		stubApi({ ...TEAM_ROUTES });
		await openDialog();
		await user.type(screen.getByLabelText("Name"), "x");
		const url = screen.getByLabelText("Push URL");
		await user.type(url, "ftp://x");
		expect(submit()).toHaveAttribute(
			"title",
			"The push URL must start with http:// or https://.",
		);
		await user.clear(url);
		await user.type(url, "https://loki.example/push");
		expect(submit()).toBeEnabled();
		await user.type(screen.getByLabelText("User name"), "bob");
		expect(submit()).toHaveAttribute(
			"title",
			"A user name needs its password.",
		);
		await user.type(screen.getByLabelText("Password"), "pw");
		expect(submit()).toBeEnabled();
		await user.click(screen.getByRole("button", { name: "Add header" }));
		await user.type(screen.getByLabelText("Headers: name"), "bad name");
		await user.type(screen.getByLabelText("Headers: value of bad name"), "v");
		expect(submit()).toHaveAttribute(
			"title",
			"“bad name” is not a valid header name.",
		);
	});

	test("the buffer must be in range; the default is sent when untouched", async () => {
		const user = userEvent.setup();
		const api = stubApi({ ...TEAM_ROUTES, "POST /log-drains": CREATED });
		const onOpenChange = await openDialog();
		await user.type(screen.getByLabelText("Name"), "x");
		await paste(user, "Push URL", "https://loki.example/push");
		const buffer = screen.getByLabelText("Buffer (MB)");
		expect(buffer).toHaveValue(256);
		await user.clear(buffer);
		await user.type(buffer, "4");
		expect(submit()).toHaveAttribute("title", "The buffer is 8 to 5120 MB.");
		await user.clear(buffer);
		await user.type(buffer, "512");
		expect(submit()).toBeEnabled();
		await user.click(submit());
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		expect((write(api).body as { bufferMb: number }).bufferMb).toBe(512);
	});

	test("an invalid log query blocks the button and shows the parser's message", async () => {
		const user = userEvent.setup();
		stubApi({ ...TEAM_ROUTES });
		await openDialog();
		await user.type(screen.getByLabelText("Name"), "x");
		await paste(user, "Push URL", "https://loki.example/push");
		await user.type(screen.getByLabelText("Query (optional)"), '"open');
		expect(submit()).toBeDisabled();
		expect(submit().getAttribute("title")).toMatch(/^The query has a mistake/);
		expect(screen.getByLabelText("Query (optional)")).toHaveAttribute(
			"aria-invalid",
			"true",
		);
	});

	test("selected projects and services narrow the scope sent", async () => {
		const user = userEvent.setup();
		const api = stubApi({ ...TEAM_ROUTES, "POST /log-drains": CREATED });
		const onOpenChange = await openDialog();
		await user.type(screen.getByLabelText("Name"), "Scoped");
		await paste(user, "Push URL", "https://loki.example/push");
		await user.click(screen.getByRole("button", { name: "Selected projects" }));
		expect(submit()).toHaveAttribute(
			"title",
			"Choose at least one project, or ship from all projects.",
		);
		await user.click(await screen.findByRole("checkbox", { name: "Web" }));
		await user.click(await screen.findByRole("checkbox", { name: /^api/ }));
		await user.type(screen.getByLabelText("Query (optional)"), "level:error");
		await user.click(submit());
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		expect(write(api).body).toMatchObject({
			allProjects: false,
			projectIds: ["p-web"],
			serviceIds: ["svc-api"],
			query: "level:error",
		});
	});

	test("a ticked service whose project is then unticked is not sent", async () => {
		const user = userEvent.setup();
		const api = stubApi({ ...TEAM_ROUTES, "POST /log-drains": CREATED });
		const onOpenChange = await openDialog();
		await user.type(screen.getByLabelText("Name"), "Scoped");
		await paste(user, "Push URL", "https://loki.example/push");
		await user.click(await screen.findByRole("checkbox", { name: /^cron/ }));
		await user.click(screen.getByRole("checkbox", { name: /^api/ }));
		await user.click(screen.getByRole("button", { name: "Selected projects" }));
		await user.click(await screen.findByRole("checkbox", { name: "Web" }));
		// cron lives in Ops, which is not selected; api stays.
		await user.click(submit());
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		expect(write(api).body).toMatchObject({
			projectIds: ["p-web"],
			serviceIds: ["svc-api"],
		});
	});

	test("a server-side rejection shows its message under the form and keeps the dialog open", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			...TEAM_ROUTES,
			"POST /log-drains": problem(422, {
				title: "Unprocessable",
				detail: "push URL host is not allowed",
				code: "invalid",
			}),
		});
		const onOpenChange = await openDialog();
		await user.type(screen.getByLabelText("Name"), "x");
		await paste(user, "Push URL", "https://10.0.0.1/push");
		await user.click(submit());
		expect(
			await screen.findByText("push URL host is not allowed"),
		).toBeVisible();
		expect(onOpenChange).not.toHaveBeenCalled();
		expect(screen.getByRole("dialog", { name: "Add log drain" })).toBeVisible();
		expect(screen.queryByText("Log drain added")).toBeNull();
		expect(write(api).path).toBe("/log-drains");
	});

	test("Cancel closes without saving", async () => {
		const user = userEvent.setup();
		const api = stubApi({ ...TEAM_ROUTES });
		const onOpenChange = await openDialog();
		await user.type(screen.getByLabelText("Name"), "x");
		await user.click(screen.getByRole("button", { name: "Cancel" }));
		expect(onOpenChange).toHaveBeenCalledWith(false);
		expect(api.calls.some((c) => c.method !== "GET")).toBe(false);
	});

	test("Enter in the name field does not submit an incomplete form", async () => {
		const user = userEvent.setup();
		const api = stubApi({ ...TEAM_ROUTES });
		await openDialog();
		await user.type(screen.getByLabelText("Name"), "x{Enter}");
		expect(api.calls.some((c) => c.method !== "GET")).toBe(false);
	});
});

describe("DrainDialog: editing a drain", () => {
	const STORED = drainView({
		id: "d-9",
		name: "Grafana",
		kind: "loki",
		loki: {
			url: "https://logs.grafana.net/loki/api/v1/push",
			tenantId: "acme",
			labels: { env: "prod" },
		},
		secrets: { username: "12••", password: "••••" },
		headerNames: ["X-Api-Key"],
		allProjects: false,
		projectIds: ["p-web"],
		serviceIds: ["svc-api"],
		query: "level:error",
		bufferMb: 64,
	});

	test("shows the stored settings, the fixed kind and no type picker", async () => {
		stubApi({ ...TEAM_ROUTES });
		await openDialog(STORED);
		expect(screen.getByRole("dialog", { name: "Edit Grafana" })).toBeVisible();
		expect(screen.queryByRole("radio")).toBeNull();
		expect(screen.getByText(/The kind cannot be changed/)).toBeVisible();
		expect(screen.getByLabelText("Name")).toHaveValue("Grafana");
		expect(screen.getByLabelText("Push URL")).toHaveValue(
			"https://logs.grafana.net/loki/api/v1/push",
		);
		expect(screen.getByLabelText("Tenant ID (optional)")).toHaveValue("acme");
		expect(screen.getByLabelText("Labels: value of env")).toHaveValue("prod");
		expect(screen.getByLabelText("Query (optional)")).toHaveValue(
			"level:error",
		);
		expect(screen.getByLabelText("Buffer (MB)")).toHaveValue(64);
		expect(screen.getByLabelText(/^Password/)).toHaveValue("");
		expect(screen.getByLabelText(/^Password/)).toHaveAttribute(
			"placeholder",
			"••••",
		);
		expect(screen.getByText("X-Api-Key")).toBeVisible();
	});

	test("saving unchanged PATCHes the drain without touching its credentials", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			...TEAM_ROUTES,
			"PATCH /log-drains/d-9": { body: STORED },
		});
		const onOpenChange = await openDialog(STORED);
		await user.click(await screen.findByRole("checkbox", { name: /^api/ }));
		await user.click(screen.getByRole("checkbox", { name: /^api/ }));
		await user.click(submit("Save"));
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		const call = write(api);
		expect([call.method, call.path]).toEqual(["PATCH", "/log-drains/d-9"]);
		expect(call.body).toEqual({
			name: "Grafana",
			enabled: true,
			config: {
				loki: {
					url: "https://logs.grafana.net/loki/api/v1/push",
					tenantId: "acme",
					labels: { env: "prod" },
				},
			},
			secrets: {},
			allProjects: false,
			projectIds: ["p-web"],
			serviceIds: ["svc-api"],
			query: "level:error",
			bufferMb: 64,
		});
		expect(await screen.findByText("Log drain saved")).toBeVisible();
	});

	test("replacing, removing and re-keying credentials is sent as such", async () => {
		const user = userEvent.setup();
		const api = stubApi({
			...TEAM_ROUTES,
			"PATCH /log-drains/d-9": { body: STORED },
		});
		const onOpenChange = await openDialog(STORED);
		await user.type(screen.getByLabelText(/^Password/), "new-pass");
		// Remove buttons: user name, password, then (no token stored) none.
		await user.click(
			screen.getAllByRole("button", { name: "Remove" })[0] as HTMLElement,
		);
		await user.click(
			screen.getByRole("button", { name: "Remove all headers" }),
		);
		await user.click(submit("Save"));
		await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
		expect((write(api).body as { secrets: unknown }).secrets).toEqual({
			password: "new-pass",
			clear: ["username", "headers"],
		});
	});

	test("clearing the name blocks saving until a name is typed again", async () => {
		const user = userEvent.setup();
		stubApi({
			...TEAM_ROUTES,
			"PATCH /log-drains/d-9": { body: STORED },
		});
		await openDialog(STORED);
		await user.clear(screen.getByLabelText("Name"));
		expect(submit("Save")).toBeDisabled();
		await user.type(screen.getByLabelText("Name"), "Renamed");
		expect(submit("Save")).toBeEnabled();
	});

	test("a failed save shows the server's message and keeps the edits", async () => {
		const user = userEvent.setup();
		stubApi({
			...TEAM_ROUTES,
			"PATCH /log-drains/d-9": problem(403, {
				title: "Forbidden",
				detail: "Only team admins can change drains",
				code: "forbidden",
			}),
		});
		const onOpenChange = await openDialog(STORED);
		await user.type(screen.getByLabelText("Name"), " 2");
		await user.click(submit("Save"));
		expect(
			await screen.findByText("Only team admins can change drains"),
		).toBeVisible();
		expect(onOpenChange).not.toHaveBeenCalled();
		expect(screen.getByLabelText("Name")).toHaveValue("Grafana 2");
	});

	test("an unticked project hides the form's services of that project", async () => {
		const user = userEvent.setup();
		stubApi({ ...TEAM_ROUTES });
		await openDialog(STORED);
		const scope = await screen.findByRole("checkbox", { name: /^api/ });
		expect(scope).toBeChecked();
		expect(
			within(screen.getByRole("dialog")).queryByRole("checkbox", {
				name: /^cron/,
			}),
		).toBeNull();
		await user.click(screen.getByRole("checkbox", { name: "Web" }));
		expect(screen.queryByRole("checkbox", { name: /^api/ })).toBeNull();
	});
});

describe("DrainDialog: lifecycle", () => {
	test("makes no requests while closed", async () => {
		const api = stubApi({ ...TEAM_ROUTES });
		await renderWithProviders(
			<DrainDialog open={false} onOpenChange={() => {}} drain={null} />,
		);
		expect(api.calls).toEqual([]);
		expect(screen.queryByRole("dialog")).toBeNull();
	});
});
