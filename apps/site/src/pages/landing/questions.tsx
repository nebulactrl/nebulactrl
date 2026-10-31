import { CaretDownIcon } from "@phosphor-icons/react/dist/csr/CaretDown";
import { type FaqItem, faqItems } from "../../content/faq.tsx";
import { body, container, gap, link } from "./shared.tsx";

/** The questions the homepage answers, by their wording in `content/faq.tsx`; /faq has the rest. */
const QUESTIONS = [
	"What do I need?",
	"What does the installer change?",
	"Is it ready for production?",
	"Do I need an AI client?",
	"What happens when the control plane is down?",
	"Who owns my data?",
] as const;

function pick(): FaqItem[] {
	const items = faqItems();
	return QUESTIONS.map((q) => {
		const item = items.find((candidate) => candidate.q === q);
		if (!item) throw new Error(`no FAQ item asks "${q}"`);
		return item;
	});
}

/** One question as a disclosure row. */
function Row({ item }: { item: FaqItem }) {
	return (
		<details className="faq-item border-line border-b first:border-t">
			<summary className="faq-summary flex items-center justify-between gap-4 py-4 font-semibold text-base text-ink-1 outline-none focus-visible:focus-ring">
				{item.q}
				<CaretDownIcon
					aria-hidden
					weight="bold"
					className="faq-chevron size-4 shrink-0 text-ink-3"
				/>
			</summary>
			<div className="max-w-[680px] pb-6 text-base text-ink-2 leading-relaxed">
				{item.a}
			</div>
		</details>
	);
}

/** Six questions as an accordion, and a link to the rest. */
export function Questions() {
	return (
		<section id="questions" className={gap} aria-labelledby="questions-title">
			<div className={container}>
				<div className="max-w-[760px]">
					<p className="section-head m-0">
						<span>Questions</span>
						<span className="section-num">05</span>
					</p>
					<h2 id="questions-title" className="display-2 m-0 mt-4 text-ink-1">
						Before you run the installer.
					</h2>
					<div className="mt-8">
						{pick().map((item) => (
							<Row key={item.q} item={item} />
						))}
					</div>
					<p className={`${body} mt-6`}>
						<a className={link} href="/faq">
							All the questions
						</a>
					</p>
				</div>
			</div>
		</section>
	);
}
