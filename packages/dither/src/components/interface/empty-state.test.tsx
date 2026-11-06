import { describe, expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import { DitherEmptyState } from "./empty-state";
import { ILLUSTRATIONS } from "./illustration";

const root = () =>
	document.querySelector("[data-slot=empty-state]") as HTMLElement;
const art = () =>
	document.querySelector<SVGElement>("[data-slot=dither-illustration]");

describe("DitherEmptyState text", () => {
	test("the title is a level-3 heading by default and titleAs changes the level", () => {
		const { unmount } = render(
			<DitherEmptyState title="Add your first server" />,
		);
		expect(
			screen.getByRole("heading", { level: 3, name: "Add your first server" }),
		).toBeInTheDocument();
		unmount();
		render(<DitherEmptyState title="Nothing" titleAs="h2" />);
		expect(
			screen.getByRole("heading", { level: 2, name: "Nothing" }),
		).toBeInTheDocument();
	});

	test("body and description both render the sentence, description winning", () => {
		const { unmount } = render(<DitherEmptyState title="t" body="from body" />);
		expect(screen.getByText("from body")).toHaveAttribute(
			"data-slot",
			"empty-description",
		);
		unmount();
		render(
			<DitherEmptyState
				title="t"
				body="from body"
				description="from description"
			/>,
		);
		expect(screen.getByText("from description")).toBeInTheDocument();
		expect(screen.queryByText("from body")).toBeNull();
	});

	test("with no body there is no description element", () => {
		render(<DitherEmptyState title="t" />);
		expect(root().querySelector("[data-slot=empty-description]")).toBeNull();
	});

	test("actions render in order inside one row; no row without actions", () => {
		const { unmount } = render(
			<DitherEmptyState
				title="t"
				action={<button type="button">Add</button>}
				secondaryAction={<button type="button">Import</button>}
			/>,
		);
		const content = root().querySelector(
			"[data-slot=empty-content]",
		) as HTMLElement;
		expect(
			Array.from(content.querySelectorAll("button"), (b) => b.textContent),
		).toEqual(["Add", "Import"]);
		unmount();
		render(<DitherEmptyState title="t" />);
		expect(root().querySelector("[data-slot=empty-content]")).toBeNull();
	});

	test("a secondary action alone still gets the row", () => {
		render(
			<DitherEmptyState
				title="t"
				secondaryAction={<button type="button">Docs</button>}
			/>,
		);
		expect(screen.getByRole("button", { name: "Docs" })).toBeInTheDocument();
	});

	test("hint shows under the actions", () => {
		render(<DitherEmptyState title="t" hint={<span>press N</span>} />);
		expect(screen.getByText("press N")).toBeInTheDocument();
	});
});

describe("DitherEmptyState object", () => {
	test("no illustration, media or icon means no object at all", () => {
		render(<DitherEmptyState title="t" />);
		expect(root().querySelector("[data-slot=empty-icon]")).toBeNull();
		expect(art()).toBeNull();
	});

	test("every named illustration draws its own decorative svg", () => {
		const seen = new Set<string>();
		for (const name of ILLUSTRATIONS) {
			const { unmount } = render(
				<DitherEmptyState title="t" illustration={name} />,
			);
			const svg = art() as SVGElement;
			expect(svg).toHaveAttribute("data-illustration", name);
			expect(svg).toHaveAttribute("aria-hidden", "true");
			const paths = svg.querySelectorAll("path");
			expect(paths).toHaveLength(2);
			seen.add(paths[1]?.getAttribute("d") ?? "");
			unmount();
		}
		// Each object is a different shape.
		expect(seen.size).toBe(ILLUSTRATIONS.length);
	});

	test("the illustration is tinted by color and the matrix changes the cells", () => {
		const { unmount } = render(
			<DitherEmptyState title="t" illustration="box" color="blue" />,
		);
		expect(art()?.style.color).toBe("var(--dither-blue)");
		const bayer = art()?.querySelectorAll("path")[1]?.getAttribute("d");
		unmount();
		render(
			<DitherEmptyState title="t" illustration="box" matrix="blue-noise" />,
		);
		expect(art()?.style.color).toBe("var(--dither-ink)");
		expect(art()?.querySelectorAll("path")[1]?.getAttribute("d")).not.toBe(
			bayer,
		);
	});

	test("animate bobs the object; animate={false} holds it still", () => {
		const { unmount } = render(
			<DitherEmptyState title="t" illustration="box" />,
		);
		expect(art()?.querySelector("g")).toHaveClass("nc-if-bob");
		unmount();
		render(<DitherEmptyState title="t" illustration="box" animate={false} />);
		expect(art()?.querySelector("g")).not.toHaveClass("nc-if-bob");
	});

	test("lg is 336px on a 168×108 grid; md is 224px on 112×72", () => {
		const { unmount } = render(
			<DitherEmptyState title="t" illustration="box" />,
		);
		expect(art()?.parentElement).toHaveClass("max-w-[336px]");
		expect(art()).toHaveAttribute("viewBox", "0 0 168 108");
		unmount();
		render(<DitherEmptyState title="t" illustration="box" size="md" />);
		expect(art()?.parentElement).toHaveClass("max-w-[224px]");
		expect(art()).toHaveAttribute("viewBox", "0 0 112 72");
	});

	test("the keyframes for the bob are on the page", () => {
		render(<DitherEmptyState title="t" illustration="box" />);
		expect(
			document.head.querySelector("style[data-nc-interface-keyframes]")
				?.textContent,
		).toContain("nc-if-bob");
	});

	test("media wins over icon; the illustration wins over both", () => {
		const { unmount } = render(
			<DitherEmptyState
				title="t"
				media={<i data-testid="media" />}
				icon={<i data-testid="icon" />}
			/>,
		);
		expect(screen.getByTestId("media")).toBeInTheDocument();
		expect(screen.queryByTestId("icon")).toBeNull();
		unmount();
		render(
			<DitherEmptyState
				title="t"
				illustration="key"
				media={<i data-testid="media" />}
				icon={<i data-testid="icon" />}
			/>,
		);
		expect(art()).not.toBeNull();
		expect(screen.queryByTestId("media")).toBeNull();
		expect(screen.queryByTestId("icon")).toBeNull();
	});

	test("an icon sits in the engraved tile", () => {
		render(<DitherEmptyState title="t" icon={<i data-testid="icon" />} />);
		expect(screen.getByTestId("icon").parentElement).toHaveAttribute(
			"data-variant",
			"icon",
		);
	});

	test("an unknown illustration name falls back to media or icon", () => {
		render(
			<DitherEmptyState
				title="t"
				// @ts-expect-error not a real illustration
				illustration="spaceship"
				icon={<i data-testid="icon" />}
			/>,
		);
		expect(art()).toBeNull();
		expect(screen.getByTestId("icon")).toBeInTheDocument();
	});
});

describe("DitherEmptyState frame and backdrop", () => {
	test("raised is the default frame, dashed outlines it, none removes it", () => {
		const { unmount } = render(<DitherEmptyState title="t" />);
		expect(root()).toHaveClass("surface-raised");
		unmount();
		const dashed = render(<DitherEmptyState title="t" frame="dashed" />);
		expect(root()).toHaveClass("border-dashed");
		expect(root()).not.toHaveClass("surface-raised");
		dashed.unmount();
		render(<DitherEmptyState title="t" frame="none" />);
		expect(root()).not.toHaveClass("surface-raised");
		expect(root()).not.toHaveClass("border-dashed");
	});

	test("the dither backdrop is on by default; backdrop={false} is the plain look", () => {
		const { unmount } = render(<DitherEmptyState title="t" />);
		expect(root().querySelector("[data-slot=dither-shader]")).not.toBeNull();
		expect(root()).toHaveClass("overflow-hidden");
		unmount();
		render(<DitherEmptyState title="t" backdrop={false} />);
		expect(root().querySelector("[data-slot=dither-shader]")).toBeNull();
		expect(root()).not.toHaveClass("overflow-hidden");
	});

	test("className is merged onto the root", () => {
		render(<DitherEmptyState title="t" className="min-h-64" />);
		expect(root()).toHaveClass("min-h-64");
	});
});
