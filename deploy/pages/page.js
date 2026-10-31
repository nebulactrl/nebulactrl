(function () {
  var host = document.getElementById("host");
  if (host && !document.body.hasAttribute("data-preview") && location.hostname) {
    host.textContent = location.hostname;
    host.parentNode.hidden = false;
  }
  var t = document.querySelector("time[datetime]");
  if (t) {
    var d = new Date(t.getAttribute("datetime"));
    if (!isNaN(d.getTime())) {
      try {
        t.textContent = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(d);
      } catch (e) {}
    }
  }
})();
