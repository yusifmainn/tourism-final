/* Aze-Tourism — behaviour shared by the public pages: the mobile menu. */
(function () {
  "use strict";
  const toggle = document.querySelector(".nav-toggle"), mnav = document.getElementById("mnav");
  if (!toggle || !mnav) return;

  function setOpen(open) {
    mnav.classList.toggle("open", open);
    toggle.setAttribute("aria-expanded", String(open));
  }
  toggle.addEventListener("click", () => setOpen(!mnav.classList.contains("open")));
  mnav.addEventListener("click", (e) => { if (e.target.closest("a")) setOpen(false); });
  document.addEventListener("keydown", (e) => { if (e.key === "Escape") setOpen(false); });
})();
