import qrcode from "qrcode-generator";

/** Modules of white around the code. The QR standard asks for four. */
const QUIET_ZONE = 4;

/** The dark modules of `text` as rows of booleans, at error correction M. */
export function qrMatrix(text: string): boolean[][] {
	const qr = qrcode(0, "M");
	qr.addData(text);
	qr.make();
	const size = qr.getModuleCount();
	return Array.from({ length: size }, (_, row) =>
		Array.from({ length: size }, (_, col) => qr.isDark(row, col)),
	);
}

/** One SVG path for the dark modules: a rectangle per horizontal run. */
export function qrPath(matrix: readonly (readonly boolean[])[]): string {
	const parts: string[] = [];
	matrix.forEach((cells, row) => {
		let col = 0;
		while (col < cells.length) {
			if (!cells[col]) {
				col += 1;
				continue;
			}
			const start = col;
			while (col < cells.length && cells[col]) col += 1;
			parts.push(
				`M${start + QUIET_ZONE} ${row + QUIET_ZONE}h${col - start}v1h-${col - start}z`,
			);
		}
	});
	return parts.join("");
}

/**
 * A QR code for `value`, drawn as one SVG path. It stays black on white in
 * every theme: scanners need that contrast, so it is not a theme colour.
 */
export function QrCode({
	value,
	label,
	size = 168,
}: {
	value: string;
	/** What the picture is, for a screen reader. */
	label: string;
	/** Rendered width and height in px. */
	size?: number;
}) {
	const matrix = qrMatrix(value);
	const modules = matrix.length + QUIET_ZONE * 2;
	return (
		<svg
			role="img"
			aria-label={label}
			viewBox={`0 0 ${modules} ${modules}`}
			width={size}
			height={size}
			shapeRendering="crispEdges"
			className="shrink-0 self-start rounded-ctl"
		>
			<rect width={modules} height={modules} fill="#fff" />
			<path d={qrPath(matrix)} fill="#000" />
		</svg>
	);
}
