import { describe, expect, test } from "bun:test";
import { makeService } from "#/features/canvas/lib/fixtures";
import {
	buildRoutePatch,
	routeDirty,
	toRouteForm,
	validateRouteForm,
} from "./route-form";

const http = makeService({
	id: "r1",
	name: "home",
	kind: "route",
	sourceType: "external",
	config: {
		...makeService().config,
		route: {
			protocol: "http",
			scheme: "http",
			upstreams: [{ address: "192.168.1.20:8123" }],
		},
	},
});

const tcp = makeService({
	id: "r2",
	name: "mc",
	kind: "route",
	sourceType: "external",
	config: {
		...makeService().config,
		route: {
			protocol: "tcp",
			listenPort: 25565,
			allowCidrs: ["10.0.0.0/8"],
			upstreams: [{ serviceId: "svc", port: 25565 }],
		},
	},
});

describe("toRouteForm / buildRoutePatch", () => {
	test("an untouched form is clean", () => {
		expect(routeDirty(http, toRouteForm(http), "http")).toBe(false);
		expect(routeDirty(tcp, toRouteForm(tcp), "tcp")).toBe(false);
	});
	test("http: only changed fields go out", () => {
		const form = toRouteForm(http);
		form.scheme = "https";
		form.insecureSkipVerify = true;
		form.upstreams = [{ kind: "address", address: " 192.168.1.21:8123 " }];
		expect(buildRoutePatch(http, form, "http")).toEqual({
			scheme: "https",
			insecureSkipVerify: true,
			upstreams: [{ address: "192.168.1.21:8123" }],
		});
	});
	test("tcp: port, allow-list and the open flag", () => {
		const form = toRouteForm(tcp);
		form.listenPort = "25566";
		form.cidrs = [];
		form.openToInternet = true;
		expect(buildRoutePatch(tcp, form, "tcp")).toEqual({
			listenPort: 25566,
			allowCidrs: [],
			openToInternet: true,
		});
	});
	test("tcp: adding a network turns the open flag off", () => {
		const route = tcp.config.route;
		if (!route) throw new Error("the tcp fixture has no route");
		const open = makeService({
			...tcp,
			config: {
				...tcp.config,
				route: { ...route, allowCidrs: [], openToInternet: true },
			},
		});
		const form = toRouteForm(open);
		form.cidrs = ["192.168.0.0/16"];
		form.openToInternet = true;
		expect(buildRoutePatch(open, form, "tcp")).toEqual({
			allowCidrs: ["192.168.0.0/16"],
			openToInternet: false,
		});
	});
	test("tcp: a deny list is sent with the patch; respelling is not an edit", () => {
		const form = toRouteForm(tcp);
		expect(form.denyCidrs).toEqual([]);
		form.denyCidrs = ["10.1.0.0/16", " 10.1.0.0/16 ", ""];
		expect(buildRoutePatch(tcp, form, "tcp")).toEqual({
			denyCidrs: ["10.1.0.0/16"],
		});
		form.cidrs = ["10.1.2.3/8"];
		form.denyCidrs = [];
		expect(routeDirty(tcp, form, "tcp")).toBe(false);
	});
	test("tcp: everyone except the denied networks needs the open flag", () => {
		const route = tcp.config.route;
		if (!route) throw new Error("the tcp fixture has no route");
		const denied = makeService({
			...tcp,
			config: {
				...tcp.config,
				route: {
					...route,
					allowCidrs: [],
					denyCidrs: ["203.0.113.0/24"],
					openToInternet: true,
				},
			},
		});
		const form = toRouteForm(denied);
		expect(form.denyCidrs).toEqual(["203.0.113.0/24"]);
		expect(form.openToInternet).toBe(true);
		expect(routeDirty(denied, form, "tcp")).toBe(false);
		expect(validateRouteForm(form, "tcp")).toEqual({});
		form.openToInternet = false;
		expect(validateRouteForm(form, "tcp").access).toBeDefined();
	});
	test("a managed upstream keeps its service and a changed port", () => {
		const form = toRouteForm(tcp);
		form.upstreams = [{ kind: "service", serviceId: "svc", port: "25570" }];
		expect(buildRoutePatch(tcp, form, "tcp")).toEqual({
			upstreams: [{ serviceId: "svc", port: 25570 }],
		});
	});
});

describe("validateRouteForm", () => {
	test("http: addresses and the health path", () => {
		const form = toRouteForm(http);
		form.upstreams = [{ kind: "address", address: "nas" }];
		form.healthPath = "health";
		const errors = validateRouteForm(form, "http");
		expect(errors["upstreams.0"]).toBeDefined();
		expect(errors.healthPath).toBeDefined();
		expect(validateRouteForm(toRouteForm(http), "http")).toEqual({});
	});
	test("a route needs an upstream and a name", () => {
		const form = toRouteForm(http);
		form.name = " ";
		form.upstreams = [];
		const errors = validateRouteForm(form, "http");
		expect(errors.name).toBeDefined();
		expect(errors.upstreams).toBeDefined();
	});
	test("tcp: reserved and taken ports, access choice", () => {
		const form = toRouteForm(tcp);
		form.listenPort = "443";
		expect(validateRouteForm(form, "tcp").listenPort).toBeDefined();
		form.listenPort = "25570";
		const ports = {
			taken: [
				{
					protocol: "tcp",
					port: 25570,
					serviceId: "other",
					serviceName: "web",
				},
			],
			selfServiceId: "r2",
		};
		expect(validateRouteForm(form, "tcp", ports).listenPort).toContain("web");
		form.listenPort = "25565";
		expect(validateRouteForm(form, "tcp", ports)).toEqual({});
		form.cidrs = [];
		expect(validateRouteForm(form, "tcp", ports).access).toBeDefined();
		form.openToInternet = true;
		expect(validateRouteForm(form, "tcp", ports).access).toBeUndefined();
	});
	test("udp has no access list to validate", () => {
		const form = toRouteForm(tcp);
		form.cidrs = [];
		form.openToInternet = false;
		expect(validateRouteForm(form, "udp").access).toBeUndefined();
	});
	test("tcp: deny rows are validated and a rule set that denies everything is refused", () => {
		const form = toRouteForm(tcp);
		form.denyCidrs = ["10.1.0.0/16", "nope"];
		const errors = validateRouteForm(form, "tcp");
		expect(errors["denyCidrs.1"]).toBeDefined();
		expect(errors["denyCidrs.0"]).toBeUndefined();
		form.denyCidrs = ["10.0.0.0/7"];
		expect(validateRouteForm(form, "tcp").accessRules).toContain(
			"deny every address",
		);
		form.denyCidrs = Array.from({ length: 201 }, (_, i) => `172.${i}.0.0/16`);
		expect(validateRouteForm(form, "tcp").denyCidrs).toBeDefined();
	});
});
