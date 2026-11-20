// Apply the stored theme before first paint to avoid a flash.
(function () {
  var mode = "system";
  try {
    mode = localStorage.getItem("postwerk.theme") || "system";
  } catch (e) {}
  var dark =
    mode === "dark" ||
    (mode === "system" && window.matchMedia("(prefers-color-scheme: dark)").matches);
  if (dark) document.documentElement.classList.add("dark");
})();
