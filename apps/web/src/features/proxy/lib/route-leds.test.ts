import { describe, expect, test } from "bun:test";
import { routeLeds } from "./route-leds";

describe("routeLeds", () => {
	test("an http route shows Route and Verify", () => {
		expect(routeLeds(["done", "live", "off"], "http")).toEqual([
			{ label: "Route", state: "live" },
			{ label: "Verify", state: "off" },
		]);
		expect(routeLeds(["done", "done", "done"], "http")).toEqual([
			{ label: "Route", state: "done" },
			{ label: "Verify", state: "done" },
		]);
	});
	test("queued and preparing light the Route LED", () => {
		expect(routeLeds(["live", "off", "off"], "http")[0]).toEqual({
			label: "Route",
			state: "live",
		});
	});
	test("a failure at verify stays on Verify", () => {
		expect(routeLeds(["done", "done", "fail"], "http")).toEqual([
			{ label: "Route", state: "done" },
			{ label: "Verify", state: "fail" },
		]);
	});
	test("tcp and udp routes have a single LED", () => {
		expect(routeLeds(["off", "off", "off"], "tcp")).toEqual([
			{ label: "Route", state: "off" },
		]);
		expect(routeLeds(["done", "live", "off"], "udp")[0]?.state).toBe("live");
		expect(routeLeds(["done", "done", "done"], "tcp")[0]?.state).toBe("done");
		expect(routeLeds(["done", "done", "fail"], "tcp")[0]?.state).toBe("fail");
	});
});
