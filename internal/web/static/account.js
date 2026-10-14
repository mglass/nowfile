// Status expiry helpers for the account page. Without this script the form
// still works: the custom field stays visible and times are read as UTC.
(function () {
  var form = document.getElementById("status-form");
  if (!form) return;
  var select = document.getElementById("expiry");
  var wrap = document.getElementById("custom-wrap");
  form.elements.tz.value = String(-new Date().getTimezoneOffset());
  function toggle() { wrap.hidden = select.value !== "custom"; }
  select.addEventListener("change", toggle);
  toggle();
  form.addEventListener("submit", function () {
    var v = form.elements.custom.value;
    form.elements.custom_utc.value = "";
    if (select.value === "custom" && v) {
      var d = new Date(v);
      if (!isNaN(d)) form.elements.custom_utc.value = d.toISOString().replace(/\.\d{3}Z$/, "Z");
    }
  });
})();
// Time zone fields left empty get the browser's zone.
document.querySelectorAll("input[data-tz]").forEach(function (i) { if (!i.value) i.value = Intl.DateTimeFormat().resolvedOptions().timeZone; });
