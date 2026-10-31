import { describe, expect, test } from "bun:test";
import type { PublicPortView } from "#/api/gen/types.gen";
import {
	connectionAddress,
	isPublicPortDirty,
	NO_PUBLIC_ADDRESS,
	needsOpenConfirmation,
	parsePublicPort,
	publicPortError,
	publicPortStatus,
	revealProblem,
	toPublicPortForm,
	toSetRequest,
	validatePublicPort,
	willRecreateProxy,
} from "./public-port";

const off: PublicPortView = {
	enabled: false,
	allowCidrs: null,
	denyCidrs: null,
	deployed: false,
	newEntrypoint: false,
	openToAll: false,
	staged: false,
	suggestedPort: 15432,
	tls: "passthrough",
	warnings: null,
};

const on: PublicPortView = {
	...off,
	enabled: true,
	routeId: "route-1",
	listenPort: 15432,
	allowCidrs: ["10.0.0.0/8"],
	denyCidrs: ["10.1.0.0/16"],
	deployed: true,
	serverId: "srv-1",
	serverName: "hetzner-01",
	host: "203.0.113.7",
};

describe("the form", () => {
	test("an off port starts from the suggested port with an empty allow list", () => {
		expect(toPublicPortForm(off)).toEqual({
			port: "15432",
			allow: [],
			deny: [],
			confirm: "",
		});
		expect(toPublicPortForm(undefined).port).toBe("");
		expect(toPublicPortForm({ ...off, suggestedPort: 0 }).port).toBe("");
	});
	test("an enabled port shows what is saved; the slug is typed again", () => {
		expect(toPublicPortForm(on)).toEqual({
			port: "15432",
			allow: ["10.0.0.0/8"],
			deny: ["10.1.0.0/16"],
			confirm: "",
		});
	});
	test("parsePublicPort accepts 1024 to 65535 only", () => {
		expect(parsePublicPort("1024")).toBe(1024);
		expect(parsePublicPort(" 65535 ")).toBe(65535);
		for (const bad of [
			"",
			"80",
			"1023",
			"65536",
			"5432.5",
			"-1",
			"abc",
			"012345",
		]) {
			expect(parsePublicPort(bad)).toBeNull();
		}
	});
});

describe("validatePublicPort", () => {
	const form = toPublicPortForm({ ...off });
	test("an empty allow list blocks Save and says why", () => {
		const problems = validatePublicPort(form, "orders-db");
		expect(problems.valid).toBe(false);
		expect(problems.reason).toBe("Add at least one allowed network.");
	});
	test("a valid allow list is enough", () => {
		const problems = validatePublicPort(
			{ ...form, allow: ["203.0.113.0/24"] },
			"orders-db",
		);
		expect(problems.valid).toBe(true);
		expect(problems.reason).toBeUndefined();
	});
	test("the port is checked first", () => {
		const problems = validatePublicPort(
			{ ...form, port: "80", allow: ["10.0.0.0/8"] },
			"orders-db",
		);
		expect(problems.valid).toBe(false);
		expect(problems.port).toContain("1024");
		expect(problems.reason).toBe(problems.port);
	});
	test("a bad network is named under its row", () => {
		const problems = validatePublicPort(
			{ ...form, allow: ["10.0.0.0/8", "nope"], deny: ["x"] },
			"orders-db",
		);
		expect(problems.valid).toBe(false);
		expect(problems.check.entries["allowCidrs.1"]).toBeDefined();
		expect(problems.check.entries["denyCidrs.0"]).toBeDefined();
		expect(problems.reason).toBe("Fix the highlighted networks.");
	});
	test("a deny list that covers the allow list is refused with the backend's words", () => {
		const problems = validatePublicPort(
			{ ...form, allow: ["10.0.0.0/8"], deny: ["10.0.0.0/7"] },
			"orders-db",
		);
		expect(problems.valid).toBe(false);
		expect(problems.reason).toContain("deny every address");
	});
	test("0.0.0.0/0 or ::/0 needs the slug typed, exactly", () => {
		for (const open of ["0.0.0.0/0", "::/0"]) {
			const open1 = { ...form, allow: ["10.0.0.0/8", open] };
			expect(needsOpenConfirmation(open1.allow)).toBe(true);
			const blocked = validatePublicPort(open1, "orders-db");
			expect(blocked.valid).toBe(false);
			expect(blocked.confirm).toContain("orders-db");
			expect(
				validatePublicPort({ ...open1, confirm: "orders" }, "orders-db").valid,
			).toBe(false);
			expect(
				validatePublicPort({ ...open1, confirm: " orders-db " }, "orders-db")
					.valid,
			).toBe(true);
		}
		expect(needsOpenConfirmation(["10.0.0.0/8"])).toBe(false);
	});
});

describe("toSetRequest", () => {
	test("port, allow list and nothing else for a plain list", () => {
		expect(
			toSetRequest(
				{
					port: " 15432 ",
					allow: [" 10.0.0.0/8 ", "", "10.1.2.3/8"],
					deny: [],
					confirm: "",
				},
				"orders-db",
			),
		).toEqual({ listenPort: 15432, allowCidrs: ["10.0.0.0/8"] });
	});
	test("the deny list is sent when there is one", () => {
		expect(
			toSetRequest(
				{
					port: "15432",
					allow: ["10.0.0.0/8"],
					deny: ["10.1.0.0/16"],
					confirm: "",
				},
				"orders-db",
			),
		).toEqual({
			listenPort: 15432,
			allowCidrs: ["10.0.0.0/8"],
			denyCidrs: ["10.1.0.0/16"],
		});
	});
	test("an open list carries confirmOpen with the slug", () => {
		expect(
			toSetRequest(
				{
					port: "15432",
					allow: ["::/0"],
					deny: ["fd00::/8"],
					confirm: "orders-db",
				},
				"orders-db",
			),
		).toEqual({
			listenPort: 15432,
			allowCidrs: ["::/0"],
			denyCidrs: ["fd00::/8"],
			confirmOpen: "orders-db",
		});
	});
	test("a form loaded from the view round-trips to the same request", () => {
		expect(toSetRequest(toPublicPortForm(on), "orders-db")).toEqual({
			listenPort: 15432,
			allowCidrs: ["10.0.0.0/8"],
			denyCidrs: ["10.1.0.0/16"],
		});
	});
});

describe("isPublicPortDirty", () => {
	test("an off port is always a new setting", () => {
		expect(isPublicPortDirty(off, toPublicPortForm(off))).toBe(true);
	});
	test("an enabled port is dirty only when the port or a list changed", () => {
		const form = toPublicPortForm(on);
		expect(isPublicPortDirty(on, form)).toBe(false);
		expect(isPublicPortDirty(on, { ...form, port: "15433" })).toBe(true);
		expect(isPublicPortDirty(on, { ...form, allow: ["10.1.2.3/8"] })).toBe(
			false,
		);
		expect(
			isPublicPortDirty(on, { ...form, allow: [...form.allow, "::1"] }),
		).toBe(true);
		expect(isPublicPortDirty(on, { ...form, deny: [] })).toBe(true);
		expect(isPublicPortDirty(on, { ...form, confirm: "x" })).toBe(false);
	});
});

describe("willRecreateProxy", () => {
	test("a port that is not the applied one recreates the proxy", () => {
		expect(willRecreateProxy(off, 15432)).toBe(true);
		expect(willRecreateProxy(on, 15433)).toBe(true);
	});
	test("the applied port does not", () => {
		expect(willRecreateProxy(on, 15432)).toBe(false);
	});
	test("a staged port follows the view's newEntrypoint", () => {
		const staged = { ...on, deployed: false, staged: true };
		expect(willRecreateProxy({ ...staged, newEntrypoint: true }, 15432)).toBe(
			true,
		);
		expect(willRecreateProxy({ ...staged, newEntrypoint: false }, 15432)).toBe(
			false,
		);
	});
	test("an invalid port says nothing", () => {
		expect(willRecreateProxy(off, null)).toBe(false);
	});
});

describe("connectionAddress", () => {
	test("host:port, with an IPv6 host in brackets", () => {
		expect(connectionAddress(on)).toBe("203.0.113.7:15432");
		expect(connectionAddress({ ...on, host: "2001:db8::7" })).toBe(
			"[2001:db8::7]:15432",
		);
		expect(connectionAddress({ ...on, host: "db.example.com" })).toBe(
			"db.example.com:15432",
		);
	});
	test("nothing while the server has no public address or the port is off", () => {
		expect(connectionAddress({ ...on, host: "" })).toBeNull();
		const { host: _host, ...hostless } = on;
		expect(connectionAddress(hostless)).toBeNull();
		expect(connectionAddress(off)).toBeNull();
	});
});

describe("publicPortStatus", () => {
	test("staged says to deploy the route", () => {
		expect(publicPortStatus({ ...on, staged: true, deployed: false })).toEqual({
			tone: "staged",
			label: "Staged",
			detail: "Deploy the route to apply.",
		});
	});
	test("applied names the server and the port", () => {
		expect(publicPortStatus(on)).toEqual({
			tone: "live",
			label: "Applied",
			detail: "Open on hetzner-01, port 15432.",
		});
	});
	test("an enabled port that is neither staged nor deployed is not applied", () => {
		expect(publicPortStatus({ ...on, deployed: false })?.label).toBe(
			"Not applied",
		);
	});
	test("an off port with nothing staged has no status", () => {
		expect(publicPortStatus(off)).toBeNull();
	});
});

describe("error mapping", () => {
	test("each refusal lands on its field", () => {
		expect(publicPortError("open_confirmation_required", "x").field).toBe(
			"confirm",
		);
		expect(publicPortError("allowlist_required", "x").field).toBe("allow");
		expect(
			publicPortError("invalid_listen_port", "Port 80 is reserved").message,
		).toBe("Port 80 is reserved");
		expect(publicPortError("port_in_use", "").field).toBe("port");
		expect(publicPortError("port_in_use", "").message).toContain("in use");
		expect(publicPortError("no_server", "x").field).toBe("general");
		expect(publicPortError("agent_too_old", "x").message).toContain("too old");
		expect(publicPortError("invalid_config", "bad config")).toEqual({
			field: "general",
			message: "bad config",
		});
		expect(publicPortError(undefined, "boom").message).toBe("boom");
	});
	test("a reveal without a public address says where to set it", () => {
		expect(revealProblem("no_public_address", "x")).toBe(NO_PUBLIC_ADDRESS);
		expect(NO_PUBLIC_ADDRESS).toContain(
			"Set the server public IP in the server settings",
		);
		expect(revealProblem("public_port_disabled", "x")).toContain("closed");
		expect(revealProblem("other", "boom")).toBe("boom");
	});
});
