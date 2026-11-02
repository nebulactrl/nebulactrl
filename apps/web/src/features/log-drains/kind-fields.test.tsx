import { test as bunTest, describe, expect } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { KindFields } from "./kind-fields";
import { type Draft, mapFromRows } from "./model";
import { DraftHarness, newHost } from "./test-harness";

/** Form tests type into a large dialog; allow for a loaded CI machine. */
const test = (
	name: string,
	fn: (() => void) | (() => Promise<unknown>),
): void => {
	bunTest(name, fn, 30_000);
};

function renderKind(initial: Partial<Draft>) {
	const host = newHost();
	render(
		<DraftHarness initial={initial} host={host}>
			{(draft, set, setDraft) => (
				<KindFields draft={draft} set={set} onDraft={setDraft} />
			)}
		</DraftHarness>,
	);
	return host;
}

describe("KindFields: Loki", () => {
	test("edits the push URL and tenant", async () => {
		const user = userEvent.setup();
		const host = renderKind({ kind: "loki" });
		await user.type(
			screen.getByLabelText("Push URL"),
			"https://loki.example/push",
		);
		await user.type(screen.getByLabelText("Tenant ID (optional)"), "acme");
		expect(host.draft().lokiUrl).toBe("https://loki.example/push");
		expect(host.draft().lokiTenant).toBe("acme");
	});

	test("adds a label as a name and value pair", async () => {
		const user = userEvent.setup();
		const host = renderKind({ kind: "loki" });
		await user.click(screen.getByRole("button", { name: "Add label" }));
		await user.type(screen.getByLabelText("Labels: name"), "env");
		await user.type(screen.getByLabelText("Labels: value of env"), "prod");
		expect(mapFromRows(host.draft().lokiLabels)).toEqual({ env: "prod" });
	});

	test("shows none of the other kinds' fields", () => {
		renderKind({ kind: "loki" });
		expect(screen.queryByLabelText("Logs URL")).toBeNull();
		expect(screen.queryByLabelText("Address")).toBeNull();
		expect(screen.queryByLabelText("URL")).toBeNull();
	});
});

describe("KindFields: OTLP", () => {
	test("edits the logs URL and resource attributes", async () => {
		const user = userEvent.setup();
		const host = renderKind({ kind: "otlp" });
		await user.type(
			screen.getByLabelText("Logs URL"),
			"https://otel.example/v1/logs",
		);
		await user.click(screen.getByRole("button", { name: "Add attribute" }));
		await user.type(
			screen.getByLabelText("Resource attributes: name"),
			"region",
		);
		await user.type(
			screen.getByLabelText("Resource attributes: value of region"),
			"eu",
		);
		expect(host.draft().otlpUrl).toBe("https://otel.example/v1/logs");
		expect(mapFromRows(host.draft().otlpAttributes)).toEqual({ region: "eu" });
	});
});

describe("KindFields: syslog", () => {
	test("starts on TCP + TLS with its explanation and the TLS-only fields", () => {
		renderKind({ kind: "syslog" });
		expect(screen.getByRole("button", { name: "TCP + TLS" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(
			screen.getByText("Reliable and encrypted. Use this over the internet."),
		).toBeVisible();
		expect(screen.getByLabelText("TLS server name (optional)")).toBeVisible();
	});

	test("UDP and TCP explain themselves and hide the TLS fields", async () => {
		const user = userEvent.setup();
		const host = renderKind({ kind: "syslog" });
		await user.click(screen.getByRole("button", { name: "UDP" }));
		expect(host.draft().syslogProtocol).toBe("udp");
		expect(
			screen.getByText(
				"Fast and unacknowledged: a lost datagram is not retried.",
			),
		).toBeVisible();
		expect(screen.queryByLabelText("TLS server name (optional)")).toBeNull();
		await user.click(screen.getByRole("button", { name: "TCP" }));
		expect(host.draft().syslogProtocol).toBe("tcp");
		expect(
			screen.getByText(
				"Reliable, but not encrypted: lines cross the network in clear text.",
			),
		).toBeVisible();
	});

	test("edits address, app name, facility and TLS server name", async () => {
		const user = userEvent.setup();
		const host = renderKind({ kind: "syslog" });
		await user.type(screen.getByLabelText("Address"), "logs.example.com:6514");
		await user.type(screen.getByLabelText("App name (optional)"), "nebula");
		await user.type(screen.getByLabelText("Facility (optional)"), "16");
		await user.type(
			screen.getByLabelText("TLS server name (optional)"),
			"logs.example.com",
		);
		const d = host.draft();
		expect([
			d.syslogAddress,
			d.syslogAppName,
			d.syslogFacility,
			d.syslogTlsServerName,
		]).toEqual(["logs.example.com:6514", "nebula", "16", "logs.example.com"]);
	});

	test("accepting any certificate warns that the receiver's identity is not checked", async () => {
		const user = userEvent.setup();
		const host = renderKind({ kind: "syslog" });
		expect(screen.queryByText("Identity not checked")).toBeNull();
		await user.click(
			screen.getByRole("checkbox", { name: /Accept any certificate/ }),
		);
		expect(host.draft().syslogTlsInsecure).toBe(true);
		expect(screen.getByText("Identity not checked")).toBeVisible();
		await user.click(
			screen.getByRole("checkbox", { name: /Accept any certificate/ }),
		);
		expect(host.draft().syslogTlsInsecure).toBe(false);
		expect(screen.queryByText("Identity not checked")).toBeNull();
	});
});

describe("KindFields: HTTP", () => {
	test("a preset fills the URL, field names and header names but never a key", async () => {
		const user = userEvent.setup();
		const host = renderKind({ kind: "http" });
		expect(
			screen.getByText(/A preset fills the URL and the field names/),
		).toBeVisible();
		await user.click(screen.getByRole("button", { name: "Datadog" }));
		const d = host.draft();
		expect(d.name).toBe("Datadog");
		expect(d.httpUrl).toBe(
			"https://http-intake.logs.datadoghq.com/api/v2/logs",
		);
		expect(mapFromRows(d.httpExtraFields)).toEqual({ ddsource: "nebulactrl" });
		expect(d.headers.map((r) => [r.name, r.value])).toEqual([
			["DD-API-KEY", ""],
		]);
		expect(screen.getByLabelText("URL")).toHaveValue(
			"https://http-intake.logs.datadoghq.com/api/v2/logs",
		);
		expect(screen.getByRole("button", { name: "Datadog" })).toHaveAttribute(
			"aria-pressed",
			"true",
		);
		expect(
			screen.getByText(
				/Paste your API key as the value of the DD-API-KEY header/,
			),
		).toBeVisible();
	});

	test("switching preset replaces the previous one's fields and hint", async () => {
		const user = userEvent.setup();
		const host = renderKind({ kind: "http" });
		await user.click(screen.getByRole("button", { name: "Datadog" }));
		await user.click(screen.getByRole("button", { name: "Axiom" }));
		const d = host.draft();
		expect(d.name).toBe("Datadog");
		expect(d.httpUrl).toBe("https://api.axiom.co/v1/datasets/<dataset>/ingest");
		expect(d.httpTimestampField).toBe("_time");
		expect(d.httpExtraFields).toEqual([]);
		expect(screen.getByRole("button", { name: "Datadog" })).toHaveAttribute(
			"aria-pressed",
			"false",
		);
		expect(
			screen.getByText(/Replace <dataset> with the dataset's name/),
		).toBeVisible();
	});

	test("edits URL, method, body format, time and message fields and extra fields", async () => {
		const user = userEvent.setup();
		const host = renderKind({ kind: "http" });
		await user.type(
			screen.getByLabelText("URL"),
			"https://logs.example.com/ingest",
		);
		await user.click(screen.getByRole("button", { name: "PUT" }));
		await user.click(screen.getByRole("button", { name: "One JSON per line" }));
		await user.type(screen.getByLabelText("Time field (optional)"), "ts");
		await user.type(screen.getByLabelText("Message field (optional)"), "msg");
		await user.click(screen.getByRole("button", { name: "Add field" }));
		await user.type(screen.getByLabelText("Extra fields: name"), "ddsource");
		await user.type(
			screen.getByLabelText("Extra fields: value of ddsource"),
			"nebula",
		);
		const d = host.draft();
		expect({
			url: d.httpUrl,
			method: d.httpMethod,
			format: d.httpFormat,
			ts: d.httpTimestampField,
			msg: d.httpMessageField,
			extra: mapFromRows(d.httpExtraFields),
		}).toEqual({
			url: "https://logs.example.com/ingest",
			method: "PUT",
			format: "ndjson",
			ts: "ts",
			msg: "msg",
			extra: { ddsource: "nebula" },
		});
	});
});
