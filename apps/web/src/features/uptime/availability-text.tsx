import { formatAvailability } from "./model";

/**
 * An availability figure. With nothing to judge it is a dash that a screen
 * reader says as "No data": never 0%, which would claim every check failed.
 */
export function AvailabilityText({ percent }: { percent: number | null }) {
	if (percent === null) {
		return (
			<>
				<span aria-hidden>—</span>
				<span className="sr-only">{formatAvailability(null)}</span>
			</>
		);
	}
	return <>{formatAvailability(percent)}</>;
}
