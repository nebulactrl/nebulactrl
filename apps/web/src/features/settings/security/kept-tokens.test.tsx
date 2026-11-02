import { describe, expect, test } from "bun:test";
import { screen, within } from "@testing-library/react";
import type { LiveTokenBody } from "#/api/gen/types.gen";
import { renderWithProviders } from "#/test/render";
import { KeptTokens } from "./kept-tokens";

const token = (over: Partial<LiveTokenBody> = {}): LiveTokenBody => ({
	id: "t1",
	name: "ci-deploy",
	prefix: "nbl_ab12",
	teamId: "team1",
	teamName: "Platform",
	createdAt: "2026-01-01T00:00:00Z",
	...over,
});

describe("KeptTokens", () => {
	test("renders nothing when no token is left and none was revoked", async () => {
		const { container } = await renderWithProviders(
			<KeptTokens tokens={[]} revoked={0} />,
		);
		expect(container).toBeEmptyDOMElement();
	});

	test("says how many tokens were revoked, singular and plural", async () => {
		const one = await renderWithProviders(
			<KeptTokens tokens={[]} revoked={1} />,
		);
		expect(screen.getByText("1 API token was revoked.")).toBeInTheDocument();
		expect(screen.queryByText(/still work/)).not.toBeInTheDocument();
		one.unmount();

		await renderWithProviders(<KeptTokens tokens={[]} revoked={3} />);
		expect(screen.getByText("3 API tokens were revoked.")).toBeInTheDocument();
	});

	test("lists the surviving token with its prefix, team and 'never used'", async () => {
		await renderWithProviders(<KeptTokens tokens={[token()]} revoked={0} />);
		expect(screen.getByText("This API token still works:")).toBeInTheDocument();
		const item = screen.getByRole("listitem");
		expect(within(item).getByText("ci-deploy")).toBeInTheDocument();
		expect(item).toHaveTextContent("nbl_ab12… · Platform · never used");
		expect(screen.queryByText(/revoked/)).not.toBeInTheDocument();
	});

	test("shows when a token was last used and links to the API tokens page", async () => {
		await renderWithProviders(
			<KeptTokens
				tokens={[
					token({ lastUsedAt: "2026-01-02T03:04:05Z" }),
					token({ id: "t2", name: "agent", prefix: "nbl_cd34" }),
				]}
				revoked={2}
			/>,
		);
		expect(screen.getByText("2 API tokens were revoked.")).toBeInTheDocument();
		expect(
			screen.getByText("These 2 API tokens still work:"),
		).toBeInTheDocument();
		const [used, unused] = screen.getAllByRole("listitem");
		expect(used).toHaveTextContent("last used");
		expect(used?.querySelector("time")).toHaveAttribute(
			"datetime",
			"2026-01-02T03:04:05Z",
		);
		expect(unused).toHaveTextContent("never used");
		expect(
			screen.getByRole("link", { name: "Account settings → API tokens" }),
		).toHaveAttribute("href", "/account/tokens");
	});
});
