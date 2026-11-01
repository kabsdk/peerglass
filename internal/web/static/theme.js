// Theme button (F14): cycles the system's theme, light and dark. The choice
// is kept in a cookie so the server can render it on <html> next time;
// nothing else is stored. Like rows.js, this never builds markup (S2).
(function () {
  'use strict';
  var button = document.getElementById('theme');
  if (!button) {
    return;
  }
  var order = ['', 'light', 'dark'];
  var names = {'': 'System', light: 'Light', dark: 'Dark'};

  button.addEventListener('click', function () {
    var root = document.documentElement;
    var current = root.getAttribute('data-theme') || '';
    var next = order[(order.indexOf(current) + 1) % order.length];
    if (next) {
      root.setAttribute('data-theme', next);
      document.cookie = 'theme=' + next + '; path=/; max-age=31536000; samesite=strict';
    } else {
      root.removeAttribute('data-theme');
      document.cookie = 'theme=; path=/; max-age=0; samesite=strict';
    }
    button.textContent = 'Theme: ' + names[next];
  });
})();
