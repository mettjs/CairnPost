// The console works without this file. It adds two comforts.

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
