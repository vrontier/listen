// Asks before a form whose data-confirm holds a question is sent, in the
// layout's dialog; data-confirm-yes names the button that goes ahead.
// Without scripts the form is sent at once.
(function () {
  "use strict";
  const dialog = document.getElementById("confirm");
  if (!dialog || !dialog.showModal) return;
  const text = document.getElementById("confirm-text");
  const yes = document.getElementById("confirm-yes");
  let pending = null;
  document.addEventListener("submit", (e) => {
    const form = e.target;
    if (!form.dataset || !form.dataset.confirm) return;
    e.preventDefault();
    pending = form;
    text.textContent = form.dataset.confirm;
    yes.textContent = form.dataset.confirmYes || "Yes";
    dialog.returnValue = "";
    dialog.showModal();
  });
  dialog.addEventListener("close", () => {
    const form = pending;
    pending = null;
    if (form && dialog.returnValue === "yes") form.submit();
  });
})();
