/**
 * A made-up landscape, drawn in SVG, so the gallery has a photo-like image
 * (smooth tones, edges, a bright disc) without shipping a binary or reaching
 * for the network. Data URLs never taint a canvas, so dithering can read it.
 */
const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="960" height="600" viewBox="0 0 960 600">
<defs>
<linearGradient id="sky" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#0b0d2b"/><stop offset=".55" stop-color="#5a4fb8"/><stop offset="1" stop-color="#f3a76a"/></linearGradient>
<radialGradient id="sun" cx=".5" cy=".5" r=".5"><stop offset="0" stop-color="#fff6d8"/><stop offset=".45" stop-color="#ffd48a"/><stop offset="1" stop-color="#ffb06a" stop-opacity="0"/></radialGradient>
<linearGradient id="far" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#6a5aa8"/><stop offset="1" stop-color="#3a2f78"/></linearGradient>
<linearGradient id="mid" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#3b3380"/><stop offset="1" stop-color="#1c1850"/></linearGradient>
<linearGradient id="near" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#1b1745"/><stop offset="1" stop-color="#07061c"/></linearGradient>
</defs>
<rect width="960" height="600" fill="url(#sky)"/>
<circle cx="640" cy="330" r="190" fill="url(#sun)"/>
<circle cx="640" cy="330" r="62" fill="#fff4d0"/>
<g fill="#fff" opacity=".85"><circle cx="120" cy="70" r="2"/><circle cx="250" cy="130" r="1.5"/><circle cx="390" cy="50" r="2"/><circle cx="520" cy="110" r="1.5"/><circle cx="780" cy="60" r="2"/><circle cx="880" cy="140" r="1.5"/><circle cx="60" cy="180" r="1.5"/></g>
<path d="M0 400 L110 330 L210 390 L330 300 L450 395 L560 340 L690 410 L800 320 L960 400 L960 600 L0 600Z" fill="url(#far)"/>
<path d="M0 470 L90 420 L200 465 L320 400 L430 470 L560 410 L700 480 L820 430 L960 475 L960 600 L0 600Z" fill="url(#mid)"/>
<path d="M0 540 L130 500 L260 545 L400 495 L540 548 L690 505 L830 550 L960 515 L960 600 L0 600Z" fill="url(#near)"/>
</svg>`;

export const SAMPLE_IMAGE = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`;

/** Width ÷ height of `SAMPLE_IMAGE`. */
export const SAMPLE_RATIO = 960 / 600;
