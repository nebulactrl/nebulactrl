import { afterEach, describe, expect, jest, test } from "bun:test";
import { act, render, screen } from "@testing-library/react";
import { useNow } from "./use-now";

function Clock({ intervalMs }: { intervalMs?: number }) {
	const now = useNow(intervalMs);
	return <time>{now}</time>;
}

afterEach(() => {
	jest.useRealTimers();
});

describe("useNow", () => {
	test("starts at the current time and refreshes every 30s by default", () => {
		jest.useFakeTimers();
		jest.setSystemTime(new Date("2026-01-01T00:00:00Z"));
		const start = Date.now();
		render(<Clock />);
		expect(screen.getByText(String(start))).toBeInTheDocument();
		act(() => {
			jest.advanceTimersByTime(29_999);
		});
		expect(screen.getByText(String(start))).toBeInTheDocument();
		act(() => {
			jest.advanceTimersByTime(1);
		});
		expect(screen.getByText(String(start + 30_000))).toBeInTheDocument();
	});

	test("takes its own interval", () => {
		jest.useFakeTimers();
		jest.setSystemTime(new Date("2026-01-01T00:00:00Z"));
		const start = Date.now();
		render(<Clock intervalMs={1000} />);
		act(() => {
			jest.advanceTimersByTime(3000);
		});
		expect(screen.getByText(String(start + 3000))).toBeInTheDocument();
	});

	test("stops ticking once unmounted", () => {
		jest.useFakeTimers();
		const view = render(<Clock intervalMs={1000} />);
		view.unmount();
		expect(jest.getTimerCount()).toBe(0);
	});
});
