import { describe, expect, test } from "bun:test";
import type { TailnetConfig, TailnetView } from "#/api/gen/types.gen";
import {
	tailnetChipInfo,
	tailnetDifferences,
	tailnetStateInfo,
	trimFqdn,
} from "./tailnet-chip";

const view = (overrides: Partial<TailnetView> = {}): TailnetView => ({
	fqdn: "web-prod.tail1234.ts.net.",
	funnel: false,
	hidePublic: false,
	hostname: "web-prod",
	ips: ["100.64.0.2"],
	message: "",
	needsReprovision: false,
	observedAt: null,
	provider: "tailscale",
	state: "online",
	tcp: null,
	url: "https://web-prod.tail1234.ts.net",
	web: true,
	...overrides,
});

describe("tailnet state words", () => {
	test("every state has a status kind and a word", () => {
		expect(tailnetStateInfo("online")).toEqual({
			kind: "live",
			label: "Online",
		});
		expect(tailnetStateInfo("provisioning").kind).toBe("deploying");
		expect(tailnetStateInfo("offline").label).toBe("Offline");
		expect(tailnetStateInfo("needs_attention").kind).toBe("failed");
		expect(tailnetStateInfo("removing").label).toBe("Removing");
	});

	test("the root dot of a MagicDNS name is dropped", () => {
		expect(trimFqdn("a.b.ts.net.")).toBe("a.b.ts.net");
		expect(trimFqdn("a.b.ts.net")).toBe("a.b.ts.net");
	});
});

describe("tailnet chip", () => {
	test("shows the hostname and states the node state in words", () => {
		const chip = tailnetChipInfo(view());
		expect(chip.label).toBe("web-prod");
		expect(chip.state).toBe("Online");
		expect(chip.kind).toBe("live");
		expect(chip.isPublic).toBe(false);
		expect(chip.tailnetOnly).toBe(false);
		expect(chip.tooltip).toBe("Tailnet node web-prod.tail1234.ts.net · Online");
	});

	test("Funnel is a distinct public mark and says so in the tooltip", () => {
		const chip = tailnetChipInfo(view({ funnel: true }));
		expect(chip.isPublic).toBe(true);
		expect(chip.tooltip).toContain("Public via Funnel");
	});

	test("tailnet only is spelled out", () => {
		const chip = tailnetChipInfo(view({ hidePublic: true }));
		expect(chip.tailnetOnly).toBe(true);
		expect(chip.tooltip).toContain("Tailnet only");
	});

	test("a node that needs attention explains why", () => {
		const chip = tailnetChipInfo(
			view({ state: "needs_attention", message: "Device removed" }),
		);
		expect(chip.kind).toBe("failed");
		expect(chip.tooltip).toContain("Needs attention");
		expect(chip.tooltip).toContain("Device removed");
	});

	test("a node without a name yet falls back", () => {
		const chip = tailnetChipInfo(
			view({ fqdn: "", hostname: "", state: "provisioning" }),
		);
		expect(chip.label).toBe("tailnet");
		expect(chip.tooltip).toContain("(unnamed)");
		expect(chip.kind).toBe("deploying");
	});
});

describe("applied versus staged", () => {
	const applied = view({ tcp: [{ listen: 5432, target: 5432 }] });
	const same: TailnetConfig = {
		enabled: true,
		web: true,
		tcp: [{ listen: 5432, target: 5432 }],
	};

	test("agreeing config and node differ in nothing", () => {
		expect(tailnetDifferences(same, applied)).toEqual([]);
		expect(
			tailnetDifferences({ ...same, hostname: "web-prod" }, applied),
		).toEqual([]);
		expect(tailnetDifferences(undefined, null)).toEqual([]);
	});

	test("a service that is not provisioned yet creates its device", () => {
		expect(tailnetDifferences(same, null)).toEqual(["The device is created"]);
	});

	test("turning it off removes the device, unless that is under way", () => {
		expect(tailnetDifferences(undefined, applied)).toEqual([
			"The device is removed from your tailnet",
		]);
		expect(tailnetDifferences(undefined, view({ state: "removing" }))).toEqual(
			[],
		);
	});

	test("each changed exposure is named", () => {
		expect(
			tailnetDifferences(
				{
					enabled: true,
					hostname: "api",
					web: false,
					funnel: true,
					hidePublic: true,
					tcp: [{ listen: 6379 }],
				},
				applied,
			),
		).toEqual([
			"Hostname web-prod → api",
			"Web exposure off",
			"TCP ports",
			"Funnel on",
			"Tailnet only on",
		]);
	});

	test("a target left at the listen port is the same rule", () => {
		expect(
			tailnetDifferences(
				{ ...same, tcp: [{ listen: 5432 }] },
				view({ tcp: [{ listen: 5432, target: 5432 }] }),
			),
		).toEqual([]);
	});
});
