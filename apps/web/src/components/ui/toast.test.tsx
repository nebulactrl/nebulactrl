import { describe, expect, it, jest, mock } from "bun:test";
import { useNavigate } from "@tanstack/react-router";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { toast } from "#/components/ui/toast";
import { renderWithProviders } from "#/test/render";

const stack = () => screen.queryByRole("region", { name: "Notifications" });
const raise = (fn: () => unknown) => act(async () => void fn());

async function render(path = "/") {
	return renderWithProviders(<p>page</p>, { path });
}

/** Answers `matching(query)` for the queries it is asked; reduced motion stays as the preload set it. */
function withMatchMedia(matching: (query: string) => boolean) {
	const original = window.matchMedia;
	window.matchMedia = ((query: string) => {
		if (query.includes("prefers-reduced-motion"))
			return original.call(window, query);
		return {
			matches: matching(query),
			media: query,
			onchange: null,
			addEventListener: () => {},
			removeEventListener: () => {},
			addListener: () => {},
			removeListener: () => {},
			dispatchEvent: () => false,
		} as MediaQueryList;
	}) as typeof window.matchMedia;
	return () => {
		window.matchMedia = original;
	};
}

/** Buttons that move the router between sections of the app. */
function Nav() {
	const navigate = useNavigate();
	return (
		<>
			<button type="button" onClick={() => void navigate({ to: "/servers" })}>
				go servers
			</button>
			<button
				type="button"
				onClick={() =>
					void navigate({
						to: "/p/$projectId/$envSlug",
						params: { projectId: "abc", envSlug: "prod" },
					})
				}
			>
				go canvas
			</button>
			<button
				type="button"
				onClick={() =>
					void navigate({
						to: "/p/$projectId/$envSlug",
						params: { projectId: "abc", envSlug: "prod" },
						search: { service: "web" } as never,
					})
				}
			>
				go canvas with service
			</button>
			<button
				type="button"
				onClick={() =>
					void navigate({
						to: "/servers/$serverId",
						params: { serverId: "s1" },
					})
				}
			>
				go server detail
			</button>
		</>
	);
}

describe("Toaster", () => {
	it("shows nothing until a toast is raised", async () => {
		await render();
		expect(stack()).toBeNull();
	});

	it("raises a toast with its title and description in a Notifications region, and returns an id", async () => {
		await render();
		let id = "";
		await raise(() => {
			id = toast({ title: "Saved", description: "Settings were updated" });
		});

		const region = await screen.findByRole("region", { name: "Notifications" });
		expect(within(region).getByText("Saved")).toBeInTheDocument();
		expect(
			within(region).getByText("Settings were updated"),
		).toBeInTheDocument();
		expect(typeof id).toBe("string");
		expect(region.querySelector(`[data-toast="${id}"]`)).not.toBeNull();
	});

	it("uses a caller-supplied id", async () => {
		await render();
		await raise(() => toast({ id: "mine", title: "Hello" }));
		await screen.findByText("Hello");
		expect(document.querySelector('[data-toast="mine"]')).not.toBeNull();
	});

	it.each([
		["success", "Success", "success"],
		["error", "Error", "danger"],
		["warning", "Warning", "warning"],
		["info", "Info", "info"],
	] as const)(
		"toast.%s names its kind for assistive tech and carries the %s tone",
		async (kind, label, tone) => {
			await render();
			await raise(() => toast[kind]("Title here", "Details here"));

			const card = (await screen.findByText("Title here")).closest(
				"[data-toast]",
			);
			expect(card).toHaveAttribute("data-tone", tone);
			expect(within(card as HTMLElement).getByText(label)).toHaveClass(
				"sr-only",
			);
			expect(
				within(card as HTMLElement).getByText("Details here"),
			).toBeInTheDocument();
		},
	);

	it("the shorthand without a description shows no description line", async () => {
		await render();
		await raise(() => toast.info("Only a title"));
		const card = (await screen.findByText("Only a title")).closest(
			"[data-toast]",
		) as HTMLElement;
		expect(card.textContent).toBe("InfoOnly a title×");
	});

	it("toast.deployed is a success with the circle-check glyph", async () => {
		await render();
		await raise(() => toast.deployed("Deploy sent", "api-server"));
		const card = (await screen.findByText("Deploy sent")).closest(
			"[data-toast]",
		) as HTMLElement;
		expect(card).toHaveAttribute("data-tone", "success");
		expect(within(card).getByText("Success")).toBeInTheDocument();
		expect(within(card).getByText("api-server")).toBeInTheDocument();
		// The plain success icon is a Lucide svg; the check glyph is a different one.
		const plain = await (async () => {
			await raise(() => toast.success("Plain"));
			return (await screen.findByText("Plain")).closest(
				"[data-toast]",
			) as HTMLElement;
		})();
		expect(card.querySelector("svg")?.outerHTML).not.toBe(
			plain.querySelector("svg")?.outerHTML,
		);
	});

	it("a loading toast shows a spinner labelled Working, and update turns it into the result in place", async () => {
		await render();
		let id = "";
		await raise(() => {
			id = toast.add({ title: "Restarting…", type: "loading" });
		});
		const loading = (await screen.findByText("Restarting…")).closest(
			"[data-toast]",
		) as HTMLElement;
		expect(within(loading).getByText("Working")).toHaveClass("sr-only");
		expect(loading).not.toHaveAttribute("data-tone");

		await raise(() =>
			toast.update(id, { title: "Restarted", type: "success" }),
		);

		await screen.findByText("Restarted");
		expect(screen.queryByText("Restarting…")).toBeNull();
		const card = document.querySelector(`[data-toast="${id}"]`);
		expect(card).toHaveAttribute("data-tone", "success");
		expect(
			within(card as HTMLElement).getByText("Success"),
		).toBeInTheDocument();
		expect(document.querySelectorAll("[data-toast]")).toHaveLength(1);
	});

	it("update can change the description and actions, and an error update stays up", async () => {
		await render();
		const onUndo = mock(() => {});
		let id = "";
		await raise(() => {
			id = toast.add({ title: "Working", description: "step 1" });
		});
		await screen.findByText("step 1");

		await raise(() =>
			toast.update(id, {
				description: "step 2",
				actionProps: { children: "View", onClick: onUndo },
				type: "error",
			}),
		);

		await screen.findByText("step 2");
		expect(screen.getByRole("button", { name: "View" })).toBeInTheDocument();
		expect(document.querySelector(`[data-toast="${id}"]`)).toHaveAttribute(
			"data-tone",
			"danger",
		);
	});

	it("the dismiss button removes that toast", async () => {
		const user = userEvent.setup();
		await render();
		await raise(() => toast.info("Dismiss me"));
		await screen.findByText("Dismiss me");

		await user.click(screen.getByRole("button", { name: "Dismiss" }));

		await waitFor(() => expect(screen.queryByText("Dismiss me")).toBeNull());
		await waitFor(() => expect(stack()).toBeNull());
	});

	it("toast.close(id) removes the toast with that id and ignores an unknown id", async () => {
		await render();
		let id = "";
		await raise(() => {
			id = toast.info("Closable");
		});
		await screen.findByText("Closable");

		await raise(() => toast.close("no-such-toast"));
		expect(screen.getByText("Closable")).toBeInTheDocument();

		await raise(() => toast.close(id));
		await waitFor(() => expect(stack()).toBeNull());
	});

	it("toast.close() removes every toast", async () => {
		await render();
		await raise(() => {
			toast.info("First");
			toast.warning("Second");
		});
		await screen.findByText("Second");

		await raise(() => toast.close());

		await waitFor(() => expect(stack()).toBeNull());
	});

	it("keeps at most four toasts, dropping the oldest", async () => {
		await render();
		await raise(() => {
			for (const n of [1, 2, 3, 4, 5]) toast.info(`toast ${n}`);
		});
		await screen.findByText("toast 5");
		await waitFor(() => expect(screen.queryByText("toast 1")).toBeNull());
		expect(document.querySelectorAll("[data-toast]")).toHaveLength(4);
	});

	it("an info toast dismisses itself after the default 4.2s, an explicit timeout replaces it", async () => {
		await render();
		jest.useFakeTimers();
		try {
			act(() => void toast.info("Default"));
			expect(screen.getByText("Default")).toBeInTheDocument();
			act(() => jest.advanceTimersByTime(4199));
			expect(screen.getByText("Default")).toBeInTheDocument();
			act(() => jest.advanceTimersByTime(1));
			expect(stack()).toBeNull();

			act(() => void toast({ title: "Brief", timeout: 1000 }));
			act(() => jest.advanceTimersByTime(999));
			expect(screen.getByText("Brief")).toBeInTheDocument();
			act(() => jest.advanceTimersByTime(1));
			expect(stack()).toBeNull();
		} finally {
			jest.useRealTimers();
		}
	});

	it("an error toast never dismisses itself by default, but an explicit timeout is honoured", async () => {
		await render();
		jest.useFakeTimers();
		try {
			act(
				() => void toast({ title: "Soft error", type: "error", timeout: 1000 }),
			);
			act(() => jest.advanceTimersByTime(999));
			expect(screen.getByText("Soft error")).toBeInTheDocument();
			act(() => jest.advanceTimersByTime(1));
			expect(stack()).toBeNull();

			act(() => void toast.error("Boom"));
			act(() => jest.advanceTimersByTime(600_000));
			expect(screen.getByText("Boom")).toBeInTheDocument();
		} finally {
			jest.useRealTimers();
		}
	});

	it("shows no drain bar for a toast that never times out", async () => {
		await render();
		await raise(() => toast.error("Sticky"));
		const card = (await screen.findByText("Sticky")).closest(
			"[data-toast]",
		) as HTMLElement;
		expect(card.querySelector('[aria-hidden="true"].origin-left')).toBeNull();
		await raise(() => toast.info("Timed"));
		const timed = (await screen.findByText("Timed")).closest(
			"[data-toast]",
		) as HTMLElement;
		expect(
			timed.querySelector('[aria-hidden="true"].origin-left'),
		).not.toBeNull();
	});

	it("an action button runs its callback and dismisses the toast; the primary action runs without dismissing", async () => {
		const user = userEvent.setup();
		await render();
		const onView = mock(() => {});
		const onUndo = mock(() => {});
		await raise(() =>
			toast({
				title: "Deleted",
				actionProps: { children: "View", onClick: onView },
				secondaryActionProps: { children: "Undo", onClick: onUndo },
			}),
		);
		await screen.findByText("Deleted");

		await user.click(screen.getByRole("button", { name: "Undo" }));
		expect(onUndo).toHaveBeenCalledTimes(1);
		expect(screen.getByText("Deleted")).toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "View" }));
		expect(onView).toHaveBeenCalledTimes(1);
		await waitFor(() => expect(screen.queryByText("Deleted")).toBeNull());
	});

	it("a non-dismissible toast still offers its actions", async () => {
		await render();
		await raise(() =>
			toast({
				title: "Pinned",
				dismissible: false,
				actionProps: { children: "Open", onClick: () => {} },
			}),
		);
		await screen.findByText("Pinned");
		expect(screen.getByRole("button", { name: "Open" })).toBeInTheDocument();
	});
});

describe("Toaster section changes", () => {
	it("clears the toasts when the reader moves to another section", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Nav />, { path: "/" });
		await raise(() => toast.info("Receipt"));
		await screen.findByText("Receipt");

		await user.click(screen.getByRole("button", { name: "go servers" }));

		await waitFor(() => expect(screen.queryByText("Receipt")).toBeNull());
	});

	it("keeps the toasts when the navigation stays inside the same section", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<Nav />, { path: "/servers" });
		await raise(() => toast.info("Stays"));
		await screen.findByText("Stays");

		await user.click(screen.getByRole("button", { name: "go server detail" }));
		await screen.findByText("Stays");
		expect(screen.getByText("Stays")).toBeInTheDocument();
	});

	it("rides 150px above the corner on an open canvas, and 20px elsewhere", async () => {
		const user = userEvent.setup();
		const restore = withMatchMedia(() => false);
		try {
			await renderWithProviders(<Nav />, { path: "/p/abc/canvas" });
			await raise(() => toast.info("Over canvas"));
			expect(
				(await screen.findByRole("region", { name: "Notifications" })).style
					.bottom,
			).toBe("150px");

			// With an inspector open (a `service` search param) it drops back down.
			await user.click(
				screen.getByRole("button", { name: "go canvas with service" }),
			);
			await waitFor(() =>
				expect(
					screen.getByRole("region", { name: "Notifications" }).style.bottom,
				).toBe("20px"),
			);
		} finally {
			restore();
		}
	});

	it("drops to 20px on a narrow screen even over an open canvas", async () => {
		const restore = withMatchMedia((query) => query === "(max-width: 759px)");
		try {
			await renderWithProviders(<Nav />, { path: "/p/abc/canvas" });
			await raise(() => toast.info("Narrow"));
			expect(
				(await screen.findByRole("region", { name: "Notifications" })).style
					.bottom,
			).toBe("20px");
		} finally {
			restore();
		}
	});

	it("sits at 20px on a page that is not a canvas", async () => {
		const restore = withMatchMedia(() => false);
		try {
			await renderWithProviders(<Nav />, { path: "/servers" });
			await raise(() => toast.info("Plain page"));
			expect(
				(await screen.findByRole("region", { name: "Notifications" })).style
					.bottom,
			).toBe("20px");
		} finally {
			restore();
		}
	});
});
