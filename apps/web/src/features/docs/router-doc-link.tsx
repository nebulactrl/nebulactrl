import type { DocLinkComponent } from "@nebulactrl/docs/api-reference";
import { Link } from "@tanstack/react-router";

/** The shared API reference draws its links to other docs pages with this, so they stay client side. */
export const RouterDocLink: DocLinkComponent = ({
	slug,
	className,
	children,
}) => (
	<Link to="/docs/$" params={{ _splat: slug }} className={className}>
		{children}
	</Link>
);
