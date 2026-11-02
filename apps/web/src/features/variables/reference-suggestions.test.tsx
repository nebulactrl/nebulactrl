import { describe, expect, it } from "bun:test";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { stubApi } from "#/test/api";
import { make, problem } from "#/test/factories";
import { renderWithProviders } from "#/test/render";
import {
	buildCandidates,
	type ReferenceCandidate,
} from "./reference-autocomplete";
import {
	useReferenceCandidates,
	useReferenceInput,
} from "./reference-suggestions";

// The product's own reference syntax; `{{` is how user-event types one `{`.
const OPEN = "$" + "{{";
const TYPED_OPEN = "$" + "{{{{";

const CANDIDATES = buildCandidates(
	[
		{
			id: "db",
			slug: "postgres",
			name: "Postgres",
			variables: [{ key: "POSTGRES_PASSWORD", isSecret: true }],
			exports: [
				{ key: "DATABASE_URL", description: "Primary URL", secret: true },
			],
		},
		{ id: "cache", slug: "redis", name: "Redis", variables: [] },
	],
	"web",
	{
		own: [{ key: "SELF", isSecret: false }],
		shared: [{ key: "REGION", isSecret: false, layer: "project" }],
		team: [{ key: "ORG", isSecret: false }],
		vault: [{ name: "bao", kind: "vault" }],
		functions: true,
	},
);

function InputHarness({
	candidates = CANDIDATES,
	initial = "",
	multiline = false,
	onEscape,
}: {
	candidates?: readonly ReferenceCandidate[];
	initial?: string;
	multiline?: boolean;
	onEscape?: () => void;
}) {
	const [value, setValue] = useState(initial);
	const reference = useReferenceInput<HTMLInputElement>({
		value,
		onValueChange: setValue,
		candidates,
		placement: multiline ? "inside-bottom" : "below",
	});
	return (
		// biome-ignore lint/a11y/noStaticElementInteractions: records the Escape that must not leave the field
		<div
			className="relative"
			onKeyDown={(e) => e.key === "Escape" && onEscape?.()}
		>
			<input aria-label="Value" value={value} {...reference.fieldProps} />
			{reference.popup}
			<output aria-label="current">{value}</output>
		</div>
	);
}

const field = () => screen.getByLabelText("Value") as HTMLInputElement;
const options = () =>
	within(
		screen.getByRole("listbox", { name: "Variable references" }),
	).getAllByRole("option");
const active = () =>
	options().find((option) => option.getAttribute("aria-selected") === "true");

describe("useReferenceInput", () => {
	it("opens a listbox only while the caret is inside an unclosed reference", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<InputHarness />);
		await user.click(field());
		await user.type(field(), "plain text");
		expect(screen.queryByRole("listbox")).toBeNull();
		expect(field()).toHaveAttribute("aria-expanded", "false");
		expect(field()).not.toHaveAttribute("aria-controls");

		await user.type(field(), ` ${TYPED_OPEN}`);
		expect(
			await screen.findByRole("listbox", { name: "Variable references" }),
		).toBeInTheDocument();
		expect(field()).toHaveAttribute("aria-expanded", "true");
		expect(field()).toHaveAttribute(
			"aria-controls",
			screen.getByRole("listbox").id,
		);
		expect(field()).toHaveAttribute(
			"aria-activedescendant",
			options()[0]?.id ?? "",
		);

		// Closing the reference hides the list again.
		await user.type(field(), "x}}");
		expect(screen.queryByRole("listbox")).toBeNull();
	});

	it("narrows the list as the partial is typed and closes when nothing matches", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<InputHarness />);
		await user.click(field());
		await user.type(field(), `${TYPED_OPEN}postgres.`);
		const texts = options().map((option) => option.textContent ?? "");
		expect(texts.every((text) => text.startsWith("postgres."))).toBe(true);
		expect(texts.some((text) => text.includes("DATABASE_URL"))).toBe(true);

		await user.type(field(), "NOPE");
		expect(screen.queryByRole("listbox")).toBeNull();
		expect(field()).toHaveAttribute("aria-expanded", "false");
	});

	it("labels each row: description, secret flag and its kind", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<InputHarness />);
		await user.click(field());
		await user.type(field(), TYPED_OPEN);
		const list = within(screen.getByRole("listbox"));
		expect(list.getByText("Primary URL")).toBeInTheDocument();
		// A secret shows its lock label instead of a kind badge.
		expect(list.getAllByText("secret").length).toBeGreaterThanOrEqual(2);
		for (const kind of [
			"this service",
			"shared · project",
			"team",
			"secret manager",
			"function",
			"platform",
		]) {
			expect(list.getAllByText(kind).length).toBeGreaterThan(0);
		}
		// A plain service variable has no badge; a vault shows its scheme and a slash.
		expect(list.getByText("vault:")).toBeInTheDocument();
	});

	it("arrow keys move the highlight and wrap around", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<InputHarness />);
		await user.click(field());
		await user.type(field(), `${TYPED_OPEN}postgres.`);
		const count = options().length;
		expect(options()[0]).toHaveAttribute("aria-selected", "true");

		await user.keyboard("{ArrowDown}");
		expect(options()[1]).toHaveAttribute("aria-selected", "true");
		expect(field()).toHaveAttribute(
			"aria-activedescendant",
			options()[1]?.id ?? "",
		);
		await user.keyboard("{ArrowUp}{ArrowUp}");
		expect(options()[count - 1]).toHaveAttribute("aria-selected", "true");
		await user.keyboard("{ArrowDown}");
		expect(options()[0]).toHaveAttribute("aria-selected", "true");
	});

	it("hovering a row highlights it", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<InputHarness />);
		await user.click(field());
		await user.type(field(), `${TYPED_OPEN}postgres.`);
		fireEvent.mouseMove(options()[2] as HTMLElement);
		expect(active()).toBe(options()[2]);
	});

	it("Enter inserts the highlighted reference and keeps the caret right after it", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<InputHarness initial="a " />);
		await user.click(field());
		await user.keyboard("{End}");
		await user.type(field(), `${TYPED_OPEN}postgres.`);
		await user.keyboard("{ArrowDown}{Enter}");
		const chosen = CANDIDATES.filter((c) => c.slug === "postgres")[1];
		expect(chosen).toBeDefined();
		expect(field()).toHaveValue(`a ${chosen?.insert}`);
		expect(screen.queryByRole("listbox")).toBeNull();
		expect(field().selectionStart).toBe(`a ${chosen?.insert}`.length);
	});

	it("Tab inserts too, but Shift+Tab leaves the field alone", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<InputHarness />);
		await user.click(field());
		await user.type(field(), `${TYPED_OPEN}redis.`);
		await user.keyboard("{Shift>}{Tab}{/Shift}");
		expect(field()).toHaveValue(`${OPEN}redis.`);

		await user.click(field());
		await user.type(field(), "{ArrowDown}");
		await user.keyboard("{Tab}");
		expect(field().value.startsWith(`${OPEN}redis.`)).toBe(true);
		expect(field().value.endsWith("}}")).toBe(true);
	});

	it("inserting in the middle keeps the text after the caret", async () => {
		const user = userEvent.setup();
		const text = `x=${OPEN}red z`;
		const caret = text.indexOf(" z");
		await renderWithProviders(<InputHarness initial={text} />);
		await user.click(field());
		field().setSelectionRange(caret, caret);
		fireEvent.select(field());
		expect(await screen.findByRole("listbox")).toBeInTheDocument();
		await user.keyboard("{Enter}");
		expect(field().value.startsWith(`x=${OPEN}redis.`)).toBe(true);
		expect(field().value.endsWith("}} z")).toBe(true);
		expect(field().value).not.toContain("red z");
	});

	it("clicking a row inserts it without losing focus", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<InputHarness />);
		await user.click(field());
		await user.type(field(), `${TYPED_OPEN}team.`);
		await user.click(options()[0] as HTMLElement);
		expect(field()).toHaveValue(`${OPEN}team.ORG}}`);
		expect(field()).toHaveFocus();
		expect(screen.queryByRole("listbox")).toBeNull();
	});

	it("Escape closes the list but not the surrounding dialog, and stays closed for that reference", async () => {
		const user = userEvent.setup();
		let escapes = 0;
		await renderWithProviders(<InputHarness onEscape={() => escapes++} />);
		await user.click(field());
		await user.type(field(), `${TYPED_OPEN}postgres.`);
		await user.keyboard("{Escape}");
		expect(screen.queryByRole("listbox")).toBeNull();
		expect(escapes).toBe(0);

		// Typing on in the same reference does not bring it back...
		await user.type(field(), "P");
		expect(screen.queryByRole("listbox")).toBeNull();
		// ...but a new reference does.
		await user.type(field(), `}} ${TYPED_OPEN}redis.`);
		expect(await screen.findByRole("listbox")).toBeInTheDocument();
		// With the list closed, Escape is the surrounding dialog's again.
		await user.type(field(), "}}");
		await user.keyboard("{Escape}");
		expect(escapes).toBe(1);
	});

	it("blurring the field closes the list", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<InputHarness />);
		await user.click(field());
		await user.type(field(), TYPED_OPEN);
		expect(screen.getByRole("listbox")).toBeInTheDocument();
		await user.tab();
		expect(screen.queryByRole("listbox")).toBeNull();
	});

	it("docks the list inside a tall field, or below a single line", async () => {
		const user = userEvent.setup();
		const { unmount } = await renderWithProviders(<InputHarness multiline />);
		await user.click(field());
		await user.type(field(), TYPED_OPEN);
		const popup = document.querySelector('[data-slot="reference-suggestions"]');
		expect(popup?.className).toContain("bottom-2");
		unmount();
		await renderWithProviders(<InputHarness />);
		await user.click(field());
		await user.type(field(), TYPED_OPEN);
		expect(
			document.querySelector('[data-slot="reference-suggestions"]')?.className,
		).toContain("top-full");
	});

	it("does nothing on keys when no list is open", async () => {
		const user = userEvent.setup();
		await renderWithProviders(<InputHarness initial="hello" />);
		await user.click(field());
		await user.keyboard("{Enter}{ArrowDown}");
		expect(field()).toHaveValue("hello");
	});
});

function CandidatesProbe({
	enabled,
	serviceId = "web",
}: {
	enabled: boolean;
	serviceId?: string;
}) {
	const candidates = useReferenceCandidates("e1", serviceId, enabled);
	return (
		<ul aria-label="candidates">
			{candidates.map((c) => (
				<li
					key={c.insert}
				>{`${c.kind}|${c.insert}|${c.isSecret ? "secret" : "plain"}`}</li>
			))}
		</ul>
	);
}

const serviceView = (id: string, slug: string, name: string) =>
	make("ServiceView", {
		id,
		slug,
		name,
		environmentId: "e1",
		projectId: "p1",
		kind: "app",
		sourceType: "image",
	});

const variable = (key: string, computedSecret = false) =>
	make("Variable", {
		key,
		value: "v",
		isSecret: false,
		computedSecret,
	});

describe("useReferenceCandidates", () => {
	it("fetches nothing until enabled", async () => {
		const api = stubApi({});
		await renderWithProviders(<CandidatesProbe enabled={false} />);
		expect(api.calls).toEqual([]);
		expect(api.unmatched).toEqual([]);
		// Only the static functions: no service, variable or secret manager is known yet.
		const kinds = within(screen.getByRole("list", { name: "candidates" }))
			.getAllByRole("listitem")
			.map((li) => li.textContent?.split("|")[0]);
		expect(new Set(kinds)).toEqual(new Set(["function"]));
	});

	it("combines own keys, other services, shared, team, vault and functions", async () => {
		const api = stubApi({
			"GET /environments/e1/services": {
				body: make("PageServiceView", {
					items: [
						serviceView("web", "web", "Web"),
						serviceView("db", "postgres", "Postgres"),
					],
				}),
			},
			"GET /services/web/variables": {
				body: make("VariableList", { items: [variable("SELF")] }),
			},
			"GET /services/db/variables": {
				body: make("VariableList", {
					items: [variable("POSTGRES_PASSWORD", true)],
					exports: [
						{ key: "DATABASE_URL", description: "Primary", secret: true },
					],
				}),
			},
			"GET /environments/e1/shared-variables": {
				body: make("SharedVariableList", {
					items: [
						make("SharedVariableView", {
							key: "REGION",
							computedSecret: false,
							layer: "environment",
						}),
					],
				}),
			},
			"GET /team/shared-variables": {
				body: make("SharedVariableList", {
					items: [
						make("SharedVariableView", {
							key: "ORG",
							computedSecret: true,
							layer: "team",
						}),
					],
				}),
			},
			"GET /secret-managers": {
				body: make("ListOutputBody", {
					items: [
						make("SecretManager", { id: "m1", name: "bao", kind: "vault" }),
					],
				}),
			},
		});
		await renderWithProviders(<CandidatesProbe enabled />);
		const list = screen.getByRole("list", { name: "candidates" });
		await waitFor(() =>
			expect(
				within(list).queryByText(
					`export|${OPEN}postgres.DATABASE_URL}}|secret`,
				),
			).not.toBeNull(),
		);
		const texts = within(list)
			.getAllByRole("listitem")
			.map((li) => li.textContent);
		expect(texts).toContain(`own|${OPEN}SELF}}|plain`);
		expect(texts).toContain(`shared|${OPEN}shared.REGION}}|plain`);
		expect(texts).toContain(`team|${OPEN}team.ORG}}|secret`);
		expect(texts).toContain(`vault|${OPEN}vault:bao/|plain`);
		expect(texts).toContain(
			`variable|${OPEN}postgres.POSTGRES_PASSWORD}}|secret`,
		);
		expect(texts).toContain(
			`platform|${OPEN}postgres.NEBULA_PRIVATE_HOST}}|plain`,
		);
		expect(texts.some((t) => t?.startsWith("function|"))).toBe(true);
		// The service being edited is not offered as another service.
		expect(texts.some((t) => t?.includes(`${OPEN}web.`))).toBe(false);
		// Names only: the variables of the other service were asked once each.
		expect(
			api.calls.filter((c) => c.path === "/services/db/variables"),
		).toHaveLength(1);
		expect(api.unmatched).toEqual([]);
	});

	it("still offers everything else when the team list and managers are not allowed", async () => {
		stubApi({
			"GET /environments/e1/services": {
				body: make("PageServiceView", {
					items: [serviceView("web", "web", "Web")],
				}),
			},
			"GET /services/web/variables": {
				body: make("VariableList", { items: [variable("SELF")] }),
			},
			"GET /environments/e1/shared-variables": {
				body: make("SharedVariableList", {
					items: [
						make("SharedVariableView", {
							key: "REGION",
							computedSecret: false,
							layer: "project",
						}),
					],
				}),
			},
			"GET /team/shared-variables": problem(403, {
				code: "forbidden",
				title: "Forbidden",
				detail: "no team access",
			}),
			"GET /secret-managers": problem(403, {
				code: "forbidden",
				title: "Forbidden",
				detail: "no access",
			}),
		});
		await renderWithProviders(<CandidatesProbe enabled />);
		const list = screen.getByRole("list", { name: "candidates" });
		await waitFor(() =>
			expect(
				within(list).queryByText(`shared|${OPEN}shared.REGION}}|plain`),
			).not.toBeNull(),
		);
		const texts = within(list)
			.getAllByRole("listitem")
			.map((li) => li.textContent ?? "");
		expect(texts.some((t) => t.startsWith("team|"))).toBe(false);
		expect(texts.some((t) => t.startsWith("vault|"))).toBe(false);
		expect(texts).toContain(`own|${OPEN}SELF}}|plain`);
	});
});
