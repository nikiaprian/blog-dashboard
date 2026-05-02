/**
 * Client-side sort for tables with data-sortable="true" and thead .th-sort buttons.
 */
(function () {
  function renumberRowIndices(table) {
    const th = table.querySelector("thead th.th-num");
    if (!th || typeof th.cellIndex !== "number") return;
    const idx = th.cellIndex;
    Array.from(table.querySelectorAll("tbody tr")).forEach(function (row, i) {
      const cell = row.children[idx];
      if (cell) cell.textContent = String(i + 1);
    });
  }

  function toSortableValue(text, type) {
    const s = String(text || "").trim();
    if (type === "number") {
      return Number(s.toLowerCase().replace(/[^0-9.-]/g, "")) || 0;
    }
    if (type === "date") {
      const t = Date.parse(s);
      return Number.isNaN(t) ? 0 : t;
    }
    return s.toLowerCase();
  }

  function initSortableTable(table) {
    const tbody = table.querySelector("tbody");
    if (!tbody) return;

    const headers = table.querySelectorAll("thead .th-sort");
    if (!headers.length) return;

    const sortTable = function (btn, colIdx, forceDir) {
      const current =
        btn.dataset.sortDir === "asc" ? "asc" : btn.dataset.sortDir === "desc" ? "desc" : "";
      const next = forceDir || (current === "asc" ? "desc" : "asc");
      headers.forEach(function (h) {
        delete h.dataset.sortDir;
      });
      btn.dataset.sortDir = next;

      const type = btn.dataset.sortType || "text";
      const rows = Array.from(tbody.querySelectorAll("tr"));
      rows.sort(function (a, b) {
        const aCell = a.children[colIdx];
        const bCell = b.children[colIdx];
        const aRaw = (aCell && aCell.dataset.sortValue) || (aCell && aCell.innerText) || "";
        const bRaw = (bCell && bCell.dataset.sortValue) || (bCell && bCell.innerText) || "";
        const av = toSortableValue(aRaw, type);
        const bv = toSortableValue(bRaw, type);
        if (av < bv) return next === "asc" ? -1 : 1;
        if (av > bv) return next === "asc" ? 1 : -1;
        return 0;
      });
      rows.forEach(function (row) {
        tbody.appendChild(row);
      });
      const tableEl = btn.closest("table");
      if (tableEl) renumberRowIndices(tableEl);
    };

    headers.forEach(function (btn) {
      btn.addEventListener("click", function () {
        const th = btn.closest("th");
        const colIdx = th && typeof th.cellIndex === "number" ? th.cellIndex : 0;
        sortTable(btn, colIdx);
      });
    });

    const list = Array.from(headers);
    let defaultBtn = list.find(function (h) {
      return h.textContent.trim().toLowerCase() === "name";
    });
    let defaultDir = "asc";
    if (!defaultBtn) {
      defaultBtn = list.find(function (h) {
        return h.textContent.trim().toLowerCase() === "time" && h.dataset.sortType === "date";
      });
      if (defaultBtn) defaultDir = "desc";
    }
    if (defaultBtn) {
      const th = defaultBtn.closest("th");
      const colIdx = th && typeof th.cellIndex === "number" ? th.cellIndex : 0;
      sortTable(defaultBtn, colIdx, defaultDir);
    }
  }

  function initAll() {
    document.querySelectorAll('table[data-sortable="true"]').forEach(initSortableTable);
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", initAll);
  } else {
    initAll();
  }

  window.initSortableTables = initAll;
})();
