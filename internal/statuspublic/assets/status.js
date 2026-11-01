/* Day tooltip of the uptime bars. The page works without it (native titles). */
(() => {
	const NS = "http://www.w3.org/2000/svg", B = ".bar svg";
	/* What the page says, without the "last updated" line: a refresh that brings
	   nothing new only touches that line. Read before the script edits the bars. */
	const signature = (root) => (root.querySelector(".hero")?.innerHTML ?? "") + (root.querySelector("main")?.innerHTML ?? "");
	let sig = signature(document);
	const el = (t, c, p, ns) => {
		const e = ns ? document.createElementNS(NS, t) : document.createElement(t);
		if (c) e.setAttribute("class", c);
		p.append(e);
		return e;
	};
	const tip = el("div", "tip", document.body);
	tip.setAttribute("aria-hidden", "true");
	const dt = el("div", "tip-d", tip), st = el("div", "tip-s", tip);
	const sw = el("svg", "tip-sw", st, 1), sr = el("rect", "", sw, 1);
	sw.setAttribute("viewBox", "0 0 14 14");
	sr.setAttribute("width", 14);
	sr.setAttribute("height", 14);
	const wd = el("span", "", st), nt = el("div", "tip-n", tip), il = el("ul", "tip-i", tip);
	const live = el("div", "sr", document.body);
	live.setAttribute("aria-live", "polite");

	let cur, off, kb, timer;
	const bar = (e) => e.target.closest && e.target.closest(B);
	const state = (d) => d.state + (d.uptime ? ` · ${d.uptime} uptime` : "");
	const unmark = () => cur && (cur.classList.remove("on"), cur.parentNode.classList.remove("spot"));
	const hide = () => {
		clearTimeout(timer);
		unmark();
		cur = null;
		tip.classList.remove("open");
	};
	const later = () => {
		clearTimeout(timer);
		timer = setTimeout(hide, 100);
	};

	function place() {
		const r = cur.getBoundingClientRect(), w = tip.offsetWidth, h = tip.offsetHeight;
		const c = r.left + r.width / 2;
		const x = Math.max(8, Math.min(document.documentElement.clientWidth - w - 8, c - w / 2));
		const above = r.top - h - 8 >= 8;
		tip.style.left = x + "px";
		tip.style.top = (above ? r.top - h - 8 : r.bottom + 8) + "px";
		tip.style.transformOrigin = c - x + "px " + (above ? "100%" : "0");
		tip.toggleAttribute("data-below", !above);
	}

	function show(s, n, say) {
		clearTimeout(timer);
		n = Math.max(0, Math.min(s.children.length - 1, n));
		unmark();
		cur = s.children[n];
		const d = cur.dataset, inc = d.incidents ? d.incidents.split("\n") : [];
		cur.classList.add("on");
		s.classList.add("spot");
		const stub = cur.getAttribute("height") !== "36";
		sr.setAttribute("class", cur._c);
		sr.setAttribute("y", stub ? 9 : 0);
		sr.setAttribute("height", stub ? 5 : 14);
		dt.textContent = d.date;
		wd.textContent = state(d);
		nt.textContent = d.note || "";
		nt.hidden = !d.note;
		il.replaceChildren(...inc.map((t) => Object.assign(document.createElement("li"), { textContent: t })));
		il.hidden = !inc.length;
		tip.classList.add("open");
		place();
		if (say) live.textContent = [d.date, state(d), d.note, inc.length && "incidents: " + inc.join("; ")].filter(Boolean).join(", ");
	}

	const at = (s, e) => {
		const r = s.getBoundingClientRect(), w = s.viewBox.baseVal.width;
		return Math.max(0, Math.min(s.children.length - 1, Math.floor(((e.clientX - r.left) / r.width) * w / 10)));
	};

	addEventListener("pointermove", (e) => {
		if (e.pointerType === "touch") return;
		const s = bar(e);
		if (s) {
			kb = 0;
			const n = at(s, e);
			if (s.children[n] !== off) show(s, n);
		} else if (tip.contains(e.target)) clearTimeout(timer);
		else if (cur && !kb) later();
	});
	document.documentElement.addEventListener("mouseleave", () => cur && !kb && later());
	addEventListener("pointerdown", (e) => {
		if (e.pointerType !== "touch") return;
		const s = bar(e);
		off = null;
		if (s) show(s, at(s, e));
		else if (!tip.contains(e.target)) hide();
	});
	addEventListener("focusin", (e) => {
		const s = bar(e);
		if (s && s.matches(":focus-visible")) {
			kb = 1;
			off = null;
			show(s, s.children.length - 1);
		}
	});
	addEventListener("focusout", (e) => {
		if (kb && bar(e)) {
			kb = 0;
			hide();
		}
	});
	addEventListener("keydown", (e) => {
		if (e.key === "Escape") {
			if (cur) {
				off = cur;
				hide();
			}
			return;
		}
		const s = bar(e);
		if (!s) return;
		const k = { ArrowLeft: -1, ArrowRight: 1, Home: -1e3, End: 1e3 }[e.key];
		if (!k) return;
		e.preventDefault();
		kb = 1;
		off = null;
		show(s, (cur && cur.parentNode === s ? [...s.children].indexOf(cur) : s.children.length) + k, 1);
	});
	const again = () => cur && place();
	addEventListener("scroll", again, { passive: true });
	addEventListener("resize", again);

	const prep = (root) => {
		for (const s of root.querySelectorAll(B)) {
			s.setAttribute("aria-label", s.getAttribute("aria-label") + ". Use the arrow keys to read each day.");
			for (const r of s.children) {
				r._c = r.getAttribute("class");
				r.querySelector("title")?.remove();
			}
		}
	};
	prep(document);

	/* Soft refresh: fetch the page again every minute and swap what changed in
	   place, so an open or collapsed group, the scroll position and keyboard focus
	   survive. Without script the <noscript> meta refresh reloads the page. It
	   never runs under a pointer or keyboard tooltip, nor in a hidden tab. */
	const FOCUSABLE = "a[href],summary,.bar svg";
	const items = (root) => [...root.querySelectorAll(FOCUSABLE)];
	const refresh = async () => {
		if (document.hidden || cur) return;
		const wrap = document.querySelector(".wrap");
		if (!wrap) return;
		try {
			const res = await fetch(location.href, { cache: "no-store", credentials: "omit", headers: { Accept: "text/html" } });
			if (!res.ok) return;
			const doc = new DOMParser().parseFromString(await res.text(), "text/html");
			const next = doc.querySelector(".wrap");
			if (!next || cur) return;
			const age = wrap.querySelector(".foot .age"), nextAge = next.querySelector(".foot .age");
			const time = wrap.querySelector(".foot time"), nextTime = next.querySelector(".foot time");
			if (age && nextAge) age.textContent = nextAge.textContent;
			if (time && nextTime) {
				time.textContent = nextTime.textContent;
				time.setAttribute("datetime", nextTime.getAttribute("datetime"));
			}
			const nextSig = signature(next);
			if (nextSig === sig) return;
			sig = nextSig;
			const focus = items(wrap).indexOf(document.activeElement);
			const open = [...wrap.querySelectorAll("details.group")].map((d) => d.open);
			// The banner is a live region: update it where it stands so a change is announced.
			const banner = wrap.querySelector(".banner"), nextBanner = next.querySelector(".banner");
			const hero = next.querySelector(".hero");
			if (banner && nextBanner && hero) {
				banner.className = nextBanner.className;
				banner.innerHTML = nextBanner.innerHTML;
				nextBanner.replaceWith(banner);
				wrap.querySelector(".hero").replaceWith(hero);
			}
			const main = next.querySelector("main");
			if (main) {
				main.querySelectorAll("details.group").forEach((d, i) => {
					if (open[i] === false) d.open = false;
				});
				prep(main);
				wrap.querySelector("main").replaceWith(main);
			}
			document.title = doc.title;
			if (focus >= 0) items(wrap)[focus]?.focus({ preventScroll: true });
		} catch (_) {
			/* offline or the page moved: keep what is on screen, try again next minute */
		}
	};
	setInterval(refresh, 60000);
	document.addEventListener("visibilitychange", () => !document.hidden && refresh());
})();
