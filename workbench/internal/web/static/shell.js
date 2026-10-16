// The sidebar: the menu button on narrow screens, and the theme the member
// chose (kept in this browser; "as the system" follows the system).
(function () {
  "use strict";
  const toggle = document.querySelector(".menu-toggle");
  const body = document.getElementById("sidenav");
  if (toggle && body) {
    document.documentElement.classList.add("js-menu");
    toggle.hidden = false;
    toggle.addEventListener("click", () => {
      const open = toggle.getAttribute("aria-expanded") !== "true";
      toggle.setAttribute("aria-expanded", String(open));
      body.classList.toggle("open", open);
    });
  }
  const choices = document.querySelectorAll(".theme-choice");
  for (const choice of choices) {
    choice.value = document.documentElement.dataset.theme || "";
    choice.addEventListener("change", () => {
      const v = choice.value;
      for (const other of choices) other.value = v;
      if (v) document.documentElement.dataset.theme = v;
      else delete document.documentElement.dataset.theme;
      try {
        if (v) localStorage.setItem("low-theme", v);
        else localStorage.removeItem("low-theme");
      } catch (e) {}
      document.dispatchEvent(new CustomEvent("low-theme"));
    });
  }
})();
