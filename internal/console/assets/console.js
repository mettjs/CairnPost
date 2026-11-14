// The console works without this file. It adds a few comforts.

// Times are said in this browser's time zone: its name is kept in a
// cookie the server reads. Without this file they are in the server's.
(function () {
  try {
    var zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
    var root = document.documentElement.getAttribute("data-root") || "/";
    if (zone && document.cookie.split("; ").indexOf("cairnpost_console_zone=" + zone) < 0) {
      document.cookie = "cairnpost_console_zone=" + zone + "; path=" + root + "; max-age=31536000; samesite=lax";
    }
  } catch (e) {}
})();

// A button that copies a secret shown once.
document.addEventListener("click", function (event) {
  var button = event.target.closest("[data-copy]");
  if (!button) return;
  var source = document.getElementById(button.getAttribute("data-copy"));
  if (!source || !navigator.clipboard) return;
  navigator.clipboard.writeText(source.textContent).then(function () {
    var label = button.textContent;
    button.textContent = "Copied";
    setTimeout(function () { button.textContent = label; }, 1600);
  });
});

// What an endpoint hears about is one choice of two. Choosing "every
// event" unticks the events, and ticking an event chooses "only these",
// so the form never says both. Without this file the server asks which.
document.addEventListener("change", function (event) {
  var input = event.target;
  var set = input.closest ? input.closest("fieldset") : null;
  if (!set || !input.checked) return;
  if (input.name === "hears" && input.value === "all") {
    Array.prototype.forEach.call(set.querySelectorAll('input[name="events"]'), function (box) {
      box.checked = false;
    });
  }
  if (input.name === "events") {
    var only = set.querySelector('input[name="hears"][value="some"]');
    if (only) only.checked = true;
  }
});

// A chart says what is under the pointer. Without this file the numbers
// are in the table under it.
(function () {
  var tip = null, over = null;
  function hide() {
    over = null;
    if (tip) tip.hidden = true;
  }
  function show(step, x, y) {
    if (!tip) {
      tip = document.createElement("div");
      tip.className = "tip";
      document.body.appendChild(tip);
    }
    if (step !== over) {
      over = step;
      tip.textContent = "";
      (step.getAttribute("data-tip") || "").split("|").forEach(function (text, i) {
        var line = document.createElement(i === 0 ? "b" : "div");
        line.textContent = text;
        tip.appendChild(line);
      });
      tip.hidden = false;
    }
    tip.style.left = Math.max(8, Math.min(x + 14, window.innerWidth - tip.offsetWidth - 8)) + "px";
    tip.style.top = Math.max(8, y - tip.offsetHeight - 12) + "px";
  }
  document.addEventListener("pointermove", function (event) {
    var step = event.target.closest ? event.target.closest(".chart .step") : null;
    if (step) show(step, event.clientX, event.clientY);
    else hide();
  });
  // The pointer left the page, or the page moved under it.
  document.documentElement.addEventListener("pointerleave", hide);
  document.addEventListener("pointercancel", hide);
  document.addEventListener("scroll", hide, true);
  window.addEventListener("blur", hide);
})();
