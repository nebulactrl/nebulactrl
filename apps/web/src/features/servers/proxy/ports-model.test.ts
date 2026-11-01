import { describe, expect, test } from "bun:test";
import {
	canReset,
	checkPorts,
	formOf,
	isDirty,
	type PortsView,
	portsText,
	sourceLabel,
} from "./ports-model";

const base: PortsView = {
	http: 80,
	https: 443,
	appliedHttp: 8080,
	appliedHttps: 8443,
	source: "flag",
	staged: false,
	setHttp: null,
	setHttps: null,
	agentHttp: 8080,
	agentHttps: 8443,
	busyOther: [],
	reported: true,
	supported: true,
};

describe("sourceLabel", () => {
	test("names where the ports come from", () => {
		expect(sourceLabel("flag")).toBe("Set in the agent's service flags");
		expect(sourceLabel("server")).toBe("Server setting");
		expect(sourceLabel("")).toBe("Not reported yet");
	});
});

describe("checkPorts", () => {
	test("accepts a valid pair and sends it", () => {
		const r = checkPorts({ http: "80", https: "443" }, base);
		expect(r.errors).toEqual({});
		expect(r.body).toEqual({ http: 80, https: 443 });
		expect(r.effective).toEqual({ http: 80, https: 443 });
	});
	test("blank means the agent's own port and is sent as 0", () => {
		const r = checkPorts({ http: "", https: "9443" }, base);
		expect(r.body).toEqual({ http: 0, https: 9443 });
		expect(r.effective).toEqual({ http: 8080, https: 9443 });
		expect(checkPorts({ http: "", https: "" }, base).body).toEqual({
			http: 0,
			https: 0,
		});
	});
	test("rejects non-numbers and out-of-range ports", () => {
		expect(checkPorts({ http: "eighty", https: "" }, base).errors.http).toMatch(
			/number/,
		);
		expect(checkPorts({ http: "0", https: "" }, base).errors.http).toMatch(
			/between 1 and 65535/,
		);
		expect(checkPorts({ http: "", https: "65536" }, base).errors.https).toMatch(
			/between 1 and 65535/,
		);
		expect(checkPorts({ http: "-1", https: "" }, base).body).toBeNull();
		expect(checkPorts({ http: "80.5", https: "" }, base).body).toBeNull();
	});
	test("HTTP and HTTPS must differ, also against the agent's own port", () => {
		expect(
			checkPorts({ http: "8080", https: "8080" }, base).errors.form,
		).toMatch(/different ports/);
		// Only HTTP set: it collides with the agent's own HTTPS port.
		expect(checkPorts({ http: "8443", https: "" }, base).errors.form).toMatch(
			/different ports/,
		);
	});
	test("refuses the admin port", () => {
		expect(checkPorts({ http: "9000", https: "" }, base).errors.http).toMatch(
			/admin port/,
		);
		expect(checkPorts({ http: "", https: "9000" }, base).errors.https).toMatch(
			/admin port/,
		);
	});
	test("refuses ports a TCP or UDP route reserves", () => {
		const reserved = [
			{ protocol: "tcp", port: 5432, serviceName: "pg" },
			{ protocol: "udp", port: 8443, serviceName: "dns" },
			{ protocol: "udp", port: 8081, serviceName: "voip" },
		];
		expect(
			checkPorts({ http: "5432", https: "" }, base, reserved).errors.form,
		).toMatch(/TCP port 5432 is reserved by the route pg/);
		expect(
			checkPorts({ http: "", https: "5432" }, base, reserved).errors.form,
		).toMatch(/TCP port 5432/);
		// UDP clashes only with the HTTPS port (HTTPS is published for tcp and udp).
		expect(
			checkPorts({ http: "", https: "8443" }, base, reserved).errors.form,
		).toMatch(/UDP port 8443/);
		expect(
			checkPorts({ http: "8081", https: "9443" }, base, reserved).errors,
		).toEqual({});
	});
});

describe("form state", () => {
	test("starts from the staged setting, blank when none", () => {
		expect(formOf(base)).toEqual({ http: "", https: "" });
		expect(formOf({ ...base, setHttp: 80, setHttps: 443 })).toEqual({
			http: "80",
			https: "443",
		});
	});
	test("isDirty compares trimmed text with the staged setting", () => {
		expect(isDirty({ http: "", https: "" }, base)).toBe(false);
		expect(isDirty({ http: " ", https: "" }, base)).toBe(false);
		expect(isDirty({ http: "80", https: "" }, base)).toBe(true);
	});
	test("reset is offered while something is set", () => {
		expect(canReset(base)).toBe(false);
		expect(canReset({ ...base, setHttps: 443 })).toBe(true);
	});
	test("portsText", () => {
		expect(portsText(80, 443)).toBe("80 and 443");
	});
});
