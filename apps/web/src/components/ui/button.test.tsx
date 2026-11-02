import { describe, expect, it, mock } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Button, buttonVariants } from "#/components/ui/button";

describe("Button", () => {
	it("renders a button that calls onClick once per click", async () => {
		const onClick = mock();
		render(<Button onClick={onClick}>Deploy</Button>);
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Deploy" }));
		expect(onClick).toHaveBeenCalledTimes(1);
	});

	it("activates from the keyboard with Enter and Space", async () => {
		const onClick = mock();
		const user = userEvent.setup();
		render(<Button onClick={onClick}>Deploy</Button>);
		await user.tab();
		expect(screen.getByRole("button")).toHaveFocus();
		await user.keyboard("{Enter}");
		await user.keyboard(" ");
		expect(onClick).toHaveBeenCalledTimes(2);
	});

	it("ignores clicks and leaves tab order when disabled", async () => {
		const onClick = mock();
		const user = userEvent.setup();
		render(
			<Button disabled onClick={onClick}>
				Deploy
			</Button>,
		);
		const button = screen.getByRole("button", { name: "Deploy" });
		expect(button).toBeDisabled();
		await user.click(button);
		await user.tab();
		expect(button).not.toHaveFocus();
		expect(onClick).not.toHaveBeenCalled();
	});

	it("marks itself with the button slot and forwards type and aria props", () => {
		render(
			<Button type="submit" aria-label="Save changes">
				Save
			</Button>,
		);
		const button = screen.getByRole("button", { name: "Save changes" });
		expect(button).toHaveAttribute("data-slot", "button");
		expect(button).toHaveAttribute("type", "submit");
	});

	it("merges a caller className with the variant classes", () => {
		render(<Button className="custom-x">Go</Button>);
		expect(screen.getByRole("button")).toHaveClass("custom-x", "btn-primary");
	});

	it("maps each variant to its own key style and aliases to the same style", () => {
		const style = (variant: Parameters<typeof buttonVariants>[0]) =>
			buttonVariants(variant).split(" ");
		expect(style({ variant: "secondary" })).toContain("btn-secondary");
		expect(style({ variant: "outline" })).toContain("btn-secondary");
		expect(style({ variant: "ghost" })).toContain("btn-ghost");
		expect(style({ variant: "danger" })).toContain("btn-danger");
		expect(style({ variant: "destructive" })).toContain("btn-danger");
		expect(style({ variant: "danger-solid" })).toContain("btn-danger-solid");
		expect(style({ variant: "accent" })).toContain("btn-primary");
		expect(style({ variant: "link" })).not.toContain("pressable");
		// No variant given: the call-to-action default.
		expect(style({})).toContain("btn-primary");
	});

	it("applies the requested size", () => {
		render(
			<Button size="icon-sm" aria-label="More">
				+
			</Button>,
		);
		expect(screen.getByRole("button", { name: "More" })).toHaveClass("size-7");
	});

	it("renders a different element through the render prop and keeps it clickable", async () => {
		const onClick = mock();
		render(
			<Button
				render={<a href="/docs" />}
				nativeButton={false}
				onClick={onClick}
			>
				Docs
			</Button>,
		);
		// Base UI gives a non-button element the button role and tab stop.
		const link = screen.getByRole("button", { name: "Docs" });
		expect(link.tagName).toBe("A");
		expect(link).toHaveAttribute("href", "/docs");
		await userEvent.setup().click(link);
		expect(onClick).toHaveBeenCalledTimes(1);
	});
});
