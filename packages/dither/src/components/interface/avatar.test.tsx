import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { render, screen, waitFor } from "@testing-library/react";
import { avatarPx, DitherAvatar, DitherAvatarGroup } from "./avatar";
import { faceFor, faceRuns } from "./face";

const avatar = (name: string) => screen.getByRole("img", { name });
const runsOf = (el: Element) =>
	Array.from(el.querySelectorAll("svg[viewBox] rect"), (r) => [
		r.getAttribute("x"),
		r.getAttribute("y"),
		r.getAttribute("width"),
	]);

describe("avatarPx", () => {
	test("named sizes and numbers", () => {
		expect(avatarPx("sm")).toBe(22);
		expect(avatarPx("default")).toBe(32);
		expect(avatarPx("lg")).toBe(40);
		expect(avatarPx(57)).toBe(57);
	});
});

describe("DitherAvatar face", () => {
	test("is an image named after the person and draws the generated face", () => {
		render(<DitherAvatar name="Alan Turing" />);
		const el = avatar("Alan Turing");
		expect(el).toHaveAttribute("data-slot", "avatar");
		expect(el.querySelector("[data-slot=dither-avatar-face]")).not.toBeNull();
	});

	test("the face features are the name's own cells, so a name is the same face every time", () => {
		render(
			<>
				<DitherAvatar name="Ada Lovelace" />
				<DitherAvatar name="  ada lovelace " label="Ada again" />
			</>,
		);
		const expected = faceRuns(faceFor("Ada Lovelace").bits).map((run) => [
			String(run.x),
			String(run.y),
			String(run.w),
		]);
		expect(expected.length).toBeGreaterThan(0);
		expect(runsOf(avatar("Ada Lovelace"))).toEqual(expected);
		expect(runsOf(avatar("Ada again"))).toEqual(expected);
	});

	test("different names get different faces", () => {
		const names = [
			"Grace Hopper",
			"Linus",
			"Margaret Hamilton",
			"Dennis",
			"Ken",
		];
		render(
			<>
				{names.map((name) => (
					<DitherAvatar key={name} name={name} />
				))}
			</>,
		);
		const faces = new Set(
			names.map((name) => JSON.stringify(runsOf(avatar(name)))),
		);
		expect(faces.size).toBeGreaterThan(1);
	});

	test("label replaces the name as the accessible name", () => {
		render(<DitherAvatar name="u_3412" label="Alan Turing" />);
		expect(
			screen.getByRole("img", { name: "Alan Turing" }),
		).toBeInTheDocument();
		expect(screen.queryByRole("img", { name: "u_3412" })).toBeNull();
	});

	test("the dithered light and shade layers come from gradient, dotted and hatched patterns; solid is flat", () => {
		render(
			<>
				<DitherAvatar name="g" fill="gradient" />
				<DitherAvatar name="d" fill="dotted" />
				<DitherAvatar name="h" fill="hatched" />
				<DitherAvatar name="s" fill="solid" />
			</>,
		);
		const bands = (name: string) =>
			Array.from(avatar(name).querySelectorAll("svg[data-slot] > rect")).slice(
				1,
			);
		for (const name of ["g", "d", "h"]) {
			const fills = bands(name).map((r) => r.getAttribute("fill"));
			expect(fills).toHaveLength(4);
			for (const fill of fills) expect(fill).toMatch(/^url\(#nc-dither-/);
		}
		// The shape of a dotted fill differs from a gradient's square cells.
		expect(bands("d").map((r) => r.getAttribute("fill"))).not.toEqual(
			bands("g").map((r) => r.getAttribute("fill")),
		);
		const solid = bands("s");
		for (const r of solid) {
			expect(r.getAttribute("fill")).toBeNull();
			expect((r as SVGRectElement).style.fillOpacity).not.toBe("");
		}
	});

	test("color overrides the hue picked from the name", () => {
		render(
			<>
				<DitherAvatar name="Same" />
				<DitherAvatar name="Same" label="tinted" color="--brand" ring />
			</>,
		);
		expect(avatar("tinted").style.boxShadow).toContain("var(--brand)");
		expect(avatar("Same").style.boxShadow).toBe("");
	});
});

describe("DitherAvatar sizing", () => {
	test("without a size it follows --avatar-size, 32px if unset", () => {
		render(<DitherAvatar name="x" />);
		const el = avatar("x");
		expect(el).toHaveAttribute("data-size", "default");
		expect(el.style.width).toBe("var(--avatar-size, 2rem)");
		expect(el.style.height).toBe("var(--avatar-size, 2rem)");
	});

	test("named sizes use the primitive's own size and no inline dimensions", () => {
		render(
			<>
				<DitherAvatar name="s" size="sm" />
				<DitherAvatar name="l" size="lg" />
			</>,
		);
		expect(avatar("s")).toHaveAttribute("data-size", "sm");
		expect(avatar("s").style.width).toBe("");
		expect(avatar("l")).toHaveAttribute("data-size", "lg");
	});

	test("a pixel size sets the diameter and picks the nearest named size", () => {
		render(
			<>
				<DitherAvatar name="a" size={24} />
				<DitherAvatar name="b" size={30} />
				<DitherAvatar name="c" size={38} />
				<DitherAvatar name="d" size={64} />
			</>,
		);
		expect(avatar("a")).toHaveAttribute("data-size", "sm");
		expect(avatar("b")).toHaveAttribute("data-size", "default");
		expect(avatar("c")).toHaveAttribute("data-size", "lg");
		expect(avatar("d")).toHaveAttribute("data-size", "lg");
		expect(avatar("d").style.width).toBe("64px");
		expect(avatar("d").style.height).toBe("64px");
	});
});

describe("DitherAvatar decorations", () => {
	test("shape=square rounds the corners instead of a circle", () => {
		render(
			<>
				<DitherAvatar name="sq" shape="square" />
				<DitherAvatar name="ci" />
			</>,
		);
		expect(avatar("sq")).toHaveClass("rounded-[22%]");
		expect(avatar("ci")).not.toHaveClass("rounded-[22%]");
	});

	test("ring cuts a 2px gap and draws a 4px ring in the avatar's colour", () => {
		render(<DitherAvatar name="r" ring color="red" />);
		expect(avatar("r").style.boxShadow).toBe(
			"0 0 0 2px var(--color-surface-raised), 0 0 0 4px var(--dither-red)",
		);
	});

	test("bloom adds a glow to the shadow, after the ring", () => {
		render(<DitherAvatar name="b" ring bloom="high" color="blue" />);
		const shadow = avatar("b").style.boxShadow;
		expect(shadow).toContain("0 0 0 4px var(--dither-blue)");
		expect(shadow).toContain("22px");
		expect(shadow.indexOf("4px")).toBeLessThan(shadow.indexOf("22px"));
	});

	test("caller style wins over the computed one", () => {
		render(<DitherAvatar name="s" size={50} style={{ width: 10 }} />);
		expect(avatar("s").style.width).toBe("10px");
		expect(avatar("s").style.height).toBe("50px");
	});

	test("status adds a dot and is spoken with the name", () => {
		render(<DitherAvatar name="Alan Turing" status="busy" />);
		const el = screen.getByRole("img", { name: "Alan Turing, busy" });
		const dot = el.querySelector<HTMLElement>("[data-slot=avatar-status]");
		expect(dot).toHaveAttribute("data-status", "busy");
		expect(dot).toHaveAttribute("aria-hidden", "true");
		expect(dot?.style.backgroundColor).toBe("var(--dither-red)");
	});

	test("status is appended to a custom label too", () => {
		render(<DitherAvatar name="u1" label="Alan" status="online" />);
		expect(
			screen.getByRole("img", { name: "Alan, online" }),
		).toBeInTheDocument();
	});

	test("each status has its own colour; offline is a hollow dot", () => {
		render(
			<>
				<DitherAvatar name="on" status="online" />
				<DitherAvatar name="aw" status="away" />
				<DitherAvatar name="off" status="offline" />
			</>,
		);
		const dot = (name: string) =>
			screen
				.getByRole("img", {
					name: `${name}, ${{ on: "online", aw: "away", off: "offline" }[name]}`,
				})
				.querySelector<HTMLElement>("[data-slot=avatar-status]") as HTMLElement;
		expect(dot("on").style.backgroundColor).toBe("var(--dither-green)");
		expect(dot("aw").style.backgroundColor).toBe("var(--dither-orange)");
		expect(dot("off").style.backgroundColor).toBe("var(--color-surface-base)");
		expect(dot("off").style.boxShadow).toContain(
			"inset 0 0 0 2px var(--dither-grey)",
		);
	});

	test("the dot scales with the avatar but never drops below 8px", () => {
		render(
			<>
				<DitherAvatar name="big" size={100} status="online" />
				<DitherAvatar name="tiny" size={20} status="online" />
			</>,
		);
		const dot = (name: string) =>
			screen
				.getByRole("img", { name: `${name}, online` })
				.querySelector<HTMLElement>("[data-slot=avatar-status]") as HTMLElement;
		expect(dot("big").style.width).toBe("26px");
		expect(dot("tiny").style.width).toBe("8px");
		expect(dot("tiny").style.height).toBe("8px");
	});

	test("a square avatar seats its dot in the corner; a circle pulls it inwards", () => {
		render(
			<>
				<DitherAvatar name="sq" shape="square" status="online" />
				<DitherAvatar name="ci" status="online" />
			</>,
		);
		const dot = (name: string) =>
			screen
				.getByRole("img", { name: `${name}, online` })
				.querySelector("[data-slot=avatar-status]");
		expect(dot("sq")).toHaveClass("right-0", "bottom-0");
		expect(dot("ci")).toHaveClass("right-[2%]", "bottom-[2%]");
	});

	test("no status, no dot", () => {
		render(<DitherAvatar name="x" />);
		expect(avatar("x").querySelector("[data-slot=avatar-status]")).toBeNull();
	});
});

describe("DitherAvatar photo", () => {
	const RealImage = window.Image;
	let outcome: "load" | "error" = "load";

	beforeEach(() => {
		// happy-dom does not fetch images: settle the load the way a browser would.
		class FakeImage {
			onload: (() => void) | null = null;
			onerror: (() => void) | null = null;
			crossOrigin: string | null = null;
			referrerPolicy = "";
			set src(_value: string) {
				queueMicrotask(() =>
					outcome === "load" ? this.onload?.() : this.onerror?.(),
				);
			}
		}
		window.Image = FakeImage as unknown as typeof Image;
	});
	afterEach(() => {
		window.Image = RealImage;
		outcome = "load";
	});

	test("a loaded photo replaces the generated face", async () => {
		render(<DitherAvatar name="Photo" src="/me.png" />);
		const el = avatar("Photo");
		await waitFor(() => expect(el.querySelector("img")).not.toBeNull());
		expect(el.querySelector("img")).toHaveAttribute("src", "/me.png");
		expect(el.querySelector("[data-slot=dither-avatar-face]")).toBeNull();
	});

	test("a photo that fails keeps the generated face", async () => {
		outcome = "error";
		render(<DitherAvatar name="Broken" src="/missing.png" />);
		await screen.findByRole("img", { name: "Broken" });
		expect(
			avatar("Broken").querySelector("[data-slot=dither-avatar-face]"),
		).not.toBeNull();
		expect(avatar("Broken").querySelector("img")).toBeNull();
	});
});

describe("DitherAvatarGroup", () => {
	const people = [
		{ name: "Ada" },
		{ name: "Grace" },
		{ name: "Linus" },
		{ name: "Ken" },
		{ name: "Dennis" },
		{ name: "Bjarne" },
	];

	test("shows four people by default and counts the rest", () => {
		render(<DitherAvatarGroup people={people} />);
		const group = screen.getByRole("group");
		expect(group.querySelectorAll("[data-slot=avatar]")).toHaveLength(4);
		const more = screen.getByRole("img", { name: "2 more: Dennis, Bjarne" });
		expect(more).toHaveTextContent("+2");
		expect(more).toHaveAttribute("title", "Dennis, Bjarne");
	});

	test("max limits the faces, max=0 shows only the count", () => {
		const { unmount } = render(<DitherAvatarGroup people={people} max={2} />);
		expect(
			screen.getByRole("group").querySelectorAll("[data-slot=avatar]"),
		).toHaveLength(2);
		expect(screen.getByText("+4")).toBeInTheDocument();
		unmount();
		render(<DitherAvatarGroup people={people} max={0} />);
		expect(
			screen.getByRole("group").querySelectorAll("[data-slot=avatar]"),
		).toHaveLength(0);
		expect(screen.getByText("+6")).toBeInTheDocument();
	});

	test("no count chip when everyone fits", () => {
		render(<DitherAvatarGroup people={people.slice(0, 3)} />);
		expect(
			screen.getByRole("group").querySelector("[data-slot=avatar-group-count]"),
		).toBeNull();
	});

	test("moreLabel replaces the announcement", () => {
		render(
			<DitherAvatarGroup
				people={people}
				moreLabel={(n) => `and ${n} teammates`}
			/>,
		);
		expect(
			screen.getByRole("img", { name: "and 2 teammates" }),
		).toBeInTheDocument();
	});

	test("the chip announces a person's label rather than their name", () => {
		render(
			<DitherAvatarGroup
				max={1}
				people={[{ name: "a" }, { name: "u_77", label: "Linus Torvalds" }]}
			/>,
		);
		expect(
			screen.getByRole("img", { name: "1 more: Linus Torvalds" }),
		).toBeInTheDocument();
	});

	test("children work as an alternative to people, and show all by default", () => {
		render(
			<DitherAvatarGroup>
				<DitherAvatar name="One" />
				<DitherAvatar name="Two" />
				<DitherAvatar name="Three" />
				<DitherAvatar name="Four" />
				<DitherAvatar name="Five" />
			</DitherAvatarGroup>,
		);
		expect(
			screen.getByRole("group").querySelectorAll("[data-slot=avatar]"),
		).toHaveLength(5);
		expect(
			screen.getByRole("group").querySelector("[data-slot=avatar-group-count]"),
		).toBeNull();
	});

	test("children past max are named in the chip", () => {
		render(
			<DitherAvatarGroup max={1}>
				<DitherAvatar name="One" />
				<DitherAvatar name="Two" label="Second" />
				<DitherAvatar name="Three" />
			</DitherAvatarGroup>,
		);
		expect(
			screen.getByRole("img", { name: "2 more: Second, Three" }),
		).toBeInTheDocument();
	});

	test("a hidden non-avatar child is counted but unnamed", () => {
		render(
			<DitherAvatarGroup max={1}>
				<DitherAvatar name="One" />
				<span>custom</span>
			</DitherAvatarGroup>,
		);
		const chip = screen.getByText("+1");
		expect(chip).toHaveAttribute("aria-label", "1 more: ");
		expect(chip).not.toHaveAttribute("title");
	});

	test("people and children combine, people first", () => {
		render(
			<DitherAvatarGroup people={[{ name: "P" }]}>
				<DitherAvatar name="C" />
			</DitherAvatarGroup>,
		);
		const names = Array.from(
			screen.getByRole("group").querySelectorAll("[data-slot=avatar]"),
			(el) => el.getAttribute("aria-label"),
		);
		expect(names).toEqual(["P", "C"]);
	});

	test("people with the same name each get a face", () => {
		render(<DitherAvatarGroup people={[{ name: "Sam" }, { name: "Sam" }]} />);
		expect(screen.getAllByRole("img", { name: "Sam" })).toHaveLength(2);
	});

	test("person status, colour and fill reach their avatar", () => {
		render(
			<DitherAvatarGroup
				people={[{ name: "Sam", status: "away", fill: "solid", color: "red" }]}
			/>,
		);
		const el = screen.getByRole("img", { name: "Sam, away" });
		expect(el.querySelector("[data-status=away]")).not.toBeNull();
		expect(
			el.querySelector("svg[data-slot] > rect:nth-child(2)"),
		).toHaveAttribute("style", expect.stringContaining("var(--dither-red)"));
	});

	test("the group's size reaches its avatars and sizes the chip", () => {
		render(<DitherAvatarGroup people={people} size={50} max={1} />);
		const group = screen.getByRole("group");
		expect(
			(group.querySelector("[data-slot=avatar]") as HTMLElement).style.width,
		).toBe("50px");
		const chip = screen.getByText("+5");
		expect(chip.style.width).toBe("50px");
		expect(chip.style.height).toBe("50px");
		expect(chip.style.fontSize).toBe("17px");
	});

	test("an avatar's own size beats the group's", () => {
		render(
			<DitherAvatarGroup size="lg">
				<DitherAvatar name="own" size="sm" />
				<DitherAvatar name="inherits" />
			</DitherAvatarGroup>,
		);
		expect(avatar("own")).toHaveAttribute("data-size", "sm");
		expect(avatar("inherits")).toHaveAttribute("data-size", "lg");
	});

	test("outline colours the gap around each avatar and the chip", () => {
		render(<DitherAvatarGroup people={people} outline="#101010" max={1} />);
		expect(
			(
				screen
					.getByRole("group")
					.querySelector("[data-slot=avatar]") as HTMLElement
			).style.boxShadow,
		).toBe("0 0 0 2px #101010");
		expect(screen.getByText("+5").style.boxShadow).toBe("0 0 0 2px #101010");
	});

	test("the chip's text never goes below 9px", () => {
		render(<DitherAvatarGroup people={people} size={20} max={1} />);
		expect(screen.getByText("+5").style.fontSize).toBe("9px");
	});

	test("className and props reach the group element", () => {
		render(
			<DitherAvatarGroup people={people} className="mine" aria-label="Team" />,
		);
		expect(screen.getByRole("group", { name: "Team" })).toHaveClass("mine");
	});
});
