import { describe, expect, test } from "bun:test";
import {
	buildDockerOptions,
	type DockerForm,
	EMPTY_DOCKER_FORM,
	toDockerForm,
	toDockerPolicy,
	validateDockerForm,
} from "./docker-options";

const form = (patch: Partial<DockerForm>): DockerForm => ({
	...EMPTY_DOCKER_FORM,
	...patch,
});

const messages = (f: DockerForm) =>
	validateDockerForm(f).map((issue) => issue.message);

describe("toDockerPolicy", () => {
	test("copies every list of the catalog", () => {
		expect(
			toDockerPolicy({
				allowedSysctls: ["net.core.somaxconn"],
				gpuCapabilities: ["compute"],
				hostSysctlKeys: ["vm.max_map_count"],
				knownCapabilities: ["CHOWN", "NET_ADMIN"],
				reservedLabels: ["traefik."],
				safeCapabilities: ["CHOWN"],
				stopSignals: ["SIGTERM"],
				unrestrictedCapabilities: ["NET_ADMIN"],
				unrestrictedFields: ["privileged"],
				warnCapabilities: ["SYS_PTRACE"],
			}),
		).toEqual({
			allowedSysctls: ["net.core.somaxconn"],
			gpuCapabilities: ["compute"],
			hostSysctlKeys: ["vm.max_map_count"],
			knownCapabilities: ["CHOWN", "NET_ADMIN"],
			reservedLabels: ["traefik."],
			safeCapabilities: ["CHOWN"],
			stopSignals: ["SIGTERM"],
			unrestrictedCapabilities: ["NET_ADMIN"],
			warnCapabilities: ["SYS_PTRACE"],
		});
	});

	test("a null list reads as empty", () => {
		const policy = toDockerPolicy({
			allowedSysctls: null,
			gpuCapabilities: null,
			hostSysctlKeys: null,
			knownCapabilities: null,
			reservedLabels: null,
			safeCapabilities: null,
			stopSignals: null,
			unrestrictedCapabilities: null,
			unrestrictedFields: null,
			warnCapabilities: null,
		});
		expect(Object.values(policy).every((list) => list.length === 0)).toBe(true);
		expect(Object.keys(policy)).toHaveLength(9);
	});
});

describe("extra hosts", () => {
	test("an entry without an address becomes a name-only row; the address keeps later colons", () => {
		const parsed = toDockerForm({
			extraHosts: ["db.internal", "v6.internal:fe80::1"],
		});
		expect(parsed.extraHosts).toEqual([
			{ name: "db.internal", address: "" },
			{ name: "v6.internal", address: "fe80::1" },
		]);
	});
});

describe("DNS servers with an embedded IPv4 address", () => {
	const dns = (...values: string[]) =>
		validateDockerForm(form({ dns: values })).filter(
			(issue) => issue.field === "dns",
		);

	test("an IPv4-mapped multicast address is not a usable resolver", () => {
		expect(dns("::ffff:224.0.0.1")).toEqual([
			{
				field: "dns",
				row: 0,
				message: "::ffff:224.0.0.1 is not a usable resolver address.",
			},
		]);
	});

	test("an IPv4-mapped unicast address is accepted", () => {
		expect(dns("::ffff:8.8.8.8")).toEqual([]);
	});

	test("the same address written two ways is listed twice", () => {
		expect(dns("::ffff:8.8.8.8", "0:0:0:0:0:ffff:808:808")).toEqual([
			{
				field: "dns",
				row: 1,
				message: "0:0:0:0:0:ffff:808:808 is listed twice.",
			},
		]);
	});

	test("the IPv6 unspecified and multicast addresses are refused", () => {
		expect(dns("::", "ff02::1").map((issue) => issue.message)).toEqual([
			":: is not a usable resolver address.",
			"ff02::1 is not a usable resolver address.",
		]);
	});
});

describe("ulimit count", () => {
	test("more rows than there are resources is refused as a whole", () => {
		const all = messages(
			form({
				ulimits: [
					{ name: "nofile", soft: "1", hard: "2" },
					{ name: "memlock", soft: "1", hard: "2" },
					{ name: "nofile", soft: "3", hard: "4" },
				],
			}),
		);
		expect(all).toContain("At most 2 ulimits.");
		expect(all).toContain("nofile is listed twice.");
	});

	test("blank rows do not count towards the maximum", () => {
		expect(
			messages(
				form({
					ulimits: [
						{ name: "nofile", soft: "1", hard: "2" },
						{ name: "memlock", soft: "1", hard: "2" },
						{ name: "nofile", soft: "", hard: "" },
					],
				}),
			),
		).toEqual([]);
	});
});

describe("entrypoint limits", () => {
	test("an element over 4096 characters is refused", () => {
		expect(
			messages(form({ entrypoint: `/bin/run ${"a".repeat(4097)}` })),
		).toEqual(["An element is longer than 4096 characters."]);
	});

	test("an element of exactly 4096 characters is accepted", () => {
		expect(messages(form({ entrypoint: "a".repeat(4096) }))).toEqual([]);
	});

	test("a NUL character is refused", () => {
		expect(messages(form({ entrypoint: "/bin/run\0--flag" }))).toEqual([
			"The entrypoint cannot contain a NUL character.",
		]);
	});

	test("an entrypoint that starts with an empty element names the program", () => {
		expect(messages(form({ entrypoint: '"" arg' }))).toEqual([
			"The first element must be the program to run.",
		]);
	});

	test("more than 32 elements are refused", () => {
		const many = Array.from({ length: 33 }, (_, i) => `a${i}`).join(" ");
		expect(messages(form({ entrypoint: many }))).toEqual([
			"At most 32 elements.",
		]);
	});

	test("a valid entrypoint is sent as an argv", () => {
		expect(
			buildDockerOptions(form({ entrypoint: '/app/entry.sh "two words"' }))
				?.entrypoint,
		).toEqual(["/app/entry.sh", "two words"]);
	});
});
