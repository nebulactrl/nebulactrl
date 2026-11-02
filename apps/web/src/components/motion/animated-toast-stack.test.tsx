import { afterEach, describe, expect, jest, test } from "bun:test";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { useSyncExternalStore } from "react";
import {
	AnimatedToastStack,
	type AnimatedToastStackProps,
	createAnimatedToastStore,
} from "./animated-toast-stack";

afterEach(() => {
	jest.useRealTimers();
});

describe("createAnimatedToastStore", () => {
	test("adds newest first, defaults the kind and returns the id", () => {
		const store = createAnimatedToastStore();
		const a = store.add({ title: "A" });
		const b = store.add({ title: "B", kind: "error", dismissible: false });
		const snapshot = store.getSnapshot();
		expect(snapshot.map((t) => t.id)).toEqual([b, a]);
		expect(snapshot[1]).toMatchObject({
			kind: "info",
			dismissible: true,
			duration: 4200,
		});
		expect(snapshot[0]).toMatchObject({ kind: "error", dismissible: false });
	});

	test("keeps only the newest `limit` toasts", () => {
		const store = createAnimatedToastStore(4200, 2);
		store.add({ title: "1", id: "one" });
		store.add({ title: "2", id: "two" });
		store.add({ title: "3", id: "three" });
		expect(store.getSnapshot().map((t) => t.id)).toEqual(["three", "two"]);
	});

	test("adding an id that exists replaces it and moves it to the front", () => {
		const store = createAnimatedToastStore();
		store.add({ id: "x", title: "first" });
		store.add({ id: "y", title: "other" });
		store.add({ id: "x", title: "second" });
		expect(store.getSnapshot().map((t) => [t.id, t.title])).toEqual([
			["x", "second"],
			["y", "other"],
		]);
	});

	test("notifies subscribers on change and stops after unsubscribe", () => {
		const store = createAnimatedToastStore();
		const listener = jest.fn();
		const unsubscribe = store.subscribe(listener);
		const id = store.add({ title: "A" });
		store.update(id, { title: "B" });
		store.dismiss(id);
		expect(listener).toHaveBeenCalledTimes(3);
		unsubscribe();
		store.add({ title: "C" });
		expect(listener).toHaveBeenCalledTimes(3);
	});

	test("a toast dismisses itself after its duration", () => {
		jest.useFakeTimers();
		const store = createAnimatedToastStore();
		store.add({ title: "A", duration: 1000 });
		jest.advanceTimersByTime(999);
		expect(store.getSnapshot()).toHaveLength(1);
		jest.advanceTimersByTime(1);
		expect(store.getSnapshot()).toHaveLength(0);
	});

	test("the store's default duration applies when a toast gives none", () => {
		jest.useFakeTimers();
		const store = createAnimatedToastStore(500);
		store.add({ title: "A" });
		jest.advanceTimersByTime(500);
		expect(store.getSnapshot()).toHaveLength(0);
	});

	test("a duration of 0 never dismisses on its own", () => {
		jest.useFakeTimers();
		const store = createAnimatedToastStore();
		store.add({ title: "A", duration: 0 });
		jest.advanceTimersByTime(60_000);
		expect(store.getSnapshot()).toHaveLength(1);
	});

	test("pausing holds the remaining time; resuming runs it out", () => {
		jest.useFakeTimers();
		const store = createAnimatedToastStore();
		store.add({ title: "A", duration: 1000 });
		jest.advanceTimersByTime(400);
		store.pause();
		jest.advanceTimersByTime(60_000);
		expect(store.getSnapshot()).toHaveLength(1);
		store.resume();
		jest.advanceTimersByTime(599);
		expect(store.getSnapshot()).toHaveLength(1);
		jest.advanceTimersByTime(1);
		expect(store.getSnapshot()).toHaveLength(0);
	});

	test("a toast added while paused waits for the resume to start its clock", () => {
		jest.useFakeTimers();
		const store = createAnimatedToastStore();
		store.add({ title: "A", duration: 1000 });
		store.pause();
		store.add({ title: "B", duration: 1000 });
		jest.advanceTimersByTime(5000);
		expect(store.getSnapshot()).toHaveLength(2);
		store.resume();
		jest.advanceTimersByTime(1000);
		expect(store.getSnapshot()).toHaveLength(0);
	});

	test("pause and resume are idempotent", () => {
		jest.useFakeTimers();
		const store = createAnimatedToastStore();
		store.add({ title: "A", duration: 1000 });
		store.resume();
		store.pause();
		jest.advanceTimersByTime(300);
		store.pause();
		store.resume();
		store.resume();
		jest.advanceTimersByTime(1000);
		expect(store.getSnapshot()).toHaveLength(0);
	});

	test("update changes only the fields given and leaves others toasts alone", () => {
		const store = createAnimatedToastStore();
		const a = store.add({
			title: "Restarting…",
			kind: "loading",
			description: "web",
			duration: 0,
		});
		const b = store.add({ title: "other" });
		store.update(a, { title: "Restarted", kind: "success", glyph: "check" });
		const byId = Object.fromEntries(store.getSnapshot().map((t) => [t.id, t]));
		expect(byId[a]).toMatchObject({
			title: "Restarted",
			kind: "success",
			glyph: "check",
			description: "web",
			duration: 0,
		});
		expect(byId[b]?.title).toBe("other");
	});

	test("update can set the actions and dismissibility", () => {
		const store = createAnimatedToastStore();
		const id = store.add({ title: "A" });
		const action = { label: "View", onClick: () => {} };
		const primaryAction = { label: "Undo", onClick: () => {} };
		store.update(id, {
			action,
			primaryAction,
			dismissible: false,
			description: "d",
		});
		expect(store.getSnapshot()[0]).toMatchObject({
			action,
			primaryAction,
			dismissible: false,
			description: "d",
		});
	});

	test("update with a duration restarts the clock", () => {
		jest.useFakeTimers();
		const store = createAnimatedToastStore();
		const id = store.add({ title: "A", duration: 1000 });
		jest.advanceTimersByTime(900);
		store.update(id, { duration: 1000 });
		jest.advanceTimersByTime(900);
		expect(store.getSnapshot()).toHaveLength(1);
		jest.advanceTimersByTime(100);
		expect(store.getSnapshot()).toHaveLength(0);
	});

	test("update with duration 0 makes a toast stay", () => {
		jest.useFakeTimers();
		const store = createAnimatedToastStore();
		const id = store.add({ title: "A", duration: 1000 });
		store.update(id, { duration: 0 });
		jest.advanceTimersByTime(10_000);
		expect(store.getSnapshot()).toHaveLength(1);
	});

	test("updating an unknown id changes nothing", () => {
		const store = createAnimatedToastStore();
		store.add({ title: "A" });
		store.update("nope", { title: "B" });
		expect(store.getSnapshot().map((t) => t.title)).toEqual(["A"]);
	});

	test("dismiss cancels the toast's timer and clear empties the stack", () => {
		jest.useFakeTimers();
		const store = createAnimatedToastStore();
		const a = store.add({ title: "A", duration: 1000 });
		store.add({ title: "B", duration: 1000 });
		store.dismiss(a);
		expect(store.getSnapshot().map((t) => t.title)).toEqual(["B"]);
		store.clear();
		expect(store.getSnapshot()).toEqual([]);
		// Replacement with the same id after a clear gets a fresh timer.
		store.add({ title: "C", duration: 1000, id: a });
		jest.advanceTimersByTime(1000);
		expect(store.getSnapshot()).toEqual([]);
	});
});

function Live({
	store,
	...rest
}: {
	store: ReturnType<typeof createAnimatedToastStore>;
} & Partial<AnimatedToastStackProps>) {
	const toasts = useSyncExternalStore(
		store.subscribe,
		store.getSnapshot,
		store.getSnapshot,
	);
	return (
		<AnimatedToastStack
			toasts={toasts}
			bottom={20}
			onDismiss={store.dismiss}
			onPause={store.pause}
			onResume={store.resume}
			{...rest}
		/>
	);
}

function toastFixture(
	overrides: Partial<
		Parameters<ReturnType<typeof createAnimatedToastStore>["add"]>[0]
	> = {},
) {
	return { title: "Deploy sent", ...overrides };
}

function mountStack(
	props: Partial<AnimatedToastStackProps> & {
		toasts?: AnimatedToastStackProps["toasts"];
	} = {},
) {
	const store = createAnimatedToastStore(0);
	const onDismiss = jest.fn();
	const onPause = jest.fn();
	const onResume = jest.fn();
	const toasts = props.toasts ?? [];
	const view = render(
		<AnimatedToastStack
			toasts={toasts}
			bottom={20}
			onDismiss={onDismiss}
			onPause={onPause}
			onResume={onResume}
			{...props}
		/>,
	);
	return { store, onDismiss, onPause, onResume, ...view };
}

function makeToasts(
	...inputs: Array<ReturnType<typeof toastFixture> & { id?: string }>
) {
	const store = createAnimatedToastStore(0, 10);
	for (const input of [...inputs].reverse())
		store.add({ duration: 0, ...input });
	return store.getSnapshot();
}

/** Runs with a device that can (or cannot) hover: the stack only expands and pauses on one that can. */
function withHover<T>(run: () => T, canHover = true): T {
	const real = window.matchMedia;
	window.matchMedia = ((query: string) => ({
		matches: canHover && query.includes("hover: hover"),
		media: query,
		onchange: null,
		addEventListener() {},
		removeEventListener() {},
		addListener() {},
		removeListener() {},
		dispatchEvent: () => false,
	})) as unknown as typeof window.matchMedia;
	try {
		return run();
	} finally {
		window.matchMedia = real;
	}
}

describe("AnimatedToastStack", () => {
	test("renders nothing with no toasts", () => {
		mountStack();
		expect(screen.queryByRole("region", { name: "Notifications" })).toBeNull();
	});

	test("lists the toasts in a Notifications region raised by `bottom`", () => {
		mountStack({
			toasts: makeToasts(
				{ id: "a", title: "Deploy sent", description: "checkout-api" },
				{ id: "b", title: "Backup done" },
			),
			bottom: 96,
		});
		const region = screen.getByRole("region", { name: "Notifications" });
		expect(region.style.bottom).toBe("96px");
		expect(within(region).getByText("Deploy sent")).toBeInTheDocument();
		expect(within(region).getByText("checkout-api")).toBeInTheDocument();
		expect(within(region).getByText("Backup done")).toBeInTheDocument();
		expect(document.body.contains(region)).toBe(true);
	});

	test("portals into the given root", () => {
		const root = document.createElement("div");
		document.body.append(root);
		mountStack({
			toasts: makeToasts({ id: "a", title: "Hi" }),
			portalRoot: root,
		});
		expect(
			within(root).getByRole("region", { name: "Notifications" }),
		).toBeInTheDocument();
	});

	test.each([
		["info", "Info"],
		["success", "Success"],
		["warning", "Warning"],
		["error", "Error"],
		["loading", "Working"],
	] as const)(
		"a %s toast is labelled %s for assistive tech, not by colour alone",
		(kind, label) => {
			mountStack({ toasts: makeToasts({ id: "a", title: "T", kind }) });
			expect(screen.getByText(label)).toHaveClass("sr-only");
		},
	);

	test("a deployed toast with the check glyph is still announced as Success", () => {
		mountStack({
			toasts: makeToasts({
				id: "a",
				title: "Deploy sent",
				kind: "success",
				glyph: "check",
			}),
		});
		expect(screen.getByText("Success")).toBeInTheDocument();
	});

	test("the toast's tone is exposed on its card; loading has none", () => {
		mountStack({
			toasts: makeToasts(
				{ id: "e", title: "Failed", kind: "error" },
				{ id: "l", title: "Working", kind: "loading" },
			),
		});
		expect(document.querySelector('[data-toast="e"]')).toHaveAttribute(
			"data-tone",
			"danger",
		);
		expect(document.querySelector('[data-toast="l"]')).not.toHaveAttribute(
			"data-tone",
		);
	});

	test("the dismiss button reports the toast's id", () => {
		const { onDismiss } = mountStack({
			toasts: makeToasts({ id: "a", title: "Deploy sent" }),
		});
		fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
		expect(onDismiss).toHaveBeenCalledTimes(1);
		expect(onDismiss).toHaveBeenCalledWith("a");
	});

	test("the outline action runs and then dismisses the toast", () => {
		const onClick = jest.fn();
		const { onDismiss } = mountStack({
			toasts: makeToasts({
				id: "a",
				title: "Failed",
				action: { label: "View logs", onClick },
			}),
		});
		fireEvent.click(screen.getByRole("button", { name: "View logs" }));
		expect(onClick).toHaveBeenCalledTimes(1);
		expect(onDismiss).toHaveBeenCalledWith("a");
	});

	test("the primary action runs and leaves the toast up", () => {
		const onClick = jest.fn();
		const { onDismiss } = mountStack({
			toasts: makeToasts({
				id: "a",
				title: "Deleted",
				primaryAction: { label: "Undo", onClick },
			}),
		});
		fireEvent.click(screen.getByRole("button", { name: "Undo" }));
		expect(onClick).toHaveBeenCalledTimes(1);
		expect(onDismiss).not.toHaveBeenCalled();
	});

	test("only the front toast offers its actions until the stack is hovered", () => {
		withHover(() => {
			const frontClick = jest.fn();
			const backClick = jest.fn();
			mountStack({
				toasts: makeToasts(
					{
						id: "front",
						title: "Front",
						action: { label: "Front action", onClick: frontClick },
					},
					{
						id: "back",
						title: "Back",
						action: { label: "Back action", onClick: backClick },
					},
				),
			});
			expect(
				screen.getByRole("button", { name: "Front action" }),
			).toBeInTheDocument();
			expect(screen.queryByRole("button", { name: "Back action" })).toBeNull();
			fireEvent.mouseEnter(
				screen.getByRole("region", { name: "Notifications" }),
			);
			expect(
				screen.getByRole("button", { name: "Back action" }),
			).toBeInTheDocument();
			fireEvent.mouseLeave(
				screen.getByRole("region", { name: "Notifications" }),
			);
			expect(screen.queryByRole("button", { name: "Back action" })).toBeNull();
		});
	});

	test("hovering pauses the store and leaving resumes it", () => {
		withHover(() => {
			const { onPause, onResume } = mountStack({
				toasts: makeToasts({ id: "a", title: "A" }),
			});
			const region = screen.getByRole("region", { name: "Notifications" });
			fireEvent.mouseEnter(region);
			expect(onPause).toHaveBeenCalledTimes(1);
			expect(onResume).not.toHaveBeenCalled();
			fireEvent.mouseLeave(region);
			expect(onResume).toHaveBeenCalledTimes(1);
		});
	});

	test("a device that cannot hover never pauses or expands", () => {
		withHover(() => {
			const { onPause } = mountStack({
				toasts: makeToasts(
					{ id: "a", title: "A" },
					{
						id: "b",
						title: "B",
						action: { label: "Back action", onClick: () => {} },
					},
				),
			});
			const region = screen.getByRole("region", { name: "Notifications" });
			fireEvent.mouseEnter(region);
			expect(onPause).not.toHaveBeenCalled();
			expect(screen.queryByRole("button", { name: "Back action" })).toBeNull();
		}, false);
	});

	test("the stack resumes the store when its last toast goes away under a hovering pointer", () => {
		withHover(() => {
			const onResume = jest.fn();
			const onPause = jest.fn();
			const toasts = makeToasts({ id: "a", title: "A" });
			const props = { bottom: 20, onDismiss: () => {}, onPause, onResume };
			const { rerender } = render(
				<AnimatedToastStack toasts={toasts} {...props} />,
			);
			fireEvent.mouseEnter(
				screen.getByRole("region", { name: "Notifications" }),
			);
			onResume.mockClear();
			rerender(<AnimatedToastStack toasts={[]} {...props} />);
			expect(onResume).toHaveBeenCalled();
			expect(
				screen.queryByRole("region", { name: "Notifications" }),
			).toBeNull();
		});
	});

	test("the front toast shows a drain bar for its duration; loading and untimed ones do not", () => {
		mountStack({ toasts: makeToasts({ id: "a", title: "A", duration: 3000 }) });
		const bar = document.querySelector<HTMLElement>(
			'[data-toast="a"] > span[aria-hidden]:last-child',
		);
		expect(bar?.style.animationDuration).toBe("3000ms");
	});

	test.each([
		["a loading toast", { kind: "loading" as const, duration: 3000 }],
		["a toast with no timer", { duration: 0 }],
	])("%s has no drain bar", (_name, input) => {
		mountStack({ toasts: makeToasts({ id: "a", title: "A", ...input }) });
		expect(
			document.querySelector('[data-toast="a"] .shadow-toast-drain'),
		).toBeNull();
	});

	test("the dismiss button leaves the toast removed in a store-backed stack", () => {
		const store = createAnimatedToastStore(0);
		render(<Live store={store} />);
		act(() => {
			store.add({ title: "Deploy sent", duration: 0 });
		});
		expect(screen.getByText("Deploy sent")).toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
		expect(store.getSnapshot()).toEqual([]);
	});

	test("a new toast joins a store-backed stack as it is added", () => {
		const store = createAnimatedToastStore(0);
		render(<Live store={store} />);
		expect(screen.queryByRole("region", { name: "Notifications" })).toBeNull();
		act(() => {
			store.add({ title: "First", duration: 0 });
			store.add({ title: "Second", duration: 0 });
		});
		expect(screen.getByText("First")).toBeInTheDocument();
		expect(screen.getByText("Second")).toBeInTheDocument();
	});
});

describe("AnimatedToastStack swipe", () => {
	function swipeSetup(overrides: Record<string, unknown> = {}) {
		const view = mountStack({
			toasts: makeToasts({ id: "a", title: "Swipe me", ...overrides }),
		});
		jest.useFakeTimers();
		const card = document.querySelector('[data-toast="a"]') as HTMLElement;
		const drag = card.parentElement as HTMLElement;
		return { ...view, card, drag };
	}

	function drag(target: HTMLElement, from: number, to: number) {
		fireEvent.pointerDown(target, { pointerId: 7, clientX: from });
		fireEvent.pointerMove(window, { pointerId: 7, clientX: to });
		fireEvent.pointerUp(window, { pointerId: 7, clientX: to });
	}

	test("dragging past the threshold slides it away and dismisses it to that side", () => {
		const { card, drag: dragEl, onDismiss } = swipeSetup();
		drag(card, 100, 260);
		expect(dragEl.style.translate).toBe("480px 0");
		expect(onDismiss).not.toHaveBeenCalled();
		act(() => {
			jest.advanceTimersByTime(340);
		});
		expect(onDismiss).toHaveBeenCalledWith("a", "right");
	});

	test("dragging left dismisses to the left", () => {
		const { card, onDismiss } = swipeSetup();
		drag(card, 300, 100);
		act(() => {
			jest.advanceTimersByTime(340);
		});
		expect(onDismiss).toHaveBeenCalledWith("a", "left");
	});

	test("a short drag snaps back and dismisses nothing", () => {
		const { card, drag: dragEl, onDismiss } = swipeSetup();
		drag(card, 100, 150);
		expect(dragEl.style.translate).toBe("0 0");
		expect(dragEl.style.opacity).toBe("1");
		act(() => {
			jest.advanceTimersByTime(1000);
		});
		expect(onDismiss).not.toHaveBeenCalled();
	});

	test("the card follows the pointer and fades while dragging", () => {
		const { card, drag: dragEl } = swipeSetup();
		fireEvent.pointerDown(card, { pointerId: 7, clientX: 100 });
		fireEvent.pointerMove(window, { pointerId: 7, clientX: 170 });
		expect(dragEl.style.translate).toBe("70px 0");
		expect(Number(dragEl.style.opacity)).toBeCloseTo(0.75);
		fireEvent.pointerCancel(window, { pointerId: 7 });
		expect(dragEl.style.translate).toBe("0 0");
	});

	test("a cancelled gesture never dismisses, even past the threshold", () => {
		const { card, onDismiss } = swipeSetup();
		fireEvent.pointerDown(card, { pointerId: 7, clientX: 100 });
		fireEvent.pointerMove(window, { pointerId: 7, clientX: 400 });
		fireEvent.pointerCancel(window, { pointerId: 7 });
		act(() => {
			jest.advanceTimersByTime(1000);
		});
		expect(onDismiss).not.toHaveBeenCalled();
	});

	test("another pointer's events are ignored", () => {
		const { card, onDismiss } = swipeSetup();
		fireEvent.pointerDown(card, { pointerId: 7, clientX: 100 });
		fireEvent.pointerMove(window, { pointerId: 8, clientX: 400 });
		fireEvent.pointerUp(window, { pointerId: 8, clientX: 400 });
		act(() => {
			jest.advanceTimersByTime(1000);
		});
		expect(onDismiss).not.toHaveBeenCalled();
	});

	test("a press on a button does not start a swipe", () => {
		const { onDismiss } = swipeSetup();
		const button = screen.getByRole("button", { name: "Dismiss" });
		drag(button, 100, 400);
		act(() => {
			jest.advanceTimersByTime(1000);
		});
		expect(onDismiss).not.toHaveBeenCalled();
	});

	test("a toast that is not dismissible cannot be swiped away", () => {
		const { card, onDismiss } = swipeSetup({ dismissible: false });
		drag(card, 100, 400);
		act(() => {
			jest.advanceTimersByTime(1000);
		});
		expect(onDismiss).not.toHaveBeenCalled();
	});

	test("only the front toast can be swiped while the stack is collapsed", () => {
		const toasts = makeToasts(
			{ id: "front", title: "Front" },
			{ id: "back", title: "Back" },
		);
		const { onDismiss } = mountStack({ toasts });
		jest.useFakeTimers();
		const back = document.querySelector('[data-toast="back"]') as HTMLElement;
		drag(back, 100, 400);
		act(() => {
			jest.advanceTimersByTime(1000);
		});
		expect(onDismiss).not.toHaveBeenCalled();
	});
});
