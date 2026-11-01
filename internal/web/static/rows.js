// Row selection and the context menu of the torrent list (F7-F10).
//
// Selection works like a desktop file manager: a click selects one row,
// Ctrl-click toggles a row, Shift-click selects a range, Ctrl+A selects every
// shown row, and the checkbox toggles its own row. The selection lives here,
// in the browser: it is mirrored into the checkboxes, which belong to the
// #selection form that only actions post (in the body), and it is put back
// after every table update, so a poll can neither undo a click nor carry a
// large selection in its URL.
// A right-click selects the row unless it already is, then opens the menu the
// server renders for the selection, at the cursor. This script only focuses,
// positions, toggles classes and checkboxes, and asks htmx to fetch; it never
// builds markup (S2).
(function () {
  'use strict';
  var list = document.getElementById('list');
  var menu = document.getElementById('menu');
  var counter = document.getElementById('selcount');
  if (!list || typeof htmx === 'undefined') {
    return;
  }

  var selected = new Set(); // info-hashes of the selected rows
  var anchor = null;        // hash of the row a Shift-click extends from

  function rows() {
    return Array.prototype.slice.call(list.querySelectorAll('tbody tr[id^="t-"]'));
  }
  function hashOf(row) {
    return row.id.slice(2);
  }
  function paint(row) {
    var on = selected.has(hashOf(row));
    var box = row.querySelector('input[name="h"]');
    if (box) {
      box.checked = on;
    }
    row.classList.toggle('selected', on);
  }
  // changed shows how many rows are selected, next to Select all.
  function changed() {
    if (counter) {
      counter.textContent = selected.size ? selected.size + ' selected' : '';
    }
  }
  function set(row, on) {
    if (on) {
      selected.add(hashOf(row));
    } else {
      selected.delete(hashOf(row));
    }
    paint(row);
    changed();
  }
  function only(row) {
    selected.clear();
    selected.add(hashOf(row));
    rows().forEach(paint);
    changed();
  }
  function all(on) {
    selected.clear();
    rows().forEach(function (r) {
      if (on) {
        selected.add(hashOf(r));
      }
      paint(r);
    });
    changed();
  }
  function range(row) {
    var shown = rows();
    var a = shown.findIndex(function (r) { return hashOf(r) === anchor; });
    var b = shown.indexOf(row);
    if (a < 0 || b < 0) {
      only(row);
      return;
    }
    selected.clear();
    for (var i = Math.min(a, b); i <= Math.max(a, b); i++) {
      selected.add(hashOf(shown[i]));
    }
    shown.forEach(paint);
    changed();
  }
  function rowOf(e) {
    var row = e.target.closest('tbody tr[id^="t-"]');
    return row && list.contains(row) ? row : null;
  }

  // Runs before htmx (capture). htmx then opens the row's detail panel
  // through the row's own hx-get, unless the click deselected the row.
  document.addEventListener('click', function (e) {
    var row = rowOf(e);
    if (!row || e.target.closest('input')) {
      return;
    }
    if (e.shiftKey && anchor !== null) {
      range(row);
      e.preventDefault();
    } else if (e.ctrlKey || e.metaKey) {
      set(row, !selected.has(hashOf(row)));
      anchor = hashOf(row);
      if (!selected.has(hashOf(row))) {
        e.stopPropagation();
      }
    } else {
      only(row);
      anchor = hashOf(row);
    }
  }, true);

  // The checkbox toggles just its row and opens nothing.
  list.addEventListener('change', function (e) {
    var box = e.target;
    if (box.name !== 'h') {
      return;
    }
    var row = box.closest('tr');
    set(row, box.checked);
    anchor = hashOf(row);
  });

  Array.prototype.forEach.call(document.querySelectorAll('[data-select]'), function (button) {
    button.addEventListener('click', function () {
      all(button.getAttribute('data-select') === 'all');
    });
  });

  // Ctrl+A (Cmd+A) selects every shown row, except where it means "select
  // the text": in a text field, a select or an open menu.
  document.addEventListener('keydown', function (e) {
    if (!(e.ctrlKey || e.metaKey) || e.altKey || e.shiftKey || (e.key !== 'a' && e.key !== 'A')) {
      return;
    }
    var t = e.target;
    var typing = t.closest && t.closest('textarea, select, [contenteditable], #menu');
    if (!typing && t.tagName === 'INPUT') {
      typing = !/^(checkbox|radio|button|submit|reset)$/.test(t.type);
    }
    if (typing) {
      return;
    }
    e.preventDefault();
    all(true);
  });

  // After the server replaced or patched rows: put the selection back and
  // forget rows that are no longer shown.
  function restore(e) {
    if (!list.contains(e.target)) {
      return;
    }
    var shown = new Set();
    rows().forEach(function (r) {
      shown.add(hashOf(r));
      paint(r);
    });
    selected.forEach(function (h) {
      if (!shown.has(h)) {
        selected.delete(h);
      }
    });
    changed();
  }
  document.addEventListener('htmx:afterSwap', restore);
  document.addEventListener('htmx:oobAfterSwap', restore);

  if (!menu || typeof menu.showPopover !== 'function') {
    return;
  }

  function place(x, y) {
    var w = menu.offsetWidth, h = menu.offsetHeight;
    menu.style.left = Math.max(0, Math.min(x, window.innerWidth - w - 4)) + 'px';
    menu.style.top = Math.max(0, Math.min(y, window.innerHeight - h - 4)) + 'px';
  }

  document.addEventListener('contextmenu', function (e) {
    var row = rowOf(e);
    if (!row) {
      return;
    }
    e.preventDefault();
    if (selected.has(hashOf(row))) {
      row.focus();
    } else {
      // As a plain click would: select this row alone and show its details.
      var cell = row.querySelector('td');
      if (cell) {
        cell.click();
      } else {
        only(row);
      }
    }
    var url = '/menu/' + encodeURIComponent(hashOf(row)) + '?n=' + selected.size;
    htmx.ajax('GET', url, {target: '#menu'}).then(function () {
      if (!menu.matches(':popover-open')) {
        menu.showPopover();
      }
      place(e.clientX, e.clientY);
    });
  });

  // An action closes the menu; the buttons do it declaratively, the label
  // select cannot.
  menu.addEventListener('change', function () {
    if (menu.matches(':popover-open')) {
      menu.hidePopover();
    }
  });
})();
