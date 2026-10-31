import { describe, expect, test } from "bun:test";
import { ApiError } from "#/api/client-config";
import {
	applyHardened,
	buildDockerOptions,
	capabilityWarnings,
	cleanPath,
	clearHostAccess,
	type DockerForm,
	type DockerPolicy,
	dockerError,
	dockerErrorMessage,
	EMPTY_DOCKER_FORM,
	FALLBACK_POLICY,
	isHardened,
	isIPAddress,
	normalizeCapability,
	parseLimit,
	readOnlyRootfsFileProblems,
	readOnlyRootfsWarning,
	requiredUnrestrictedFields,
	toDockerForm,
	ULIMIT_NAMES,
	unrestrictedFieldLabel,
	unrestrictedFields,
	validateDockerForm,
} from "./docker-options";

const policy: DockerPolicy = {
	...FALLBACK_POLICY,
	safeCapabilities: ["CHOWN", "NET_BIND_SERVICE", "SYS_PTRACE"],
};
const catalogPolicy: DockerPolicy = {
	...policy,
	knownCapabilities: ["CHOWN", "NET_ADMIN", "NET_RAW", "SYS_ADMIN"],
	allowedSysctls: ["net.core.somaxconn", "net.ipv4.ip_forward"],
	hostSysctlKeys: ["vm.max_map_count", "fs.file-max"],
};

const form = (patch: Partial<DockerForm>): DockerForm => ({
	...EMPTY_DOCKER_FORM,
	...patch,
});

const messages = (f: DockerForm, ctx = {}) =>
	validateDockerForm(f, ctx).map((issue) => issue.message);

describe("normalizeCapability", () => {
	test("upper-cases and drops the CAP_ prefix", () => {
		expect(normalizeCapability(" cap_net_raw ")).toBe("NET_RAW");
		expect(normalizeCapability("chown")).toBe("CHOWN");
		expect(normalizeCapability("CAP_CAP_X")).toBe("CAP_X");
	});
});

describe("parseLimit", () => {
	test("-1 and the word unlimited mean unlimited", () => {
		expect(parseLimit("-1")).toBe(-1);
		expect(parseLimit(" Unlimited ")).toBe(-1);
		expect(parseLimit("65536")).toBe(65536);
		expect(parseLimit("0")).toBe(0);
	});
	test("anything else is not a number", () => {
		expect(parseLimit("")).toBeNaN();
		expect(parseLimit("1.5")).toBeNaN();
		expect(parseLimit("lots")).toBeNaN();
	});
});

describe("toDockerForm / buildDockerOptions", () => {
	test("nothing set means no docker key", () => {
		expect(toDockerForm(undefined)).toEqual(EMPTY_DOCKER_FORM);
		expect(buildDockerOptions(EMPTY_DOCKER_FORM)).toBeUndefined();
		expect(buildDockerOptions(toDockerForm({}))).toBeUndefined();
		expect(buildDockerOptions(toDockerForm({ init: false }))).toBeUndefined();
	});

	test("a full set of options survives a round trip", () => {
		const options = {
			capAdd: ["CHOWN", "SYS_PTRACE"],
			ulimits: [
				{ name: "memlock" as const, soft: -1, hard: -1 },
				{ name: "nofile" as const, soft: 1024, hard: 65536 },
			],
			shmSizeMb: 256,
			extraHosts: ["db.internal:10.0.0.5", "host.docker.internal:host-gateway"],
			labels: { "com.example.team": "payments", env: "prod" },
			init: true,
			stopSignal: "SIGINT",
			tmpfs: [
				{ path: "/cache", sizeMb: 128, mode: "1777" },
				{ path: "/tmp/work" },
			],
			entrypoint: ["/app/entry.sh", "--name", "my app"],
		};
		const formed = toDockerForm(options);
		expect(formed.ulimits[0]).toEqual({
			name: "memlock",
			soft: "unlimited",
			hard: "unlimited",
		});
		expect(formed.extraHosts[1]).toEqual({
			name: "host.docker.internal",
			address: "host-gateway",
		});
		expect(formed.tmpfs[1]).toEqual({
			path: "/tmp/work",
			sizeMb: "",
			mode: "",
		});
		expect(buildDockerOptions(formed)).toEqual(options);
	});

	test("empty rows are dropped", () => {
		const built = buildDockerOptions(
			form({
				ulimits: [{ name: "nofile", soft: "", hard: " " }],
				extraHosts: [{ name: "", address: "" }],
				labels: [{ key: " ", value: "" }],
				tmpfs: [{ path: "", sizeMb: "", mode: "" }],
				shmSizeMb: " ",
			}),
		);
		expect(built).toBeUndefined();
	});

	test("capabilities are normalized, de-duplicated and sorted", () => {
		expect(
			buildDockerOptions(form({ capAdd: ["cap_chown", "NET_RAW", "chown"] }))
				?.capAdd,
		).toEqual(["CHOWN", "NET_RAW"]);
	});

	test("unlimited words become -1", () => {
		expect(
			buildDockerOptions(
				form({
					ulimits: [{ name: "nofile", soft: "unlimited", hard: "-1" }],
				}),
			)?.ulimits,
		).toEqual([{ name: "nofile", soft: -1, hard: -1 }]);
	});

	test("row order does not change the result", () => {
		const a = form({
			labels: [
				{ key: "b", value: "1" },
				{ key: "a", value: "2" },
			],
			tmpfs: [
				{ path: "/z", sizeMb: "", mode: "" },
				{ path: "/a", sizeMb: "", mode: "" },
			],
			ulimits: [
				{ name: "memlock", soft: "1", hard: "2" },
				{ name: "nofile", soft: "1", hard: "2" },
			],
		});
		const b = form({
			labels: [...a.labels].reverse(),
			tmpfs: [...a.tmpfs].reverse(),
			ulimits: [...a.ulimits].reverse(),
		});
		expect(JSON.stringify(buildDockerOptions(a))).toBe(
			JSON.stringify(buildDockerOptions(b)),
		);
		expect(Object.keys(buildDockerOptions(a)?.labels ?? {})).toEqual([
			"a",
			"b",
		]);
	});

	test("an unparsable number still differs from a clean form", () => {
		const bad = buildDockerOptions(
			form({ ulimits: [{ name: "nofile", soft: "x", hard: "10" }] }),
		);
		expect(JSON.stringify(bad)).not.toBe(
			JSON.stringify(
				buildDockerOptions(
					form({ ulimits: [{ name: "nofile", soft: "1", hard: "10" }] }),
				),
			),
		);
	});

	test("an extra host keeps an IPv6 address whole", () => {
		const formed = toDockerForm({ extraHosts: ["app:::1"] });
		expect(formed.extraHosts[0]).toEqual({ name: "app", address: "::1" });
		expect(buildDockerOptions(formed)?.extraHosts).toEqual(["app:::1"]);
	});
});

describe("isIPAddress", () => {
	test("IPv4", () => {
		expect(isIPAddress("10.0.0.5")).toBe(true);
		expect(isIPAddress("255.255.255.255")).toBe(true);
		expect(isIPAddress("256.0.0.1")).toBe(false);
		expect(isIPAddress("10.0.0")).toBe(false);
		expect(isIPAddress("010.0.0.1")).toBe(false);
		expect(isIPAddress("host-gateway")).toBe(false);
	});
	test("IPv6", () => {
		expect(isIPAddress("::1")).toBe(true);
		expect(isIPAddress("::")).toBe(true);
		expect(isIPAddress("fe80::1")).toBe(true);
		expect(isIPAddress("2001:db8:0:0:0:0:0:1")).toBe(true);
		expect(isIPAddress("1:2:3:4:5:6:7::")).toBe(true);
		expect(isIPAddress("::ffff:10.0.0.1")).toBe(true);
		expect(isIPAddress("fe80::1%eth0")).toBe(true);
		expect(isIPAddress("1:2:3:4:5:6:7:8:9")).toBe(false);
		expect(isIPAddress("1::2::3")).toBe(false);
		expect(isIPAddress("12345::1")).toBe(false);
		expect(isIPAddress("1:2:3:4:5:6:7")).toBe(false);
		expect(isIPAddress("::1.2.3.4::")).toBe(false);
		expect(isIPAddress("g::1")).toBe(false);
		expect(isIPAddress(":1")).toBe(false);
	});
});

describe("cleanPath", () => {
	test("matches path.Clean", () => {
		expect(cleanPath("/a//b/./c/")).toBe("/a/b/c");
		expect(cleanPath("/a/../b")).toBe("/b");
		expect(cleanPath("/..")).toBe("/");
		expect(cleanPath("a/..")).toBe(".");
	});
});

describe("validateDockerForm", () => {
	test("an empty form is fine", () => {
		expect(validateDockerForm(EMPTY_DOCKER_FORM)).toEqual([]);
		expect(dockerError(EMPTY_DOCKER_FORM)).toBeUndefined();
	});

	test("capabilities must be on the safe list", () => {
		expect(messages(form({ capAdd: ["CHOWN"] }), { policy })).toEqual([]);
		expect(messages(form({ capAdd: ["cap_chown"] }), { policy })).toEqual([]);
		expect(messages(form({ capAdd: ["MAC_ADMIN"] }), { policy })).toEqual([
			"MAC_ADMIN is not an allowed capability.",
		]);
		expect(
			messages(form({ capAdd: ["CHOWN", "CAP_CHOWN"] }), { policy }),
		).toEqual(["CHOWN is listed twice."]);
	});

	test("capabilities that need an unrestricted server are well-formed", () => {
		expect(
			messages(form({ capAdd: ["NET_ADMIN", "sys_admin"] }), { policy }),
		).toEqual([]);
		expect(
			unrestrictedFields(form({ capAdd: ["NET_ADMIN", "CHOWN"] })),
		).toEqual(["capAdd NET_ADMIN"]);
	});

	test("without a policy only the control plane can judge capabilities", () => {
		expect(messages(form({ capAdd: ["WHATEVER"] }))).toEqual([]);
	});

	test("ulimits: numbers, order and duplicates", () => {
		const row = (name: "nofile" | "memlock", soft: string, hard: string) => ({
			name,
			soft,
			hard,
		});
		expect(
			messages(form({ ulimits: [row("nofile", "1024", "4096")] })),
		).toEqual([]);
		expect(
			messages(form({ ulimits: [row("nofile", "unlimited", "unlimited")] })),
		).toEqual([]);
		// Soft above hard.
		expect(
			messages(form({ ulimits: [row("nofile", "5000", "4096")] })),
		).toEqual(["Soft must not exceed hard."]);
		// Hard -1 allows any soft.
		expect(messages(form({ ulimits: [row("nofile", "5000", "-1")] }))).toEqual(
			[],
		);
		// A finite hard with unlimited soft is a contradiction.
		expect(
			messages(form({ ulimits: [row("nofile", "unlimited", "4096")] })),
		).toEqual(["Soft must not exceed hard."]);
		// Half filled.
		expect(messages(form({ ulimits: [row("nofile", "", "4096")] }))).toEqual([
			"Soft and hard need a number, or -1 or unlimited for no limit.",
		]);
		// Out of range.
		expect(
			messages(form({ ulimits: [row("nofile", "1", "9999999999")] })),
		).toEqual(["Soft and hard must be -1 (unlimited) or 0 to 2147483647."]);
		expect(messages(form({ ulimits: [row("nofile", "-2", "5")] }))).toEqual([
			"Soft and hard must be -1 (unlimited) or 0 to 2147483647.",
		]);
		// The same name twice points at the second row.
		const twice = validateDockerForm(
			form({
				ulimits: [row("nofile", "1", "2"), row("nofile", "1", "2")],
			}),
		);
		expect(twice).toEqual([
			{ field: "ulimits", row: 1, message: "nofile is listed twice." },
		]);
		// A blank row is skipped, and its index is kept for the rest.
		expect(
			validateDockerForm(
				form({
					ulimits: [row("nofile", "", ""), row("memlock", "5", "1")],
				}),
			)[0]?.row,
		).toBe(1);
	});

	test("shared memory is 1 to 65536 MiB", () => {
		expect(messages(form({ shmSizeMb: "256" }))).toEqual([]);
		expect(messages(form({ shmSizeMb: "65536" }))).toEqual([]);
		expect(messages(form({ shmSizeMb: "65537" }))).toHaveLength(1);
		expect(messages(form({ shmSizeMb: "0" }))).toHaveLength(1);
		expect(messages(form({ shmSizeMb: "1g" }))).toHaveLength(1);
	});

	test("extra hosts: name and IPv4, IPv6 or host-gateway", () => {
		const host = (name: string, address: string) =>
			form({ extraHosts: [{ name, address }] });
		expect(messages(host("db", "10.0.0.5"))).toEqual([]);
		expect(messages(host("db", "::1"))).toEqual([]);
		expect(messages(host("host.docker.internal", "host-gateway"))).toEqual([]);
		expect(messages(host("db", "nope"))).toEqual([
			"The address must be an IPv4 or IPv6 address, or host-gateway.",
		]);
		expect(messages(host("db", ""))).toHaveLength(1);
		expect(messages(host("-db", "10.0.0.5"))).toHaveLength(1);
		expect(messages(host("d b", "10.0.0.5"))).toHaveLength(1);
		expect(messages(host("", "10.0.0.5"))).toHaveLength(1);
	});

	test("at most 20 extra hosts", () => {
		const rows = Array.from({ length: 21 }, (_, i) => ({
			name: `h${i}`,
			address: "10.0.0.1",
		}));
		expect(messages(form({ extraHosts: rows }))).toEqual([
			"At most 20 extra hosts.",
		]);
		expect(messages(form({ extraHosts: rows.slice(0, 20) }))).toEqual([]);
	});

	test("labels: key pattern, value, reserved prefixes, duplicates, count", () => {
		const label = (key: string, value = "v") =>
			form({ labels: [{ key, value }] });
		expect(messages(label("com.example.team"))).toEqual([]);
		expect(messages(label("a/b-c_d.e"))).toEqual([]);
		expect(messages(label("_bad"))).toHaveLength(1);
		expect(messages(label("has space"))).toHaveLength(1);
		expect(messages(label("", "value only"))).toHaveLength(1);
		expect(messages(label("k".repeat(129)))).toHaveLength(1);
		expect(messages(label("k".repeat(128)))).toEqual([]);
		expect(messages(label("k", "x".repeat(512)))).toEqual([]);
		expect(messages(label("k", "x".repeat(513)))).toEqual([
			"The value is at most 512 characters on a single line.",
		]);
		expect(messages(label("k", "a\nb"))).toHaveLength(1);
		expect(messages(label("Traefik.enable"))).toEqual([
			"traefik.* labels are managed by NebulaCtrl and cannot be set.",
		]);
		expect(messages(label("COM.DOCKER.x"))).toEqual([
			"com.docker.* labels are managed by NebulaCtrl and cannot be set.",
		]);
		expect(messages(label("nebulactrl.service"))).toHaveLength(1);
		// The prefixes come from the policy.
		const custom = { ...policy, reservedLabels: ["corp."] };
		expect(messages(label("corp.x"), { policy: custom })).toHaveLength(1);
		expect(messages(label("traefik.x"), { policy: custom })).toEqual([]);
		expect(
			messages(
				form({
					labels: [
						{ key: "a", value: "1" },
						{ key: "a", value: "2" },
					],
				}),
			),
		).toEqual(["a is listed twice."]);
		const many = Array.from({ length: 31 }, (_, i) => ({
			key: `k${i}`,
			value: "v",
		}));
		expect(messages(form({ labels: many }))).toEqual(["At most 30 labels."]);
	});

	test("stop signal must be on the policy list", () => {
		expect(messages(form({ stopSignal: "SIGINT" }))).toEqual([]);
		expect(messages(form({ stopSignal: "sigterm" }))).toEqual([]);
		expect(messages(form({ stopSignal: "SIGSTOP" }))).toHaveLength(1);
		expect(
			messages(form({ stopSignal: "SIGINT" }), {
				policy: { ...policy, stopSignals: ["SIGTERM"] },
			}),
		).toHaveLength(1);
	});

	test("entrypoint is an exec-form argv", () => {
		expect(messages(form({ entrypoint: "" }))).toEqual([]);
		expect(messages(form({ entrypoint: '/app/entry.sh --x "a b"' }))).toEqual(
			[],
		);
		expect(
			buildDockerOptions(form({ entrypoint: '/app/entry.sh --x "a b"' })),
		).toEqual({ entrypoint: ["/app/entry.sh", "--x", "a b"] });
		expect(messages(form({ entrypoint: '"" x' }))).toEqual([
			"The first element must be the program to run.",
		]);
		expect(
			messages(form({ entrypoint: Array(33).fill("a").join(" ") })),
		).toEqual(["At most 32 elements."]);
		expect(buildDockerOptions(form({ entrypoint: "   " }))).toBeUndefined();
	});

	test("tmpfs paths", () => {
		const mount = (path: string, sizeMb = "", mode = "") =>
			form({ tmpfs: [{ path, sizeMb, mode }] });
		expect(messages(mount("/cache"))).toEqual([]);
		expect(messages(mount("/var/cache/app", "128", "1777"))).toEqual([]);
		expect(messages(mount("cache"))).toHaveLength(1);
		expect(messages(mount("/"))).toHaveLength(1);
		expect(messages(mount("/a/"))).toHaveLength(1);
		expect(messages(mount("/a/../b"))).toHaveLength(1);
		expect(messages(mount("/a b"))).toHaveLength(1);
		expect(messages(mount("/a,b"))).toHaveLength(1);
		expect(messages(mount("/a:b"))).toHaveLength(1);
		for (const reserved of [
			"/proc",
			"/proc/self",
			"/sys",
			"/dev",
			"/dev/shm",
			"/run/docker",
			"/run/docker/x",
		]) {
			expect(messages(mount(reserved))).toEqual([
				`${reserved} is reserved by the system.`,
			]);
		}
		expect(messages(mount("/device"))).toEqual([]);
		expect(messages(mount("/run"))).toEqual([]);
	});

	test("tmpfs size, mode, duplicates, count and file collisions", () => {
		const mount = (path: string, sizeMb = "", mode = "") => ({
			path,
			sizeMb,
			mode,
		});
		expect(messages(form({ tmpfs: [mount("/a", "0")] }))).toHaveLength(1);
		expect(messages(form({ tmpfs: [mount("/a", "65537")] }))).toHaveLength(1);
		expect(messages(form({ tmpfs: [mount("/a", "x")] }))).toHaveLength(1);
		expect(messages(form({ tmpfs: [mount("/a", "", "755")] }))).toEqual([]);
		expect(messages(form({ tmpfs: [mount("/a", "", "1777")] }))).toEqual([]);
		for (const mode of ["75", "17777", "888", "rwx"]) {
			expect(messages(form({ tmpfs: [mount("/a", "", mode)] }))).toEqual([
				"The mode is octal with three or four digits, like 1777.",
			]);
		}
		expect(
			validateDockerForm(form({ tmpfs: [mount("/a"), mount("/a")] })),
		).toEqual([{ field: "tmpfs", row: 1, message: "/a is listed twice." }]);
		expect(
			messages(form({ tmpfs: [mount("/etc/app.conf")] }), {
				filePaths: ["/etc//app.conf"],
			}),
		).toEqual(["/etc/app.conf is also a file mount."]);
		const many = Array.from({ length: 11 }, (_, i) => mount(`/m${i}`));
		expect(messages(form({ tmpfs: many }))).toEqual([
			"At most 10 tmpfs mounts.",
		]);
	});

	test("dockerError names the first problem", () => {
		expect(
			dockerError(form({ shmSizeMb: "x", stopSignal: "SIGSTOP" })),
		).toStartWith("Docker options: Shared memory");
	});
});

describe("capabilityWarnings", () => {
	test("SYS_PTRACE is warned about", () => {
		const out = capabilityWarnings({ capAdd: ["CHOWN", "cap_sys_ptrace"] });
		expect(out).toHaveLength(1);
		expect(out[0]).toContain("SYS_PTRACE");
	});
	test("capabilities that need an unrestricted server are warned about", () => {
		const out = capabilityWarnings({ capAdd: ["NET_ADMIN", "sys_admin"] });
		expect(out).toHaveLength(2);
		expect(out[0]).toContain("NET_ADMIN widens the container's power");
		expect(out[0]).toContain("unrestricted server");
		expect(out[1]).toContain("SYS_ADMIN widens the container's power");
	});
	test("plain safe capabilities have no warning", () => {
		expect(capabilityWarnings({ capAdd: ["CHOWN"] })).toEqual([]);
		expect(capabilityWarnings({ capAdd: [] })).toEqual([]);
	});
	test("the policy decides which capabilities are warned about", () => {
		const out = capabilityWarnings(
			{ capAdd: ["NET_RAW"] },
			{ ...policy, warnCapabilities: ["NET_RAW"] },
		);
		expect(out).toHaveLength(1);
		expect(out[0]).toContain("NET_RAW");
	});
});

describe("dockerErrorMessage", () => {
	test("surfaces the validation text of a docker problem", () => {
		const error = new ApiError(
			422,
			"invalid_config",
			"docker: ulimit nofile: soft must not exceed hard",
		);
		expect(dockerErrorMessage(error)).toBe(
			"docker: ulimit nofile: soft must not exceed hard",
		);
	});
	test("puts a requires_unrestricted_server refusal in words", () => {
		const error = new ApiError(
			422,
			"requires_unrestricted_server",
			"devices, capAdd NET_ADMIN need an unrestricted server",
			{ fields: ["devices", "capAdd NET_ADMIN", "gpus"] },
		);
		expect(requiredUnrestrictedFields(error)).toEqual([
			"devices",
			"capAdd NET_ADMIN",
			"gpus",
		]);
		const message = dockerErrorMessage(error);
		expect(message).toContain("Unrestricted");
		expect(message).toContain(
			"host devices, the NET_ADMIN capability and GPUs",
		);
		expect(message).not.toContain("capAdd");
		// Without details it still says something readable.
		expect(
			dockerErrorMessage(
				new ApiError(422, "requires_unrestricted_server", "x"),
			),
		).toContain("Unrestricted");
	});
	test("leaves other errors to the general display", () => {
		expect(
			dockerErrorMessage(
				new ApiError(422, "invalid_config", "port must be 1-65535"),
			),
		).toBeUndefined();
		expect(
			dockerErrorMessage(new ApiError(500, "internal", "boom")),
		).toBeUndefined();
		expect(dockerErrorMessage(new Error("docker: no"))).toBeUndefined();
	});
});

describe("the typed options: mapping", () => {
	const options = {
		capAdd: ["CHOWN", "NET_ADMIN"],
		capDrop: ["ALL", "NET_RAW"],
		noNewPrivileges: true,
		readOnlyRootfs: true,
		pidsLimit: 512,
		hostname: "web",
		dns: ["1.1.1.1", "2606:4700:4700::1111"],
		sysctls: {
			"net.core.somaxconn": "1024",
			"net.ipv4.ip_local_port_range": "1024 65000",
		},
		hostSysctls: { "fs.file-max": 1048576, "vm.max_map_count": 262144 },
		user: "1000:1000",
		groupAdd: ["44", "109"],
		tmpfs: [{ path: "/tmp", sizeMb: 64 }],
		devices: [
			{ hostPath: "/dev/dri/renderD128" },
			{ hostPath: "/dev/fuse", containerPath: "/dev/fuse2", permissions: "rw" },
		],
		gpus: {
			count: 2,
			driver: "nvidia" as const,
			capabilities: ["compute", "video"],
		},
		runtime: "runsc",
		privileged: true,
		pidHost: true,
		seccompUnconfined: true,
		apparmorUnconfined: true,
	};

	test("every field survives a round trip", () => {
		const formed = toDockerForm(options);
		expect(formed.pidsLimit).toBe("512");
		expect(formed.hostSysctls[1]).toEqual({
			key: "vm.max_map_count",
			value: "262144",
		});
		expect(formed.gpus).toEqual({
			mode: "count",
			count: "2",
			capabilities: ["compute", "video"],
		});
		expect(formed.devices[0]).toEqual({
			hostPath: "/dev/dri/renderD128",
			containerPath: "",
			permissions: "",
		});
		expect(buildDockerOptions(formed)).toEqual(options);
	});

	test("a form with nothing in it produces no docker key", () => {
		expect(
			buildDockerOptions(
				form({
					capDrop: [" "],
					pidsLimit: "0",
					hostname: "  ",
					dns: ["", " "],
					sysctls: [{ key: "", value: " " }],
					hostSysctls: [{ key: " ", value: "" }],
					user: " ",
					groupAdd: [""],
					devices: [{ hostPath: "", containerPath: " ", permissions: "" }],
					gpus: { mode: "none", count: "4", capabilities: ["video"] },
					runtime: " ",
				}),
			),
		).toBeUndefined();
		expect(buildDockerOptions(toDockerForm({ pidsLimit: 0 }))).toBeUndefined();
		expect(
			buildDockerOptions(toDockerForm({ privileged: false, dns: null })),
		).toBeUndefined();
	});

	test("values are normalized like the control plane does", () => {
		const built = buildDockerOptions(
			form({
				capDrop: ["cap_net_raw", "all", "NET_RAW"],
				hostname: " Web ",
				user: " 1000 ",
				runtime: " runsc ",
				pidsLimit: " 64 ",
				gpus: {
					mode: "all",
					count: "",
					capabilities: ["Video", "compute", "video"],
				},
				dns: [" 1.1.1.1 "],
				groupAdd: [" 44 "],
				sysctls: [{ key: " net.core.somaxconn ", value: " 128 " }],
			}),
		);
		expect(built).toEqual({
			capDrop: ["ALL", "NET_RAW"],
			hostname: "web",
			user: "1000",
			runtime: "runsc",
			pidsLimit: 64,
			gpus: { count: -1, driver: "nvidia", capabilities: ["compute", "video"] },
			dns: ["1.1.1.1"],
			groupAdd: ["44"],
			sysctls: { "net.core.somaxconn": "128" },
		});
	});

	test("the GPU mode round-trips all and a count", () => {
		expect(
			toDockerForm({ gpus: { count: -1, driver: "nvidia" } }).gpus,
		).toEqual({
			mode: "all",
			count: "",
			capabilities: [],
		});
		expect(
			buildDockerOptions(
				form({ gpus: { mode: "all", count: "", capabilities: [] } }),
			),
		).toEqual({
			gpus: { count: -1, driver: "nvidia" },
		});
		expect(
			buildDockerOptions(
				form({ gpus: { mode: "count", count: "3", capabilities: [] } }),
			)?.gpus?.count,
		).toBe(3);
	});

	test("map keys and list rows come out in a fixed order", () => {
		const a = form({
			sysctls: [
				{ key: "net.ipv4.ip_forward", value: "1" },
				{ key: "net.core.somaxconn", value: "9" },
			],
			hostSysctls: [
				{ key: "vm.max_map_count", value: "5" },
				{ key: "fs.file-max", value: "6" },
			],
		});
		const b = form({
			sysctls: [...a.sysctls].reverse(),
			hostSysctls: [...a.hostSysctls].reverse(),
		});
		expect(JSON.stringify(buildDockerOptions(a))).toBe(
			JSON.stringify(buildDockerOptions(b)),
		);
		expect(Object.keys(buildDockerOptions(a)?.sysctls ?? {})).toEqual([
			"net.core.somaxconn",
			"net.ipv4.ip_forward",
		]);
	});

	test("an unparsable number still differs from a clean form", () => {
		const bad = buildDockerOptions(form({ pidsLimit: "x" }));
		expect(JSON.stringify(bad)).not.toBe(
			JSON.stringify(buildDockerOptions(form({}))),
		);
		const gpu = buildDockerOptions(
			form({ gpus: { mode: "count", count: "", capabilities: [] } }),
		);
		expect(gpu).toBeDefined();
	});

	test("ulimits no longer offer the per-user process limit", () => {
		expect(ULIMIT_NAMES).toEqual(["nofile", "memlock"]);
	});
});

describe("the typed options: validation", () => {
	const withPolicy = { policy: catalogPolicy };

	test("capDrop: ALL or a Linux capability, no duplicates, not also added", () => {
		expect(messages(form({ capDrop: ["ALL"] }), withPolicy)).toEqual([]);
		expect(
			messages(form({ capDrop: ["cap_net_raw", "ALL"] }), withPolicy),
		).toEqual([]);
		expect(messages(form({ capDrop: ["bogus"] }), withPolicy)).toEqual([
			"bogus is not a Linux capability; use ALL or a name such as NET_RAW.",
		]);
		expect(
			messages(form({ capDrop: ["NET_RAW", "cap_net_raw"] }), withPolicy),
		).toEqual(["NET_RAW is listed twice."]);
		expect(
			messages(form({ capAdd: ["CHOWN"], capDrop: ["CHOWN"] }), withPolicy),
		).toEqual(["CHOWN is in both the added and the dropped capabilities."]);
		// Without the catalog only the control plane can tell the names.
		expect(messages(form({ capDrop: ["bogus"] }))).toEqual([]);
	});

	test("pidsLimit: 0 or 1 to 4194304", () => {
		for (const ok of ["", "0", "1", "512", "4194304"]) {
			expect(messages(form({ pidsLimit: ok }))).toEqual([]);
		}
		for (const bad of ["4194305", "-1", "x", "1.5"]) {
			expect(messages(form({ pidsLimit: bad }))).toEqual([
				"The limit is 0 (none) or 1 to 4194304 processes and threads.",
			]);
		}
	});

	test("hostname: one RFC 1123 label, empty means the slug", () => {
		for (const ok of ["", "web", "Web", "a-b-1", "a".repeat(63), "9lives"]) {
			expect(messages(form({ hostname: ok }))).toEqual([]);
		}
		for (const bad of ["a".repeat(64), "-a", "a-", "a_b", "a.b", "a b"]) {
			expect(messages(form({ hostname: bad }))).toHaveLength(1);
		}
	});

	test("dns: IP addresses only, usable, unique, at most three", () => {
		expect(
			messages(form({ dns: ["1.1.1.1", "2606:4700:4700::1111"] })),
		).toEqual([]);
		expect(messages(form({ dns: ["", "1.1.1.1"] }))).toEqual([]);
		expect(validateDockerForm(form({ dns: ["dns.example.com"] }))).toEqual([
			{
				field: "dns",
				row: 0,
				message:
					"dns.example.com must be an IP address (hostnames are not accepted).",
			},
		]);
		expect(messages(form({ dns: ["fe80::1%eth0"] }))).toHaveLength(1);
		expect(messages(form({ dns: ["0.0.0.0"] }))).toEqual([
			"0.0.0.0 is not a usable resolver address.",
		]);
		expect(messages(form({ dns: ["::"] }))).toHaveLength(1);
		expect(messages(form({ dns: ["224.0.0.1"] }))).toHaveLength(1);
		expect(messages(form({ dns: ["ff02::1"] }))).toHaveLength(1);
		// The same address written two ways is one address.
		expect(
			validateDockerForm(form({ dns: ["::1", "0:0:0:0:0:0:0:1"] })),
		).toEqual([
			{ field: "dns", row: 1, message: "0:0:0:0:0:0:0:1 is listed twice." },
		]);
		expect(messages(form({ dns: ["1.1.1.1", "1.1.1.1"] }))).toEqual([
			"1.1.1.1 is listed twice.",
		]);
		expect(
			messages(form({ dns: ["1.1.1.1", "8.8.8.8", "9.9.9.9", "4.4.4.4"] })),
		).toEqual(["At most 3 DNS servers."]);
	});

	test("sysctls: curated keys, numeric values, unique, at most 16", () => {
		const sysctl = (key: string, value: string) =>
			form({ sysctls: [{ key, value }] });
		expect(messages(sysctl("net.core.somaxconn", "1024"), withPolicy)).toEqual(
			[],
		);
		expect(
			messages(sysctl("net.core.somaxconn", "1024 65000"), withPolicy),
		).toEqual([]);
		expect(messages(sysctl("net.core.nope", "1"), withPolicy)).toEqual([
			"net.core.nope is not an allowed sysctl.",
		]);
		expect(messages(sysctl("vm.max_map_count", "1"), withPolicy)[0]).toContain(
			"changes the host",
		);
		expect(messages(sysctl("vm.swappiness", "1"), withPolicy)[0]).toContain(
			"changes the host",
		);
		for (const bad of ["abc", "1  2", "-1", "1.5", "x".repeat(65)]) {
			expect(messages(sysctl("net.core.somaxconn", bad), withPolicy)).toEqual([
				"The value is a number, or numbers separated by single spaces, up to 64 characters.",
			]);
		}
		expect(messages(sysctl("", "1"), withPolicy)).toEqual([
			"Pick the sysctl to set.",
		]);
		expect(
			validateDockerForm(
				form({
					sysctls: [
						{ key: "net.core.somaxconn", value: "1" },
						{ key: "net.core.somaxconn", value: "2" },
					],
				}),
				withPolicy,
			),
		).toEqual([
			{
				field: "sysctls",
				row: 1,
				message: "net.core.somaxconn is listed twice.",
			},
		]);
		const many = Array.from({ length: 17 }, (_, i) => ({
			key: `k${i}`,
			value: "1",
		}));
		expect(messages(form({ sysctls: many }))).toEqual(["At most 16 sysctls."]);
		// Without the catalog only the control plane can tell the keys.
		expect(messages(sysctl("anything", "1"))).toEqual([]);
	});

	test("hostSysctls: supported keys, a whole minimum from 1 to 2^32, unique", () => {
		const need = (key: string, value: string) =>
			form({ hostSysctls: [{ key, value }] });
		expect(messages(need("vm.max_map_count", "262144"), withPolicy)).toEqual(
			[],
		);
		expect(
			messages(need("vm.max_map_count", "4294967296"), withPolicy),
		).toEqual([]);
		for (const bad of ["0", "4294967297", "x", "-5", "1.5", ""]) {
			expect(messages(need("vm.max_map_count", bad), withPolicy)).toEqual([
				"The minimum is a whole number from 1 to 4294967296.",
			]);
		}
		expect(messages(need("vm.swappiness", "1"), withPolicy)).toEqual([
			"vm.swappiness is not a supported host parameter.",
		]);
		expect(messages(need("", "5"), withPolicy)).toEqual([
			"Pick the kernel parameter.",
		]);
		expect(
			messages(
				form({
					hostSysctls: [
						{ key: "fs.file-max", value: "1" },
						{ key: "fs.file-max", value: "2" },
					],
				}),
				withPolicy,
			),
		).toEqual(["fs.file-max is listed twice."]);
		expect(messages(need("whatever", "5"))).toEqual([]);
	});

	test("user: uid[:gid] as numbers or names", () => {
		for (const ok of [
			"1000",
			"1000:1000",
			"app",
			"app:staff",
			"svc$",
			"_x.y-z",
			"2147483647:0",
			" 33 ",
		]) {
			expect(messages(form({ user: ok }))).toEqual([]);
		}
		for (const bad of [
			"1000:",
			":1000",
			"App",
			"2147483648",
			"1000:2147483648",
			"a:b:c",
			"99999999999",
			"1 2",
			"-1",
		]) {
			expect(messages(form({ user: bad }))).toHaveLength(1);
		}
	});

	test("groupAdd: numeric group IDs 1 to 65535, unique, at most 8", () => {
		expect(messages(form({ groupAdd: ["44", "109", ""] }))).toEqual([]);
		expect(messages(form({ groupAdd: ["65535"] }))).toEqual([]);
		for (const bad of ["0", "65536", "abc", "-1", "123456", "1.5"]) {
			expect(messages(form({ groupAdd: [bad] }))).toEqual([
				`${bad} is not a numeric group ID from 1 to 65535.`,
			]);
		}
		expect(validateDockerForm(form({ groupAdd: ["44", "44"] }))).toEqual([
			{ field: "groupAdd", row: 1, message: "44 is listed twice." },
		]);
		const nine = Array.from({ length: 9 }, (_, i) => String(100 + i));
		expect(messages(form({ groupAdd: nine }))).toEqual([
			"At most 8 supplementary groups.",
		]);
	});

	test("devices: nodes below /dev, rwm permissions, unique container paths, at most 16", () => {
		const device = (hostPath: string, containerPath = "", permissions = "") =>
			form({ devices: [{ hostPath, containerPath, permissions }] });
		expect(messages(device("/dev/dri/renderD128"))).toEqual([]);
		expect(messages(device("/dev/fuse", "/dev/fuse2", "rw"))).toEqual([]);
		expect(messages(device("/dev/net/tun", "", "rwm"))).toEqual([]);
		expect(messages(device("/etc/passwd"))[0]).toContain("below /dev");
		expect(messages(device("/dev/"))[0]).toContain("clean absolute path");
		expect(messages(device("/dev"))[0]).toContain("below /dev");
		expect(messages(device("dev/null"))[0]).toContain("clean absolute path");
		expect(messages(device("/dev/a/../b"))[0]).toContain("clean absolute path");
		expect(messages(device("/dev/a b"))[0]).toContain(
			"spaces, commas or colons",
		);
		expect(messages(device("/dev/a:b"))[0]).toContain(
			"spaces, commas or colons",
		);
		expect(messages(device("/dev/fuse", "/tmp/fuse"))[0]).toContain(
			"The container path",
		);
		for (const bad of ["rr", "x", "rwmr", "RW"]) {
			expect(messages(device("/dev/fuse", "", bad))).toEqual([
				"Permissions are a subset of rwm (read, write, mknod), like rw.",
			]);
		}
		expect(
			validateDockerForm(
				form({
					devices: [
						{ hostPath: "/dev/a", containerPath: "/dev/x", permissions: "" },
						{ hostPath: "/dev/b", containerPath: "/dev/x", permissions: "" },
					],
				}),
			),
		).toEqual([
			{ field: "devices", row: 1, message: "/dev/x is mapped twice." },
		]);
		// The same host node twice is fine while the container paths differ.
		expect(
			messages(
				form({
					devices: [
						{ hostPath: "/dev/a", containerPath: "", permissions: "" },
						{ hostPath: "/dev/a", containerPath: "/dev/b", permissions: "" },
					],
				}),
			),
		).toEqual([]);
		const many = Array.from({ length: 17 }, (_, i) => ({
			hostPath: `/dev/d${i}`,
			containerPath: "",
			permissions: "",
		}));
		expect(messages(form({ devices: many }))).toEqual(["At most 16 devices."]);
	});

	test("gpus: none, all or 1 to 16, supported unique capabilities", () => {
		const gpus = (
			mode: "none" | "all" | "count",
			count = "",
			capabilities: string[] = [],
		) => form({ gpus: { mode, count, capabilities } });
		expect(messages(gpus("none"))).toEqual([]);
		expect(messages(gpus("all", "", ["video", "compute"]))).toEqual([]);
		expect(messages(gpus("count", "1"))).toEqual([]);
		expect(messages(gpus("count", "16"))).toEqual([]);
		for (const bad of ["", "0", "17", "x", "-1"]) {
			expect(messages(gpus("count", bad))).toEqual([
				"The number of GPUs is 1 to 16.",
			]);
		}
		expect(messages(gpus("all", "", ["warp"]))).toEqual([
			"warp is not a supported GPU capability.",
		]);
		expect(messages(gpus("all", "", ["video", "VIDEO"]))).toEqual([
			"video is listed twice.",
		]);
		// What is switched off is not judged.
		expect(messages(gpus("none", "99", ["warp"]))).toEqual([]);
	});

	test("runtime: the name of an OCI runtime", () => {
		for (const ok of ["", "runsc", "sysbox-runc", "nvidia", "crun_2.0"]) {
			expect(messages(form({ runtime: ok }))).toEqual([]);
		}
		for (const bad of ["Runsc", "-x", "a b", "a/b", "x".repeat(64)]) {
			expect(messages(form({ runtime: bad }))).toHaveLength(1);
		}
	});
});

describe("host access", () => {
	test("unrestrictedFields names what needs the tier", () => {
		expect(unrestrictedFields(EMPTY_DOCKER_FORM)).toEqual([]);
		expect(
			unrestrictedFields(
				form({
					capAdd: ["CHOWN", "sys_admin", "NET_ADMIN"],
					devices: [
						{ hostPath: "/dev/fuse", containerPath: "", permissions: "" },
					],
					gpus: { mode: "all", count: "", capabilities: [] },
					runtime: "runsc",
					privileged: true,
					pidHost: true,
					seccompUnconfined: true,
					apparmorUnconfined: true,
				}),
			),
		).toEqual([
			"devices",
			"gpus",
			"runtime",
			"privileged",
			"pidHost",
			"seccompUnconfined",
			"apparmorUnconfined",
			"capAdd NET_ADMIN",
			"capAdd SYS_ADMIN",
		]);
		// The ungated options never need it.
		expect(
			unrestrictedFields(
				form({
					capAdd: ["CHOWN"],
					capDrop: ["ALL"],
					noNewPrivileges: true,
					readOnlyRootfs: true,
					dns: ["1.1.1.1"],
					devices: [{ hostPath: "", containerPath: "", permissions: "" }],
				}),
			),
		).toEqual([]);
	});

	test("the capabilities that need it come from the policy", () => {
		expect(
			unrestrictedFields(form({ capAdd: ["NET_ADMIN", "NET_RAW"] }), {
				...policy,
				unrestrictedCapabilities: ["NET_RAW"],
			}),
		).toEqual(["capAdd NET_RAW"]);
	});

	test("the fields are named in words", () => {
		expect(unrestrictedFieldLabel("capAdd SYS_ADMIN")).toBe(
			"the SYS_ADMIN capability",
		);
		expect(unrestrictedFieldLabel("pidHost")).toBe("the host PID namespace");
		expect(unrestrictedFieldLabel("something")).toBe("something");
	});

	test("clearHostAccess removes only what needs the tier", () => {
		const cleared = clearHostAccess(
			form({
				capAdd: ["CHOWN", "NET_ADMIN"],
				capDrop: ["ALL"],
				hostname: "web",
				devices: [
					{ hostPath: "/dev/fuse", containerPath: "", permissions: "" },
				],
				gpus: { mode: "count", count: "2", capabilities: ["video"] },
				runtime: "runsc",
				privileged: true,
				pidHost: true,
				seccompUnconfined: true,
				apparmorUnconfined: true,
			}),
		);
		expect(unrestrictedFields(cleared)).toEqual([]);
		expect(buildDockerOptions(cleared)).toEqual({
			capAdd: ["CHOWN"],
			capDrop: ["ALL"],
			hostname: "web",
		});
	});

	test("the host-access warnings use the control plane's wording", () => {
		expect(requiredUnrestrictedFields(new Error("x"))).toEqual([]);
		expect(
			requiredUnrestrictedFields(
				new ApiError(422, "invalid_config", "x", { fields: ["a"] }),
			),
		).toEqual([]);
	});
});

describe("the Hardened preset", () => {
	test("sets the hardening options and a /tmp tmpfs", () => {
		const hardened = applyHardened(EMPTY_DOCKER_FORM);
		expect(buildDockerOptions(hardened)).toEqual({
			capDrop: ["ALL"],
			noNewPrivileges: true,
			readOnlyRootfs: true,
			pidsLimit: 512,
			tmpfs: [{ path: "/tmp", sizeMb: 64 }],
		});
		expect(isHardened(hardened)).toBe(true);
		expect(isHardened(EMPTY_DOCKER_FORM)).toBe(false);
		expect(messages(hardened, { policy: catalogPolicy })).toEqual([]);
	});

	test("keeps existing tmpfs mounts and other options", () => {
		const start = form({
			capDrop: ["NET_RAW"],
			pidsLimit: "100",
			hostname: "web",
			tmpfs: [{ path: "/cache", sizeMb: "8", mode: "" }],
		});
		const hardened = applyHardened(start);
		expect(hardened.tmpfs).toEqual([{ path: "/cache", sizeMb: "8", mode: "" }]);
		expect(hardened.capDrop).toEqual(["ALL"]);
		expect(hardened.pidsLimit).toBe("512");
		expect(hardened.hostname).toBe("web");
	});

	test("a blank tmpfs row does not count as a tmpfs", () => {
		const hardened = applyHardened(
			form({ tmpfs: [{ path: "", sizeMb: "", mode: "" }] }),
		);
		expect(hardened.tmpfs).toEqual([{ path: "/tmp", sizeMb: "64", mode: "" }]);
	});

	test("applying it twice changes nothing more", () => {
		const once = applyHardened(EMPTY_DOCKER_FORM);
		expect(applyHardened(once)).toEqual(once);
	});

	test("isHardened needs every option", () => {
		const hardened = applyHardened(EMPTY_DOCKER_FORM);
		expect(isHardened({ ...hardened, noNewPrivileges: false })).toBe(false);
		expect(isHardened({ ...hardened, readOnlyRootfs: false })).toBe(false);
		expect(isHardened({ ...hardened, pidsLimit: "" })).toBe(false);
		expect(isHardened({ ...hardened, capDrop: ["NET_RAW"] })).toBe(false);
	});
});

describe("read-only root", () => {
	test("warns when no tmpfs is mounted", () => {
		expect(readOnlyRootfsWarning(form({ readOnlyRootfs: true }))).toContain(
			"/tmp",
		);
		expect(
			readOnlyRootfsWarning(
				form({
					readOnlyRootfs: true,
					tmpfs: [{ path: "/tmp", sizeMb: "", mode: "" }],
				}),
			),
		).toBeUndefined();
		expect(readOnlyRootfsWarning(form({}))).toBeUndefined();
	});

	test("config files must sit on a volume mount", () => {
		const ro = form({ readOnlyRootfs: true });
		expect(readOnlyRootfsFileProblems(ro, ["/etc/app.conf"], [])).toEqual([
			"/etc/app.conf",
		]);
		expect(
			readOnlyRootfsFileProblems(ro, ["/data/app.conf", "/data"], ["/data/"]),
		).toEqual([]);
		expect(
			readOnlyRootfsFileProblems(ro, ["/database/app.conf"], ["/data"]),
		).toEqual(["/database/app.conf"]);
		expect(
			readOnlyRootfsFileProblems(ro, ["/etc//app.conf", ""], ["/etc"]),
		).toEqual([]);
		// A writable root takes any file.
		expect(readOnlyRootfsFileProblems(form({}), ["/etc/app.conf"], [])).toEqual(
			[],
		);
	});
});
