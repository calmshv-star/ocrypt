(function () {
  var key = "ocrypt-theme-v1";
  var root = document.documentElement;
  var theme = "light";
  try {
    theme = window.localStorage.getItem(key) === "dark" ? "dark" : "light";
  } catch (_) {}

  function apply(next) {
    theme = next;
    root.dataset.theme = theme;
    var meta = document.querySelector('meta[name="theme-color"]');
    if (meta) meta.setAttribute("content", theme === "dark" ? "#080808" : "#f4f4f2");
    try { window.localStorage.setItem(key, theme); } catch (_) {}
  }

  apply(theme);
  window.addEventListener("DOMContentLoaded", function () {
    var toggle = document.getElementById("theme-toggle");
    if (toggle) toggle.addEventListener("click", function () { apply(theme === "dark" ? "light" : "dark"); });
  });
})();
