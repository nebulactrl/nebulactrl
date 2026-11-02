import { describe, expect, it } from "bun:test";
import { render, screen } from "@testing-library/react";
import { NetworkChip } from "./network-chip";
import { PrivateNetworkCaption } from "./private-network-caption";

const net = (name: string, alias: string) => ({
	id: `n-${name}`,
	name,
	alias,
	serverId: "s1",
});

describe("NetworkChip", () => {
	it("renders nothing when the service is on no shared network", () => {
		const { container } = render(<NetworkChip networks={[]} />);
		expect(container).toBeEmptyDOMElement();
	});

	it("shows the network's name and reads its hostname to screen readers", () => {
		render(<NetworkChip networks={[net("backend", "api.backend")]} />);
		expect(screen.getByText("backend")).toBeInTheDocument();
		expect(
			screen.getByText("Shared network. backend: api.backend"),
		).toBeInTheDocument();
	});

	it("shows a count for several networks and lists every hostname in the title", () => {
		const { container } = render(
			<NetworkChip
				networks={[net("backend", "api.backend"), net("data", "api.data")]}
			/>,
		);
		expect(screen.getByText("2 networks")).toBeInTheDocument();
		expect(container.querySelector("[data-slot=network-chip]")).toHaveAttribute(
			"title",
			"backend: api.backend\ndata: api.data",
		);
	});
});

describe("PrivateNetworkCaption", () => {
	it("names the environment's private network and clears the inspector", () => {
		const { container } = render(
			<PrivateNetworkCaption environmentName="Staging" rightInset={320} />,
		);
		expect(screen.getByText("Private network · Staging")).toBeInTheDocument();
		expect(
			container.querySelector("[data-slot=private-network-caption]"),
		).toHaveStyle({ right: "320px" });
	});
});
