import { describe, expect, test } from "bun:test";
import { render } from "@testing-library/react";
import { BRAND_ICONS } from "#/assets/brand-icons";
import { ServiceIcon, type ServiceIconSource } from "./service-icon";

/** The mark a source resolves to: a brand slug from the sprite reference, or the generic glyph's lucide class. */
function mark(service: ServiceIconSource, label = "icon") {
	const { container } = render(<ServiceIcon service={service} label={label} />);
	const tile = container.querySelector<HTMLElement>(
		'[data-slot="service-icon"]',
	);
	if (!tile) throw new Error("no tile");
	const use = tile.querySelector("use");
	const brand = use?.getAttribute("href")?.split("#brand-")[1] ?? null;
	const lucide = tile.querySelector("svg")?.getAttribute("class") ?? "";
	return { tile, brand, lucide };
}

describe("ServiceIcon: which mark a service wears", () => {
	test.each([
		[
			"a managed postgres engine",
			{ kind: "database", config: { database: { engine: "postgres" } } },
			"postgresql",
		],
		[
			"a managed redis engine",
			{ kind: "database", config: { database: { engine: "redis" } } },
			"redis",
		],
		[
			"a clickhouse engine",
			{ kind: "database", config: { database: { engine: "clickhouse" } } },
			"clickhouse",
		],
		[
			"an image name alias (postgres)",
			{ config: { image: "postgres:16" } },
			"postgresql",
		],
		[
			"a registry-qualified image (ghcr.io/org/redis)",
			{ config: { image: "ghcr.io/org/redis:7" } },
			"redis",
		],
		[
			"a localhost registry with port",
			{ config: { image: "localhost:5000/nginx:1.27" } },
			"nginx",
		],
		[
			"an image pinned by digest",
			{ config: { image: "node@sha256:abc" } },
			"nodedotjs",
		],
		[
			"a namespace that is the brand (mcr.microsoft.com/dotnet/aspnet)",
			{ config: { image: "mcr.microsoft.com/dotnet/aspnet:8" } },
			"dotnet",
		],
		[
			"an upper-case image name",
			{ config: { image: "Grafana/Grafana:11" } },
			"grafana",
		],
		[
			"a github https url",
			{
				sourceType: "git",
				config: { gitUrl: "https://github.com/acme/app.git" },
			},
			"github",
		],
		[
			"a github scp-style url",
			{ sourceType: "git", config: { gitUrl: "git@github.com:acme/app.git" } },
			"github",
		],
		[
			"a gitlab subdomain",
			{ sourceType: "git", config: { gitUrl: "https://git.gitlab.com/a/b" } },
			"gitlab",
		],
		[
			"bitbucket",
			{ sourceType: "git", config: { gitUrl: "https://bitbucket.org/a/b" } },
			"bitbucket",
		],
	] as Array<[string, ServiceIconSource, string]>)(
		"%s",
		(_name, service, slug) => {
			expect(mark(service).brand).toBe(slug);
		},
	);

	test("a brand mark takes the brand's tile colour and glyph colour", () => {
		const { tile } = mark({ config: { image: "redis:7" } });
		expect(tile.style.getPropertyValue("--t")).toBe(BRAND_ICONS.redis.tile);
		expect(tile.style.color).not.toBe("");
	});

	test("the database engine wins over the image", () => {
		expect(
			mark({
				kind: "database",
				config: { image: "mysql:8", database: { engine: "postgres" } },
			}).brand,
		).toBe("postgresql");
	});

	test("an unknown image falls back to the Docker image glyph, not a brand", () => {
		const { brand, tile } = mark({
			sourceType: "image",
			config: { image: "acme/internal-tool:1" },
		});
		expect(brand).toBeNull();
		expect(tile.querySelector("svg")?.getAttribute("class")).toContain(
			"lucide-container",
		);
	});

	test("an unrecognised git host falls back to the git glyph", () => {
		const { brand, lucide } = mark({
			sourceType: "git",
			config: { gitUrl: "https://git.example.com/a/b" },
		});
		expect(brand).toBeNull();
		expect(lucide).toContain("lucide-git-branch");
	});

	test("an unparseable git url does not throw and falls back to the git glyph", () => {
		const { brand, lucide } = mark({
			sourceType: "git",
			config: { gitUrl: "not a url" },
		});
		expect(brand).toBeNull();
		expect(lucide).toContain("lucide-git-branch");
	});

	test("a Valkey database (by engine or by image) wears the key glyph, not the generic database", () => {
		expect(
			mark({ kind: "database", config: { database: { engine: "valkey" } } })
				.lucide,
		).toContain("lucide-key-round");
		expect(
			mark({ kind: "database", config: { image: "valkey/valkey:8" } }).lucide,
		).toContain("lucide-key-round");
	});

	test("a database with no known engine wears the database glyph", () => {
		expect(
			mark({ kind: "database", config: { database: { engine: "custom" } } })
				.lucide,
		).toContain("lucide-database");
	});

	test("a compose stack wears the stack glyph", () => {
		expect(mark({ kind: "stack" }).lucide).toContain("lucide-boxes");
	});

	test("a route is a globe for http and a network for tcp/udp", () => {
		expect(
			mark({ kind: "route", config: { route: { protocol: "http" } } }).lucide,
		).toContain("lucide-globe");
		expect(
			mark({ kind: "route", config: { route: { protocol: "tcp" } } }).lucide,
		).toContain("lucide-network");
		expect(
			mark({ kind: "route", config: { route: { protocol: "udp" } } }).lucide,
		).toContain("lucide-network");
	});

	test("a worker wears the settings glyph", () => {
		expect(mark({ kind: "worker" }).lucide).toContain("lucide-settings");
	});

	test("a service with nothing configured wears the empty box", () => {
		expect(mark({}).lucide).toContain("lucide-box");
	});
});

describe("ServiceIcon: sizing and accessibility", () => {
	test("is decorative without a label", () => {
		const { container } = render(<ServiceIcon service={{ kind: "worker" }} />);
		const tile = container.firstElementChild;
		expect(tile).toHaveAttribute("aria-hidden", "true");
		expect(tile).not.toHaveAttribute("role");
	});

	test("with a label it is an image with that name", () => {
		const { getByRole } = render(
			<ServiceIcon service={{ kind: "worker" }} label="Worker" />,
		);
		expect(getByRole("img", { name: "Worker" })).toBeInTheDocument();
	});

	test("scales the tile, its corner radius and its glyph with `size`", () => {
		const { container } = render(
			<ServiceIcon service={{ kind: "worker" }} size={40} className="extra" />,
		);
		const tile = container.firstElementChild as HTMLElement;
		expect(tile.style.width).toBe("40px");
		expect(tile.style.height).toBe("40px");
		expect(tile.style.borderRadius).toBe("12px");
		expect(tile).toHaveClass("extra");
		expect(tile.querySelector("svg")).toHaveAttribute("width", "23");
	});

	test("defaults to a 28px tile", () => {
		const { container } = render(<ServiceIcon service={{ kind: "worker" }} />);
		expect((container.firstElementChild as HTMLElement).style.width).toBe(
			"28px",
		);
	});
});
