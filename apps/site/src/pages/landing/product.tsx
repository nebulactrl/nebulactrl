import { Screen } from "./screen.tsx";
import { container } from "./shared.tsx";

/**
 * The product in one picture, at the container's full width under the hero:
 * the dashboard's own canvas (see `Screen`). The deployment bench, where a
 * release proves itself, is the first section below it.
 */
export function Product() {
	return (
		<section id="product" aria-label="The dashboard">
			<div className={container}>
				<Screen />
			</div>
		</section>
	);
}
