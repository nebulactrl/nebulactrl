import { describe, expect, test } from "bun:test";
import type { NetworkRef } from "#/api/gen/types.gen";
import {
	attachBlockedReason,
	networkChipLabel,
	networkChipTitle,
	privateNetworkCaption,
} from "./model";

const ref = (id: string, name: string, serverId: string): NetworkRef => ({
	id,
	name,
	serverId,
	alias: `api.production.${name}`,
});

describe("attachBlockedReason", () => {
	const base = {
		kind: "app" as const,
		serverId: "s1",
		config: { blockEgress: false },
	};

	test("a placed service that allows outbound can attach", () => {
		expect(attachBlockedReason(base as never)).toBeNull();
	});

	test("a service that blocks outbound internet cannot", () => {
		const reason = attachBlockedReason({
			...base,
			config: { blockEgress: true },
		} as never);
		expect(reason).toContain("blocks outbound internet");
	});

	test("a service without a server cannot", () => {
		expect(attachBlockedReason({ ...base, serverId: null } as never)).toContain(
			"server",
		);
	});

	test("a route cannot", () => {
		expect(attachBlockedReason({ ...base, kind: "route" } as never)).toContain(
			"no container",
		);
	});
});

describe("canvas chip", () => {
	test("names one network and counts several", () => {
		expect(networkChipLabel([])).toBe("");
		expect(networkChipLabel([ref("a", "backend", "s1")])).toBe("backend");
		expect(
			networkChipLabel([ref("a", "backend", "s1"), ref("b", "data", "s1")]),
		).toBe("2 networks");
	});

	test("the tooltip carries every hostname", () => {
		expect(networkChipTitle([ref("a", "backend", "s1")])).toBe(
			"backend: api.production.backend",
		);
	});

	test("the floor caption is sentence case", () => {
		const caption = privateNetworkCaption("production");
		expect(caption).toBe("Private network · production");
		expect(caption).not.toBe(caption.toUpperCase());
	});
});
