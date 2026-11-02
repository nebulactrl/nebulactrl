import { afterEach, describe, expect, it, jest } from "bun:test";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createRef } from "react";
import { AsciiBackdrop } from "./ascii/ascii-backdrop";
import { DocsButton } from "./docs-button";
import { EmptyState } from "./empty-state";
import { ErrorState } from "./error-state";
import { KpiNumber, renderKpi } from "./kpi-number";
import { Loading } from "./loading";
import { Relative } from "./relative";

afterEach(() => {
	jest.useRealTimers();
});

describe("ErrorState", () => {
	it("announces the failure as an alert with its title and body", () => {
		render(<ErrorState title="Servers did not load" body="Nothing changed." />);
		const alert = screen.getByRole("alert");
		expect(alert).toHaveTextContent("Servers did not load");
		expect(alert).toHaveTextContent("Nothing changed.");
		expect(alert).toHaveAttribute("data-tone", "danger");
	});

	it("offers no buttons when there is no way out", () => {
		render(<ErrorState title="t" body="b" />);
		expect(screen.queryByRole("button")).toBeNull();
	});

	it("runs retry from the Try again button", async () => {
		const retry = jest.fn();
		render(<ErrorState title="t" body="b" retry={retry} />);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Try again" }));
		expect(retry).toHaveBeenCalledTimes(1);
	});

	it("shows a custom action next to retry", () => {
		render(
			<ErrorState
				title="t"
				body="b"
				retry={() => {}}
				action={<a href="/projects">Go to projects</a>}
			/>,
		);
		expect(
			screen.getByRole("link", { name: "Go to projects" }),
		).toHaveAttribute("href", "/projects");
		expect(
			screen.getByRole("button", { name: "Try again" }),
		).toBeInTheDocument();
	});

	it("shows only the action when trying again cannot help", () => {
		render(
			<ErrorState title="Not found" body="b" action={<a href="/">Home</a>} />,
		);
		expect(screen.getByRole("link", { name: "Home" })).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
	});

	it("keeps the page floor neutral for a page-sized state", () => {
		render(<ErrorState title="t" body="b" neutral />);
		expect(screen.getByRole("alert")).not.toHaveAttribute("data-tone");
	});
});

describe("Loading", () => {
	it("shows the caption at once with no delay", () => {
		render(<Loading label="Loading project" delayMs={0} />);
		expect(screen.getByRole("status")).toHaveTextContent("Loading project");
	});

	it("shows nothing until the delay has passed, so fast loads never flash", () => {
		jest.useFakeTimers();
		render(<Loading label="Loading project" delayMs={300} />);
		expect(screen.queryByRole("status")).toBeNull();
		act(() => {
			jest.advanceTimersByTime(299);
		});
		expect(screen.queryByRole("status")).toBeNull();
		act(() => {
			jest.advanceTimersByTime(1);
		});
		expect(screen.getByRole("status")).toHaveTextContent("Loading project");
	});

	it("defaults to a 300 ms delay", () => {
		jest.useFakeTimers();
		render(<Loading label="Loading" />);
		expect(screen.queryByRole("status")).toBeNull();
		act(() => {
			jest.advanceTimersByTime(300);
		});
		expect(screen.getByRole("status")).toBeInTheDocument();
	});

	it("never shows if it unmounts before the delay", () => {
		jest.useFakeTimers();
		const { unmount } = render(<Loading label="Loading" delayMs={100} />);
		unmount();
		act(() => {
			jest.advanceTimersByTime(500);
		});
		expect(screen.queryByRole("status")).toBeNull();
	});

	it("passes extra props through to the status element", () => {
		render(<Loading label="Loading" delayMs={0} data-testid="loader" />);
		expect(screen.getByTestId("loader")).toBe(screen.getByRole("status"));
	});
});

describe("Relative", () => {
	it("reads a past instant as a short relative time with the UTC instant as title", () => {
		const iso = new Date(Date.now() - 5 * 60_000 - 1_000).toISOString();
		render(<Relative iso={iso} />);
		const time = screen.getByText("5m ago");
		expect(time.tagName).toBe("TIME");
		expect(time).toHaveAttribute("datetime", iso);
		expect(time.getAttribute("title")).toMatch(
			/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} UTC$/,
		);
	});

	it("reads an em dash named 'never' when nothing happened", () => {
		render(<Relative iso={null} />);
		expect(screen.getByRole("img", { name: "never" })).toHaveTextContent("—");
	});

	it("treats undefined and empty the same as null", () => {
		render(
			<>
				<Relative iso={undefined} />
				<Relative iso="" />
			</>,
		);
		expect(screen.getAllByRole("img", { name: "never" })).toHaveLength(2);
	});

	it("refreshes every 30 seconds from one shared timer", () => {
		jest.useFakeTimers();
		jest.setSystemTime(new Date("2026-11-01T12:00:00Z"));
		render(
			<>
				<Relative iso="2026-11-01T11:59:50Z" />
				<Relative iso="2026-11-01T11:59:50Z" />
			</>,
		);
		expect(screen.getAllByText("10s ago")).toHaveLength(2);
		act(() => {
			jest.advanceTimersByTime(30_000);
		});
		expect(screen.getAllByText("40s ago")).toHaveLength(2);
	});

	it("stops ticking once the last instance unmounts", () => {
		jest.useFakeTimers();
		const { unmount } = render(<Relative iso="2026-11-01T11:59:50Z" />);
		expect(jest.getTimerCount()).toBeGreaterThan(0);
		unmount();
		expect(jest.getTimerCount()).toBe(0);
	});
});

describe("EmptyState", () => {
	it("shows the title as a heading, the body and the actions", () => {
		render(
			<EmptyState
				illustration="planet"
				title="Create your first project"
				body="Projects group services."
				action={<button type="button">New project</button>}
				secondaryAction={<a href="/docs">Read the docs</a>}
			/>,
		);
		expect(
			screen.getByRole("heading", { name: "Create your first project" }),
		).toBeInTheDocument();
		expect(screen.getByText("Projects group services.")).toBeInTheDocument();
		expect(
			screen.getByRole("button", { name: "New project" }),
		).toBeInTheDocument();
		expect(
			screen.getByRole("link", { name: "Read the docs" }),
		).toBeInTheDocument();
	});

	it("draws the named illustration, hidden from assistive tech", () => {
		const { container } = render(
			<EmptyState illustration="rack" title="No servers" />,
		);
		const art = container.querySelector("[data-slot=dither-illustration]");
		expect(art).toHaveAttribute("data-illustration", "rack");
		expect(art).toHaveAttribute("aria-hidden", "true");
	});

	it("draws no backdrop", () => {
		const { container } = render(
			<EmptyState illustration="box" title="Empty" />,
		);
		expect(container.querySelector("[data-slot=dither-backdrop]")).toBeNull();
	});
});

describe("DocsButton", () => {
	it("links to the docs page named by the file path", () => {
		render(
			<DocsButton path="installing/servers.md">
				Server install guide
			</DocsButton>,
		);
		expect(
			screen.getByRole("button", { name: /Server install guide/ }),
		).toHaveAttribute("href", "/docs/installing/servers");
	});

	it("maps a README to its directory page", () => {
		render(<DocsButton path="installing/README.md">Install</DocsButton>);
		expect(screen.getByRole("button", { name: /Install/ })).toHaveAttribute(
			"href",
			"/docs/installing",
		);
	});
});

describe("KpiNumber", () => {
	it("gives screen readers the formatted text for a number", () => {
		const { container } = render(<KpiNumber text="1.2k" />);
		expect(container).toHaveTextContent("1.2k");
	});

	it("shows non-numeric text as it is", () => {
		render(<KpiNumber text="3h 12m" />);
		expect(screen.getByText("3h 12m")).toBeInTheDocument();
	});

	it("shows a dash for an unmeasured value", () => {
		render(<KpiNumber text="–" />);
		expect(screen.getByText("–")).toBeInTheDocument();
	});

	it("renders through renderKpi for the capacity stat", () => {
		render(renderKpi("2/2"));
		expect(screen.getByText("2/2")).toBeInTheDocument();
	});

	it("updates when the value changes", () => {
		const { rerender, container } = render(<KpiNumber text="12.3%" />);
		rerender(<KpiNumber text="45.6%" />);
		expect(container).toHaveTextContent("45.6%");
	});
});

describe("AsciiBackdrop", () => {
	it("is a decorative host that stays transparent without WebGL", () => {
		const cardRef = createRef<HTMLElement>();
		const onActiveChange = jest.fn();
		const { container } = render(
			<AsciiBackdrop
				cardRef={cardRef}
				active={false}
				onActiveChange={onActiveChange}
			/>,
		);
		const host = container.firstElementChild;
		expect(host).toHaveAttribute("aria-hidden", "true");
		expect(host).not.toHaveAttribute("data-active");
		// No GL context: the canvas is removed again.
		expect(container.querySelector("canvas")).toBeNull();
	});

	it("marks the host active when the engine reports ready", () => {
		const { container } = render(
			<AsciiBackdrop
				cardRef={createRef<HTMLElement>()}
				active
				onActiveChange={() => {}}
			/>,
		);
		expect(container.firstElementChild).toHaveAttribute("data-active", "");
	});
});
