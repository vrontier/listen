// A map in a pop-up to pick a stream's coordinates, which are written back
// into the coordinates field. The field can always be typed in directly.
//
// Map tiles and the place search come from openstreetmap.org, loaded by the
// browser. Search runs only when asked (the search service allows about one
// request a second and no searching as one types).
(function () {
  "use strict";
  const dialog = document.getElementById("map-picker");
  if (!dialog || !window.L) return;
  const buttons = document.querySelectorAll("[data-map-for]");
  if (!buttons.length) return;

  const coordsEl = document.getElementById("map-coords");
  const useBtn = document.getElementById("map-use");
  const query = document.getElementById("map-query");
  const results = document.getElementById("map-results");
  const searchForm = document.getElementById("map-search");
  const referrer = "strict-origin-when-cross-origin"; // OpenStreetMap asks to know the site
  let map = null, marker = null, target = null, picked = null;

  function parse(v) {
    const parts = v.includes(";") ? v.split(";") : (v.split(",").length === 2 ? v.split(",") : v.trim().split(/\s+/));
    if (parts.length !== 2) return null;
    const lat = parseFloat(parts[0].replace(",", ".")), lon = parseFloat(parts[1].replace(",", "."));
    if (!isFinite(lat) || !isFinite(lon) || Math.abs(lat) > 90 || Math.abs(lon) > 180) return null;
    return [lat, lon];
  }

  function fmt(ll) { return ll[0].toFixed(5) + ", " + ll[1].toFixed(5); }

  function place(ll) {
    picked = [ll[0], ((ll[1] + 540) % 360) - 180];
    if (!marker) {
      marker = L.marker(picked, { draggable: true }).addTo(map);
      marker.on("dragend", () => { const p = marker.getLatLng(); place([p.lat, p.lng]); });
    } else {
      marker.setLatLng(picked);
    }
    coordsEl.textContent = fmt(picked);
    useBtn.disabled = false;
  }

  function init() {
    map = L.map("map", { worldCopyJump: true });
    L.tileLayer("https://tile.openstreetmap.org/{z}/{x}/{y}.png", {
      maxZoom: 19,
      referrerPolicy: referrer,
      attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a>',
    }).addTo(map);
    map.on("click", (e) => place([e.latlng.lat, e.latlng.lng]));
  }

  function open(input) {
    target = input;
    dialog.showModal();
    if (!map) init();
    map.invalidateSize();
    results.textContent = "";
    const ll = parse(input.value || "");
    if (ll) {
      map.setView(ll, 13);
      place(ll);
    } else {
      map.setView([50, 10], 4);
      if (marker) { marker.remove(); marker = null; }
      picked = null;
      useBtn.disabled = true;
      coordsEl.textContent = "Click the map, or drag the marker, to set the location.";
      const placeField = input.form && input.form.elements.place;
      if (placeField && placeField.value && !query.value) query.value = placeField.value;
    }
  }

  let lastSearch = 0;
  searchForm.addEventListener("submit", async (e) => {
    e.preventDefault();
    const q = query.value.trim();
    if (!q || Date.now() - lastSearch < 1100) return;
    lastSearch = Date.now();
    results.textContent = "Searching…";
    try {
      const url = "https://nominatim.openstreetmap.org/search?format=jsonv2&limit=5&q=" + encodeURIComponent(q);
      const r = await fetch(url, { referrerPolicy: referrer, headers: { "Accept-Language": navigator.language || "en" } });
      const found = await r.json();
      results.textContent = "";
      if (!found.length) { results.textContent = "Nothing found."; return; }
      for (const f of found) {
        const li = document.createElement("li");
        const b = document.createElement("button");
        b.type = "button";
        b.className = "link";
        b.textContent = f.display_name;
        b.addEventListener("click", () => {
          const ll = [parseFloat(f.lat), parseFloat(f.lon)];
          map.setView(ll, 13);
          place(ll);
          results.textContent = "";
        });
        li.append(b);
        results.append(li);
      }
    } catch (err) {
      results.textContent = "The search did not answer. You can still click the map.";
    }
  });

  useBtn.addEventListener("click", () => {
    if (target && picked) {
      target.value = fmt(picked);
      target.dispatchEvent(new Event("change", { bubbles: true }));
    }
    dialog.close();
  });

  for (const b of buttons) {
    const input = document.getElementById(b.dataset.mapFor);
    if (!input) continue;
    b.hidden = false;
    b.addEventListener("click", () => open(input));
  }
})();
