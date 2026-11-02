import { describe, expect, mock, test } from "bun:test";
import { onOpenStagedReview, openStagedReview } from "./open-review";

describe("openStagedReview", () => {
	test("tells every listener, with the scope it was opened for", () => {
		const a = mock((_options: { serviceId?: string }) => {});
		const b = mock((_options: { serviceId?: string }) => {});
		const offA = onOpenStagedReview(a);
		const offB = onOpenStagedReview(b);
		openStagedReview({ serviceId: "svc-1" });
		openStagedReview();
		expect(a.mock.calls).toEqual([[{ serviceId: "svc-1" }], [{}]]);
		expect(b.mock.calls).toEqual([[{ serviceId: "svc-1" }], [{}]]);
		offA();
		offB();
	});

	test("a listener that unsubscribed hears nothing more", () => {
		const listener = mock((_options: { serviceId?: string }) => {});
		const off = onOpenStagedReview(listener);
		off();
		openStagedReview({ serviceId: "svc-1" });
		expect(listener).not.toHaveBeenCalled();
	});

	test("with no listener mounted it does nothing", () => {
		expect(() => openStagedReview()).not.toThrow();
	});
});
