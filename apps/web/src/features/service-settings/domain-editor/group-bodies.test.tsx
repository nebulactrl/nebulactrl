import { describe, expect, it } from "bun:test";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type ReactNode, useState } from "react";
import type { RouteOptions, ServerCertificate } from "#/api/gen/types.gen";
import {
	type AuthRow,
	validateAuthRows,
} from "#/features/proxy/lib/auth-users";
import {
	type FormErrors,
	formToOptions,
	type OptionGroup,
	type OptionsForm,
	toForm,
	validateGroup,
} from "#/features/proxy/lib/options";
import { renderWithProviders } from "#/test/render";
import {
	AccessBody,
	HeadersBody,
	LimitsBody,
	RedirectsBody,
	TlsBody,
} from "./group-bodies";

/** What the host component currently holds, readable from the test. */
interface Seen {
	form: OptionsForm;
	options: RouteOptions;
	rows: AuthRow[];
	errors: FormErrors;
}

interface HostApi {
	form: OptionsForm;
	set(patch: Partial<OptionsForm>): void;
	errors: FormErrors;
	rows: AuthRow[];
	setRows(next: AuthRow[]): void;
}

/**
 * A stand-in for DomainOptions: keeps the form and basic-auth rows in state
 * and computes the group's errors the way the real editor does.
 */
function Host({
	group,
	options = {},
	users = [],
	seen,
	children,
}: {
	group: OptionGroup;
	options?: RouteOptions;
	users?: string[];
	seen: Seen;
	children(api: HostApi): ReactNode;
}) {
	const [form, setForm] = useState(() => toForm(options));
	const [rows, setRows] = useState<AuthRow[]>(() =>
		users.map((username) => ({ username, password: "", existing: true })),
	);
	const errors: FormErrors = {
		...validateGroup(form, group, { authUsers: rows.length }),
		...(group === "access" ? validateAuthRows(rows) : {}),
	};
	seen.form = form;
	seen.options = formToOptions(form);
	seen.rows = rows;
	seen.errors = errors;
	return (
		<>
			{children({
				form,
				set: (patch) => setForm((current) => ({ ...current, ...patch })),
				errors,
				rows,
				setRows,
			})}
		</>
	);
}

const newSeen = (): Seen => ({
	form: toForm({}),
	options: {},
	rows: [],
	errors: {},
});

const HOST = "app.example.com";

function renderAccess(options: RouteOptions = {}, users: string[] = []) {
	const seen = newSeen();
	return renderWithProviders(
		<Host group="access" options={options} users={users} seen={seen}>
			{(api) => (
				<AccessBody
					form={api.form}
					set={api.set}
					errors={api.errors}
					rows={api.rows}
					setRows={api.setRows}
					userErrors={api.errors}
				/>
			)}
		</Host>,
	).then(() => seen);
}

function renderHeaders(options: RouteOptions = {}) {
	const seen = newSeen();
	return renderWithProviders(
		<Host group="headers" options={options} seen={seen}>
			{(api) => (
				<HeadersBody
					form={api.form}
					set={api.set}
					errors={api.errors}
					host={HOST}
				/>
			)}
		</Host>,
	).then(() => seen);
}

function renderRedirects(options: RouteOptions = {}) {
	const seen = newSeen();
	return renderWithProviders(
		<Host group="redirects" options={options} seen={seen}>
			{(api) => (
				<RedirectsBody form={api.form} set={api.set} errors={api.errors} />
			)}
		</Host>,
	).then(() => seen);
}

function renderLimits(options: RouteOptions = {}) {
	const seen = newSeen();
	return renderWithProviders(
		<Host group="limits" options={options} seen={seen}>
			{(api) => (
				<LimitsBody form={api.form} set={api.set} errors={api.errors} />
			)}
		</Host>,
	).then(() => seen);
}

const pressed = (name: string) =>
	screen.getByRole("button", { name }).getAttribute("aria-pressed");

describe("AccessBody: basic auth", () => {
	it("lists stored users read-only with a keep-password placeholder", async () => {
		await renderAccess({ basicAuth: true }, ["alice", "bob"]);
		const list = screen.getByRole("list", { name: "Basic-auth users" });
		expect(within(list).getAllByRole("listitem")).toHaveLength(2);
		const name = screen.getByLabelText("User name 1");
		expect(name).toHaveValue("alice");
		expect(name).toHaveAttribute("readonly");
		expect(screen.getByLabelText("Password for user 1")).toHaveAttribute(
			"placeholder",
			"Password set. Type to change it",
		);
		expect(screen.getByLabelText("Password for user 1")).toHaveAttribute(
			"type",
			"password",
		);
	});

	it("turns basic auth on and warns until a user exists", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderAccess();
		const toggle = screen.getByRole("switch", {
			name: "Ask for a username and password",
		});
		expect(toggle).not.toBeChecked();
		await user.click(toggle);
		expect(toggle).toBeChecked();
		expect(seen.options.basicAuth).toBe(true);
		expect(
			screen.getByText("Add at least one user before turning basic auth on."),
		).toBeInTheDocument();
	});

	it("drops the basic-auth warning once a user is added", async () => {
		const user = userEvent.setup({ delay: null });
		await renderAccess({ basicAuth: true });
		expect(
			screen.getByText("Add at least one user before turning basic auth on."),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Add user" }));
		expect(
			screen.queryByText("Add at least one user before turning basic auth on."),
		).toBeNull();
	});

	it("adds an editable row for a new user and asks for its password", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderAccess();
		await user.click(screen.getByRole("button", { name: "Add user" }));
		const name = screen.getByLabelText("User name 1");
		expect(name).not.toHaveAttribute("readonly");
		expect(screen.getByLabelText("Password for user 1")).toHaveAttribute(
			"placeholder",
			"Password (write-only)",
		);
		expect(seen.rows).toEqual([
			{ username: "", password: "", existing: false },
		]);
		expect(name).toBeInvalid();
		expect(
			screen.getByText(
				"Use 1-64 letters, digits, dots, dashes or underscores.",
			),
		).toBeInTheDocument();

		await user.type(name, "carol");
		expect(
			screen.queryByText(
				"Use 1-64 letters, digits, dots, dashes or underscores.",
			),
		).toBeNull();
		expect(screen.getByLabelText("Password for user 1")).toBeInvalid();
		expect(
			screen.getByText("Set a password for a new user."),
		).toBeInTheDocument();

		await user.type(screen.getByLabelText("Password for user 1"), "s3cret");
		expect(seen.rows).toEqual([
			{ username: "carol", password: "s3cret", existing: false },
		]);
		expect(seen.errors).toEqual({});
		expect(screen.getByLabelText("Password for user 1")).toBeValid();
	});

	it("flags a duplicate user name", async () => {
		const user = userEvent.setup({ delay: null });
		await renderAccess({}, ["alice"]);
		await user.click(screen.getByRole("button", { name: "Add user" }));
		await user.type(screen.getByLabelText("User name 2"), "alice");
		await user.type(screen.getByLabelText("Password for user 2"), "pw");
		expect(screen.getByText("Already listed.")).toBeInTheDocument();
		expect(screen.getByLabelText("User name 2")).toBeInvalid();
		expect(screen.getByLabelText("User name 1")).toBeValid();
	});

	it("rejects a bad user name", async () => {
		const user = userEvent.setup({ delay: null });
		await renderAccess();
		await user.click(screen.getByRole("button", { name: "Add user" }));
		await user.type(screen.getByLabelText("User name 1"), "a b");
		expect(
			screen.getByText(
				"Use 1-64 letters, digits, dots, dashes or underscores.",
			),
		).toBeInTheDocument();
	});

	it("changes a stored user's password without touching the name", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderAccess({}, ["alice"]);
		await user.type(screen.getByLabelText("Password for user 1"), "newpw");
		expect(seen.rows).toEqual([
			{ username: "alice", password: "newpw", existing: true },
		]);
		expect(seen.errors).toEqual({});
	});

	it("removes the named user", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderAccess({}, ["alice", "bob"]);
		await user.click(screen.getByRole("button", { name: "Remove user alice" }));
		expect(seen.rows.map((r) => r.username)).toEqual(["bob"]);
		expect(screen.getByLabelText("User name 1")).toHaveValue("bob");
	});

	it("labels the remove button by position for a user still unnamed", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderAccess();
		await user.click(screen.getByRole("button", { name: "Add user" }));
		await user.click(screen.getByRole("button", { name: "Remove user 1" }));
		expect(seen.rows).toEqual([]);
	});

	it("reports too many users at the list level", async () => {
		const user = userEvent.setup({ delay: null });
		const names = Array.from({ length: 50 }, (_, i) => `u${i}`);
		await renderAccess({}, names);
		expect(screen.queryByText("At most 50 users.")).toBeNull();
		await user.click(screen.getByRole("button", { name: "Add user" }));
		expect(screen.getByText("At most 50 users.")).toBeInTheDocument();
	});
});

describe("AccessBody: IP restriction", () => {
	it("hides the network editor while the restriction is off", async () => {
		await renderAccess();
		expect(
			screen.getByRole("switch", { name: "Restrict by IP address" }),
		).not.toBeChecked();
		expect(screen.queryByText("Allowed networks")).toBeNull();
		expect(screen.queryByLabelText("Refusal status")).toBeNull();
	});

	it("shows the editor once switched on and stores an empty access block", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderAccess();
		await user.click(
			screen.getByRole("switch", { name: "Restrict by IP address" }),
		);
		expect(screen.getByText("Allowed networks")).toBeInTheDocument();
		expect(screen.getByText("Denied networks")).toBeInTheDocument();
		expect(seen.options.access).toEqual({});
		// With no network at all the rule set would admit nobody useful.
		expect(seen.errors.allowCidrs ?? seen.errors.accessRules).toBeDefined();
	});

	it("collects allowed and denied networks into the options", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderAccess({ access: { allowCidrs: ["10.0.0.0/8"] } });
		await user.click(
			screen.getByRole("button", { name: "Add denied network" }),
		);
		await user.type(screen.getByLabelText("Denied network 1"), "10.1.0.0/16");
		await user.click(screen.getByRole("button", { name: "Add network" }));
		await user.type(screen.getByLabelText("Allowed network 2"), "203.0.113.7");
		expect(seen.options.access).toEqual({
			allowCidrs: ["10.0.0.0/8", "203.0.113.7"],
			denyCidrs: ["10.1.0.0/16"],
		});
	});

	it("shows the per-row problem of an invalid network", async () => {
		const user = userEvent.setup({ delay: null });
		await renderAccess({ access: { allowCidrs: ["10.0.0.0/8"] } });
		await user.click(screen.getByRole("button", { name: "Add network" }));
		await user.type(screen.getByLabelText("Allowed network 2"), "not-an-ip");
		expect(screen.getByLabelText("Allowed network 2")).toBeInvalid();
		expect(screen.getByLabelText("Allowed network 1")).toBeValid();
	});

	it("validates the trusted proxy depth (0 to 5)", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderAccess({ access: { allowCidrs: ["10.0.0.0/8"] } });
		const depth = screen.getByLabelText("Trusted proxy depth");
		expect(depth).toHaveValue("0");
		await user.clear(depth);
		await user.type(depth, "9");
		expect(screen.getByText("Use 0 to 5.")).toBeInTheDocument();
		expect(depth).toBeInvalid();
		await user.clear(depth);
		await user.type(depth, "2");
		expect(screen.queryByText("Use 0 to 5.")).toBeNull();
		expect(seen.options.access?.trustedProxyDepth).toBe(2);
	});

	it("validates the refusal status (400 to 599) and sends a valid one", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderAccess({ access: { allowCidrs: ["10.0.0.0/8"] } });
		const status = screen.getByLabelText("Refusal status");
		expect(status).toHaveAttribute("placeholder", "403");
		await user.type(status, "200");
		expect(
			screen.getByText("Use a status from 400 to 599."),
		).toBeInTheDocument();
		await user.clear(status);
		await user.type(status, "404");
		expect(screen.queryByText("Use a status from 400 to 599.")).toBeNull();
		expect(seen.options.access?.rejectStatus).toBe(404);
	});

	it("keeps the stored networks and status when loaded with access options", async () => {
		await renderAccess({
			access: {
				allowCidrs: ["10.0.0.0/8"],
				denyCidrs: ["10.9.0.0/16"],
				rejectStatus: 404,
				trustedProxyDepth: 1,
			},
		});
		expect(screen.getByLabelText("Allowed network 1")).toHaveValue(
			"10.0.0.0/8",
		);
		expect(screen.getByLabelText("Denied network 1")).toHaveValue(
			"10.9.0.0/16",
		);
		expect(screen.getByLabelText("Refusal status")).toHaveValue("404");
		expect(screen.getByLabelText("Trusted proxy depth")).toHaveValue("1");
	});
});

describe("HeadersBody: security headers", () => {
	it("defaults to None and explains it", async () => {
		await renderHeaders();
		expect(pressed("None")).toBe("true");
		expect(
			screen.getByText("The proxy adds no security headers."),
		).toBeInTheDocument();
	});

	it("selects Basic and Strict, each with its explanation", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderHeaders();
		await user.click(screen.getByRole("button", { name: "Basic" }));
		expect(seen.options.securityHeaders).toBe("basic");
		expect(screen.getByText(/nosniff, frame denial/)).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Strict (HSTS)" }));
		expect(seen.options.securityHeaders).toBe("strict");
		expect(
			screen.getByText(/Strict-Transport-Security for a year/),
		).toBeInTheDocument();
	});

	it("returns to None, which sends no securityHeaders at all", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderHeaders({ securityHeaders: "strict" });
		expect(pressed("Strict (HSTS)")).toBe("true");
		await user.click(screen.getByRole("button", { name: "None" }));
		expect(seen.form.securityHeaders).toBe("");
		expect("securityHeaders" in seen.options).toBe(false);
	});

	it("toggles the noindex switch", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderHeaders();
		const toggle = screen.getByRole("switch", {
			name: "Ask search engines not to index",
		});
		await user.click(toggle);
		expect(toggle).toBeChecked();
		expect(seen.options.noindex).toBe(true);
		await user.click(toggle);
		expect("noindex" in seen.options).toBe(false);
	});
});

describe("HeadersBody: custom headers", () => {
	it("edits request and response headers independently", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderHeaders();
		const [addRequest, addResponse] = screen.getAllByRole("button", {
			name: "Add header",
		});
		await user.click(addRequest as HTMLElement);
		await user.type(screen.getByLabelText("Request header name 1"), "X-Env");
		await user.type(screen.getByLabelText("Request header value 1"), "prod");
		await user.click(addResponse as HTMLElement);
		await user.type(
			screen.getByLabelText("Response header name 1"),
			"X-Served",
		);
		await user.type(screen.getByLabelText("Response header value 1"), "nebula");
		expect(seen.options.requestHeaders).toEqual([
			{ name: "X-Env", value: "prod" },
		]);
		expect(seen.options.responseHeaders).toEqual([
			{ name: "X-Served", value: "nebula" },
		]);
	});

	it("refuses headers an operator can never set", async () => {
		const user = userEvent.setup({ delay: null });
		await renderHeaders();
		const [addRequest, addResponse] = screen.getAllByRole("button", {
			name: "Add header",
		});
		await user.click(addRequest as HTMLElement);
		await user.type(screen.getByLabelText("Request header name 1"), "Host");
		expect(
			screen.getByText("Host cannot be set on requests."),
		).toBeInTheDocument();
		await user.click(addResponse as HTMLElement);
		await user.type(
			screen.getByLabelText("Response header name 1"),
			"Set-Cookie",
		);
		expect(
			screen.getByText("Set-Cookie cannot be set by the proxy."),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Request header name 1")).toBeInvalid();
		expect(screen.getByLabelText("Response header name 1")).toBeInvalid();
	});

	it("drops a header row with an empty name from the options", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderHeaders();
		await user.click(
			screen.getAllByRole("button", { name: "Add header" })[0] as HTMLElement,
		);
		expect(seen.options.requestHeaders).toBeUndefined();
		expect(seen.errors).toEqual({});
	});
});

describe("HeadersBody: CORS", () => {
	it("is off by default and shows no CORS fields", async () => {
		await renderHeaders();
		expect(pressed("Off")).toBe("true");
		expect(screen.queryByLabelText("Allowed origins")).toBeNull();
	});

	it("applies the Public API preset", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderHeaders();
		await user.click(screen.getByRole("button", { name: "Public API" }));
		expect(seen.options.cors).toEqual({
			origins: ["*"],
			methods: ["GET", "HEAD", "OPTIONS"],
			headers: ["Content-Type", "Authorization"],
			maxAgeSeconds: 600,
		});
		expect(pressed("Public API")).toBe("true");
		expect(
			screen.getByText("Any origin may read; no cookies."),
		).toBeInTheDocument();
		expect(screen.getByLabelText("Allowed origins")).toHaveValue("*");
		expect(
			screen.getByRole("switch", { name: "Allow cookies and credentials" }),
		).not.toBeChecked();
	});

	it("applies This site only with the domain's own origin and credentials", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderHeaders();
		await user.click(screen.getByRole("button", { name: "This site only" }));
		expect(seen.options.cors).toMatchObject({
			origins: [`https://${HOST}`],
			credentials: true,
		});
		expect(screen.getByLabelText("Allowed origins")).toHaveValue(
			`https://${HOST}`,
		);
		expect(
			screen.getByRole("switch", { name: "Allow cookies and credentials" }),
		).toBeChecked();
		expect(
			screen.getByText("Only this domain's own origin, with cookies."),
		).toBeInTheDocument();
	});

	it("switches to Custom without clearing the fields, then Off removes cors", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderHeaders({
			cors: { origins: ["https://a.example.com"] },
		});
		expect(pressed("Custom")).toBe("true");
		await user.click(screen.getByRole("button", { name: "Public API" }));
		await user.click(screen.getByRole("button", { name: "Custom" }));
		expect(seen.form.corsOn).toBe(true);
		expect(seen.form.corsOrigins).toBe("*");
		await user.click(screen.getByRole("button", { name: "Off" }));
		expect(seen.form.corsOn).toBe(false);
		expect("cors" in seen.options).toBe(false);
		expect(screen.queryByLabelText("Allowed origins")).toBeNull();
	});

	it("edits a preset by hand, which turns it into Custom", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderHeaders();
		await user.click(screen.getByRole("button", { name: "Public API" }));
		await user.type(screen.getByLabelText("Exposed headers"), "X-Total");
		expect(pressed("Custom")).toBe("true");
		expect(seen.options.cors?.exposeHeaders).toEqual(["X-Total"]);
	});

	it("collects every CORS field into the options", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderHeaders({ cors: { origins: ["*"] } });
		const origins = screen.getByLabelText("Allowed origins");
		await user.clear(origins);
		await user.type(origins, "https://a.example.com, https://b.example.com");
		await user.type(screen.getByLabelText("Methods"), "GET, POST");
		await user.type(screen.getByLabelText("Allowed headers"), "X-One");
		await user.type(screen.getByLabelText("Exposed headers"), "X-Two");
		await user.type(screen.getByLabelText("Cache preflight (seconds)"), "120");
		await user.click(
			screen.getByRole("switch", { name: "Allow cookies and credentials" }),
		);
		expect(seen.options.cors).toEqual({
			origins: ["https://a.example.com", "https://b.example.com"],
			methods: ["GET", "POST"],
			headers: ["X-One"],
			exposeHeaders: ["X-Two"],
			credentials: true,
			maxAgeSeconds: 120,
		});
		expect(seen.errors).toEqual({});
	});

	it("rejects an empty or malformed origin list", async () => {
		const user = userEvent.setup({ delay: null });
		await renderHeaders({ cors: { origins: ["*"] } });
		const origins = screen.getByLabelText("Allowed origins");
		await user.clear(origins);
		expect(screen.getByText("Give 1 to 20 origins.")).toBeInTheDocument();
		expect(origins).toBeInvalid();
		await user.type(origins, "example.com");
		expect(
			screen.getByText("Use * or an origin like https://app.example.com."),
		).toBeInTheDocument();
	});

	it("refuses credentials combined with the * origin", async () => {
		const user = userEvent.setup({ delay: null });
		await renderHeaders({ cors: { origins: ["*"] } });
		await user.click(
			screen.getByRole("switch", { name: "Allow cookies and credentials" }),
		);
		expect(
			screen.getByText("Credentials cannot be combined with the * origin."),
		).toBeInTheDocument();
	});

	it("rejects a lower-case method, a bad header name and a long max age", async () => {
		const user = userEvent.setup({ delay: null });
		await renderHeaders({ cors: { origins: ["*"] } });
		await user.type(screen.getByLabelText("Methods"), "get");
		expect(
			screen.getByText("Use upper-case methods such as GET."),
		).toBeInTheDocument();
		await user.type(screen.getByLabelText("Allowed headers"), "bad@name");
		await user.type(screen.getByLabelText("Exposed headers"), "bad(name");
		expect(screen.getAllByText("Not a valid header name.")).toHaveLength(2);
		await user.type(
			screen.getByLabelText("Cache preflight (seconds)"),
			"90000",
		);
		expect(screen.getByText("Use 0 to 86400 seconds.")).toBeInTheDocument();
	});
});

describe("RedirectsBody", () => {
	it("explains the rules and offers only Add rule when there are none", async () => {
		await renderRedirects();
		expect(
			screen.getByText(/The regex uses Go \(RE2\) syntax/),
		).toBeInTheDocument();
		expect(screen.queryAllByRole("listitem")).toHaveLength(0);
		expect(
			screen.getByRole("button", { name: "Add rule" }),
		).toBeInTheDocument();
	});

	it("adds blank, non-permanent rules that start invalid", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderRedirects();
		await user.click(screen.getByRole("button", { name: "Add rule" }));
		expect(seen.form.redirects).toEqual([
			{ regex: "", replacement: "", permanent: false },
		]);
		expect(screen.getByText("Enter a regex.")).toBeInTheDocument();
		expect(screen.getByText("Enter a replacement.")).toBeInTheDocument();
		expect(screen.getByLabelText("Regex")).toBeInvalid();
		expect(screen.getByLabelText("Replacement")).toBeInvalid();
	});

	it("builds a permanent rule from the three inputs", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderRedirects();
		await user.click(screen.getByRole("button", { name: "Add rule" }));
		await user.type(screen.getByLabelText("Regex"), "^/old/(.*)");
		await user.type(
			screen.getByLabelText("Replacement"),
			"https://x.test/new/",
		);
		await user.click(screen.getByRole("switch", { name: "Permanent" }));
		expect(seen.options.redirects).toEqual([
			{
				regex: "^/old/(.*)",
				replacement: "https://x.test/new/",
				permanent: true,
			},
		]);
		expect(seen.errors).toEqual({});
	});

	it("omits permanent from the options for a temporary rule", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderRedirects({
			redirects: [{ regex: "^/a", replacement: "/b", permanent: true }],
		});
		const toggle = screen.getByRole("switch", { name: "Permanent" });
		expect(toggle).toBeChecked();
		await user.click(toggle);
		expect(toggle).not.toBeChecked();
		expect(seen.options.redirects).toEqual([
			{ regex: "^/a", replacement: "/b" },
		]);
	});

	it("rejects RE2-incompatible and uncompilable regexes", async () => {
		const user = userEvent.setup({ delay: null });
		await renderRedirects({
			redirects: [{ regex: "^/a", replacement: "/b", permanent: false }],
		});
		const regex = screen.getByLabelText("Regex");
		await user.clear(regex);
		await user.type(regex, "(?=x)");
		expect(
			screen.getByText(
				"Lookaround and backreferences are not supported (RE2 syntax).",
			),
		).toBeInTheDocument();
		await user.clear(regex);
		await user.type(regex, "[[");
		expect(screen.getByText(/^Invalid regex:/)).toBeInTheDocument();
	});

	it("numbers rules in order and removes the chosen one", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderRedirects({
			redirects: [
				{ regex: "^/one", replacement: "/1" },
				{ regex: "^/two", replacement: "/2" },
				{ regex: "^/three", replacement: "/3" },
			],
		});
		const list = screen.getByRole("list", { name: "Redirect rules" });
		expect(within(list).getAllByRole("listitem")).toHaveLength(3);
		expect(
			screen.getByLabelText("Regex", { selector: "#redirect-regex-1" }),
		).toHaveValue("^/two");
		await user.click(
			screen.getByRole("button", { name: "Remove redirect rule 2" }),
		);
		expect(seen.form.redirects.map((r) => r.regex)).toEqual([
			"^/one",
			"^/three",
		]);
		expect(
			screen.getByRole("button", { name: "Remove redirect rule 2" }),
		).toBeInTheDocument();
		expect(
			screen.queryByRole("button", { name: "Remove redirect rule 3" }),
		).toBeNull();
	});

	it("edits only the rule being typed in", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderRedirects({
			redirects: [
				{ regex: "^/one", replacement: "/1" },
				{ regex: "^/two", replacement: "/2" },
			],
		});
		const second = document.getElementById(
			"redirect-replacement-1",
		) as HTMLElement;
		await user.type(second, "x");
		expect(seen.form.redirects.map((r) => r.replacement)).toEqual([
			"/1",
			"/2x",
		]);
	});

	it("shows an error for more than ten rules", async () => {
		const user = userEvent.setup({ delay: null });
		await renderRedirects({
			redirects: Array.from({ length: 10 }, (_, i) => ({
				regex: `^/r${i}`,
				replacement: `/t${i}`,
			})),
		});
		await user.click(screen.getByRole("button", { name: "Add rule" }));
		expect(screen.getByText("At most 10 rules.")).toBeInTheDocument();
	});
});

describe("LimitsBody", () => {
	it("hides the rate fields while the limit is off", async () => {
		await renderLimits();
		expect(
			screen.getByRole("switch", { name: "Rate limit" }),
		).not.toBeChecked();
		expect(screen.queryByLabelText("Requests")).toBeNull();
	});

	it("switching the rate limit on reveals defaults and sends them", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderLimits();
		await user.click(screen.getByRole("switch", { name: "Rate limit" }));
		expect(screen.getByLabelText("Requests")).toHaveValue("50");
		expect(screen.getByLabelText("Per (seconds)")).toHaveValue("1");
		expect(screen.getByLabelText("Burst")).toHaveValue("100");
		expect(screen.getByLabelText("Trusted proxies")).toHaveValue("0");
		expect(seen.options.rateLimit).toEqual({
			average: 50,
			burst: 100,
			periodSeconds: 1,
		});
	});

	it("sends edited numbers, including the trusted proxy depth", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderLimits({
			rateLimit: { average: 10, burst: 20, periodSeconds: 5 },
		});
		await user.clear(screen.getByLabelText("Requests"));
		await user.type(screen.getByLabelText("Requests"), "200");
		await user.clear(screen.getByLabelText("Per (seconds)"));
		await user.type(screen.getByLabelText("Per (seconds)"), "60");
		await user.clear(screen.getByLabelText("Burst"));
		await user.type(screen.getByLabelText("Burst"), "300");
		await user.clear(screen.getByLabelText("Trusted proxies"));
		await user.type(screen.getByLabelText("Trusted proxies"), "1");
		expect(seen.options.rateLimit).toEqual({
			average: 200,
			burst: 300,
			periodSeconds: 60,
			trustedProxyDepth: 1,
		});
		expect(seen.errors).toEqual({});
	});

	it("flags zero requests/burst, an out-of-range period and a bad depth", async () => {
		const user = userEvent.setup({ delay: null });
		await renderLimits({
			rateLimit: { average: 10, burst: 20, periodSeconds: 5 },
		});
		await user.clear(screen.getByLabelText("Requests"));
		await user.type(screen.getByLabelText("Requests"), "0");
		await user.clear(screen.getByLabelText("Burst"));
		await user.type(screen.getByLabelText("Burst"), "abc");
		await user.clear(screen.getByLabelText("Per (seconds)"));
		await user.type(screen.getByLabelText("Per (seconds)"), "4000");
		await user.clear(screen.getByLabelText("Trusted proxies"));
		await user.type(screen.getByLabelText("Trusted proxies"), "6");
		expect(screen.getAllByText("At least 1.")).toHaveLength(2);
		expect(screen.getByText("Use 1 to 3600 seconds.")).toBeInTheDocument();
		expect(screen.getByText("Use 0 to 5.")).toBeInTheDocument();
		expect(screen.getByLabelText("Requests")).toBeInvalid();
		expect(screen.getByLabelText("Burst")).toBeInvalid();
		expect(screen.getByLabelText("Per (seconds)")).toBeInvalid();
		expect(screen.getByLabelText("Trusted proxies")).toBeInvalid();
	});

	it("turning the rate limit back off removes it from the options", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderLimits({
			rateLimit: { average: 10, burst: 20, periodSeconds: 5 },
		});
		await user.click(screen.getByRole("switch", { name: "Rate limit" }));
		expect("rateLimit" in seen.options).toBe(false);
		expect(screen.queryByLabelText("Requests")).toBeNull();
	});

	it("toggles compression", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderLimits();
		const toggle = screen.getByRole("switch", { name: "Compress responses" });
		await user.click(toggle);
		expect(toggle).toBeChecked();
		expect(seen.options.compress).toBe(true);
		await user.click(toggle);
		expect("compress" in seen.options).toBe(false);
	});
});

// --- TLS --------------------------------------------------------------------

function cert(overrides: Partial<ServerCertificate>): ServerCertificate {
	return {
		appliedAt: null,
		change: "none",
		createdAt: "2026-01-01T00:00:00Z",
		daysToExpiry: 90,
		domains: [],
		fingerprint: "ab",
		id: "cert-1",
		issuer: "Test CA",
		name: "wildcard",
		notAfter: "2027-01-01T00:00:00Z",
		notBefore: "2026-01-01T00:00:00Z",
		serverId: "server-1",
		status: "active",
		statusMessage: "",
		...overrides,
	};
}

interface TlsSeen {
	tlsMode: "auto" | "custom" | "none";
	certificateId: string;
	form: OptionsForm;
}

function TlsHost({
	seen,
	mode,
	certificateId,
	certificates,
	certificateError,
	certsLoading,
	options,
}: {
	seen: TlsSeen;
	mode: "auto" | "custom" | "none";
	certificateId: string;
	certificates: ServerCertificate[];
	certificateError: string | undefined;
	certsLoading: boolean;
	options: RouteOptions;
}) {
	const [form, setForm] = useState(() => toForm(options));
	const [tls, setTls] = useState({ tlsMode: mode, certificateId });
	seen.tlsMode = tls.tlsMode;
	seen.certificateId = tls.certificateId;
	seen.form = form;
	return (
		<TlsBody
			form={form}
			set={(patch) => setForm((current) => ({ ...current, ...patch }))}
			hostname={HOST}
			tlsMode={tls.tlsMode}
			certificateId={tls.certificateId}
			onTls={(patch) => setTls((current) => ({ ...current, ...patch }))}
			certificates={certificates}
			certificateError={certificateError}
			certsLoading={certsLoading}
		/>
	);
}

function renderTls({
	mode = "auto",
	certificateId = "",
	certificates = [],
	certificateError,
	certsLoading = false,
	options = {},
}: {
	mode?: "auto" | "custom" | "none";
	certificateId?: string;
	certificates?: ServerCertificate[];
	certificateError?: string;
	certsLoading?: boolean;
	options?: RouteOptions;
} = {}) {
	const seen: TlsSeen = { tlsMode: mode, certificateId, form: toForm(options) };
	return renderWithProviders(
		<TlsHost
			seen={seen}
			mode={mode}
			certificateId={certificateId}
			certificates={certificates}
			certificateError={certificateError}
			certsLoading={certsLoading}
			options={options}
		/>,
	).then(() => seen);
}

describe("TlsBody: mode", () => {
	it("defaults to Automatic with its help and no certificate picker", async () => {
		await renderTls();
		expect(pressed("Automatic")).toBe("true");
		expect(
			screen.getByText(/A Let's Encrypt certificate when the server/),
		).toBeInTheDocument();
		expect(screen.queryByText("Pinned certificate")).toBeNull();
	});

	it("shows the HTTP-only warning when TLS is off", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderTls();
		await user.click(screen.getByRole("button", { name: "Off (HTTP only)" }));
		expect(seen.tlsMode).toBe("none");
		expect(
			screen.getByText(/No HTTPS for this domain: it answers plain HTTP only/),
		).toBeInTheDocument();
	});

	it("clears the pinned certificate when leaving custom mode", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderTls({
			mode: "custom",
			certificateId: "cert-1",
			certificates: [cert({ domains: [HOST] })],
		});
		await user.click(screen.getByRole("button", { name: "Automatic" }));
		expect(seen.tlsMode).toBe("auto");
		expect(seen.certificateId).toBe("");
	});

	it("keeps the certificate id when switching to custom", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderTls({ certificateId: "cert-1" });
		await user.click(
			screen.getByRole("button", { name: "Custom certificate" }),
		);
		expect(seen.tlsMode).toBe("custom");
		expect(seen.certificateId).toBe("cert-1");
		expect(screen.getByText("Pinned certificate")).toBeInTheDocument();
	});
});

describe("TlsBody: pinned certificate", () => {
	it("says so when the server has no certificate uploaded", async () => {
		await renderTls({ mode: "custom" });
		expect(
			screen.getByText(/No certificate is uploaded on this server yet/),
		).toBeInTheDocument();
		expect(screen.queryByRole("combobox")).toBeNull();
	});

	it("treats only certificates staged for removal as none", async () => {
		await renderTls({
			mode: "custom",
			certificates: [cert({ change: "remove" })],
		});
		expect(
			screen.getByText(/No certificate is uploaded on this server yet/),
		).toBeInTheDocument();
	});

	it("does not claim there are none while certificates are still loading", async () => {
		await renderTls({ mode: "custom", certsLoading: true });
		expect(screen.queryByText(/No certificate is uploaded/)).toBeNull();
		expect(screen.getByRole("combobox")).toBeInTheDocument();
	});

	it("shows the certificate error under the picker", async () => {
		await renderTls({
			mode: "custom",
			certificates: [cert({ domains: [HOST] })],
			certificateError: "Pick the certificate to serve.",
		});
		expect(
			screen.getByText("Pick the certificate to serve."),
		).toBeInTheDocument();
		expect(screen.getByRole("combobox")).toBeInvalid();
	});

	it("lists usable certificates, disabling ones that do not cover the host", async () => {
		const user = userEvent.setup({ delay: null });
		await renderTls({
			mode: "custom",
			certificates: [
				cert({ id: "c-wild", name: "wildcard", domains: ["*.example.com"] }),
				cert({ id: "c-other", name: "other", domains: ["other.test"] }),
				cert({
					id: "c-gone",
					name: "removed",
					change: "remove",
					domains: [HOST],
				}),
			],
		});
		await user.click(screen.getByRole("combobox"));
		const options = await screen.findAllByRole("option");
		expect(options.map((o) => o.textContent)).toEqual([
			"wildcard*.example.com",
			`otherdoes not cover ${HOST}`,
		]);
		expect(options[0]).not.toHaveAttribute("aria-disabled", "true");
		expect(options[1]).toHaveAttribute("aria-disabled", "true");
	});

	it("pins the chosen certificate", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderTls({
			mode: "custom",
			certificates: [
				cert({ id: "c-wild", name: "wildcard", domains: ["*.example.com"] }),
			],
		});
		await user.click(screen.getByRole("combobox"));
		await user.click(await screen.findByRole("option", { name: /wildcard/ }));
		expect(seen.certificateId).toBe("c-wild");
		expect(seen.tlsMode).toBe("custom");
	});

	it("cannot pick a certificate that does not cover the host", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderTls({
			mode: "custom",
			certificates: [
				cert({ id: "c-other", name: "other", domains: ["other.test"] }),
			],
		});
		await user.click(screen.getByRole("combobox"));
		await user.click(await screen.findByRole("option", { name: /other/ }));
		expect(seen.certificateId).toBe("");
	});
});

describe("TlsBody: HTTPS redirect", () => {
	it("toggles forceHttps and sends false when turned off", async () => {
		const user = userEvent.setup({ delay: null });
		const seen = await renderTls();
		const toggle = screen.getByRole("switch", {
			name: "Redirect HTTP to HTTPS",
		});
		expect(toggle).toBeChecked();
		await user.click(toggle);
		expect(seen.form.forceHttps).toBe(false);
		expect(formToOptions(seen.form)).toEqual({ forceHttps: false });
	});

	it("shows the redirect as off and not applicable without TLS", async () => {
		await renderTls({ mode: "none" });
		expect(
			screen.getByRole("switch", { name: "Redirect HTTP to HTTPS" }),
		).not.toBeChecked();
		expect(screen.getByText("Not applicable without TLS.")).toBeInTheDocument();
	});

	it("explains how to serve plain HTTP as well when TLS is on", async () => {
		await renderTls();
		expect(
			screen.getByText(
				"Turn it off to serve the same domain over plain HTTP as well.",
			),
		).toBeInTheDocument();
		expect(
			screen.getByText(/Verification checks routing, not access rules/),
		).toBeInTheDocument();
	});
});
