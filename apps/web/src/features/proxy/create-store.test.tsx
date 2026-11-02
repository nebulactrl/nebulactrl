import { afterEach, describe, expect, test } from "bun:test";
import { act, renderHook } from "@testing-library/react";
import { routeCreate, useRouteCreateRequest } from "./create-store";

afterEach(() => act(() => routeCreate.close()));

describe("route create store", () => {
	test("has no request until one is opened", () => {
		const { result } = renderHook(() => useRouteCreateRequest());
		expect(result.current).toBeNull();
	});

	test("open publishes the request to every subscriber, close clears it", () => {
		const first = renderHook(() => useRouteCreateRequest());
		const second = renderHook(() => useRouteCreateRequest());

		act(() =>
			routeCreate.open({
				mode: "port",
				position: { x: 4, y: 8 },
				target: { serviceId: "svc-1", port: 25565 },
			}),
		);
		const expected = {
			mode: "port" as const,
			position: { x: 4, y: 8 },
			target: { serviceId: "svc-1", port: 25565 },
		};
		expect(first.result.current).toEqual(expected);
		expect(second.result.current).toEqual(expected);

		act(() => routeCreate.close());
		expect(first.result.current).toBeNull();
		expect(second.result.current).toBeNull();
	});

	test("a later open replaces the earlier request", () => {
		const { result } = renderHook(() => useRouteCreateRequest());
		act(() => routeCreate.open({ mode: "port" }));
		act(() => routeCreate.open({ mode: "external" }));
		expect(result.current).toEqual({ mode: "external" });
	});

	test("an unmounted subscriber stops receiving updates", () => {
		const { result, unmount } = renderHook(() => useRouteCreateRequest());
		unmount();
		act(() => routeCreate.open({ mode: "port" }));
		expect(result.current).toBeNull();
	});

	test("a request opened before a component mounts is read on mount", () => {
		routeCreate.open({ mode: "external" });
		const { result } = renderHook(() => useRouteCreateRequest());
		expect(result.current).toEqual({ mode: "external" });
	});
});
