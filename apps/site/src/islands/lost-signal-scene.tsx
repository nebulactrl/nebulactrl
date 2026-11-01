import { createLostSignal } from "@nebulactrl/ascii/lost-signal";
import { AsciiScene } from "./ascii-scene.tsx";

/** The Lost signal scene behind the 404's message. */
export default function LostSignalScene() {
	return <AsciiScene scene="lost-signal" create={createLostSignal} />;
}
