// Sends the login form as soon as the page opens in a browser, so that a
// login link logs in without a click. The form is not sent on opening the
// link itself: mail checkers that fetch links in advance would use it up
// (they rarely run scripts). The button stays as the fallback.
(function () {
  "use strict";
  const form = document.getElementById("login-now");
  if (!form) return;
  const wait = document.getElementById("login-wait");
  if (wait) wait.hidden = false;
  form.submit();
})();
