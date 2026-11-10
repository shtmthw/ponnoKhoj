/*
 * ponnoKhoj map glue. This is the only hand-written JavaScript in the
 * project (see docs/adr/0008-map-glue-script.md): Leaflet cannot be started
 * from HTML alone. Everything works without it; lat/lng fields and the
 * district selector are plain form controls.
 */
(function () {
  "use strict";

  function textNode(label) {
    var span = document.createElement("span");
    span.textContent = label; // never innerHTML: labels are vendor-supplied
    return span;
  }

  function initMap(el) {
    if (el._pkMap || !window.L) { return; }
    var body = document.body;
    var lat = parseFloat(el.dataset.lat);
    var lng = parseFloat(el.dataset.lng);
    if (isNaN(lat) || isNaN(lng)) { return; }
    var map = L.map(el).setView([lat, lng], parseInt(el.dataset.zoom || "13", 10));
    el._pkMap = map;
    L.tileLayer(body.dataset.mapTiles, { maxZoom: 19, attribution: body.dataset.tileAttribution }).addTo(map);

    var markers = [];
    try { markers = JSON.parse(el.dataset.markers || "[]"); } catch (e) { markers = []; }
    var points = [];
    markers.forEach(function (m) {
      var marker = L.marker([m.lat, m.lng]).addTo(map);
      if (m.label) { marker.bindPopup(textNode(m.label)); }
      points.push([m.lat, m.lng]);
    });
    if (el.dataset.radius) {
      var circle = L.circle([lat, lng], { radius: parseFloat(el.dataset.radius), weight: 1, fillOpacity: 0.05 }).addTo(map);
      map.fitBounds(circle.getBounds());
    } else if (points.length > 1) {
      map.fitBounds(points, { padding: [30, 30], maxZoom: 15 });
    }

    var latInput = el.dataset.latInput && document.getElementById(el.dataset.latInput);
    var lngInput = el.dataset.lngInput && document.getElementById(el.dataset.lngInput);
    if (!latInput || !lngInput) { return; }
    var pin = null;
    function write(la, ln) {
      latInput.value = la.toFixed(6);
      lngInput.value = ln.toFixed(6);
      var whereInput = el.dataset.whereInput && document.getElementById(el.dataset.whereInput);
      if (whereInput && !whereInput.checked) {
        whereInput.checked = true;
        whereInput.dispatchEvent(new Event("change", { bubbles: true }));
      }
      var select = el.dataset.clearSelect && document.getElementById(el.dataset.clearSelect);
      if (select) { select.value = ""; }
    }
    el._pkSetPin = function (la, ln, pan) {
      if (!pin) {
        pin = L.marker([la, ln], { draggable: true }).addTo(map);
        pin.on("dragend", function () { var p = pin.getLatLng(); write(p.lat, p.lng); });
      } else {
        pin.setLatLng([la, ln]);
      }
      write(la, ln);
      if (pan) { map.setView([la, ln], Math.max(map.getZoom(), 14)); }
    };
    el._pkClearPin = function () {
      if (pin) { map.removeLayer(pin); pin = null; }
      latInput.value = "";
      lngInput.value = "";
    };
    if (latInput.value && lngInput.value) {
      var la = parseFloat(latInput.value), ln = parseFloat(lngInput.value);
      if (!isNaN(la) && !isNaN(ln)) {
        pin = L.marker([la, ln], { draggable: true }).addTo(map);
        pin.on("dragend", function () { var p = pin.getLatLng(); write(p.lat, p.lng); });
        map.setView([la, ln], Math.max(map.getZoom(), 14));
      }
    }
    map.on("click", function (e) { el._pkSetPin(e.latlng.lat, e.latlng.lng, false); });
  }

  function initAll(root) {
    if (root.matches && root.matches("[data-map]")) { initMap(root); }
    root.querySelectorAll("[data-map]").forEach(initMap);
  }

  // "By area" / "Exact location": show the chosen panel only. Without
  // JavaScript both panels stay visible and the radio decides.
  function initWhere(root) {
    var group = root.querySelector("[data-where-group]");
    if (!group) { return; }
    var panels = group.querySelectorAll("[data-where]");
    function sync() {
      var checked = group.querySelector('input[name="where"]:checked');
      var where = checked ? checked.value : "area";
      panels.forEach(function (p) {
        var show = p.dataset.where === where;
        p.classList.toggle("is-hidden", !show);
        if (show) {
          p.querySelectorAll("[data-map]").forEach(function (m) {
            if (m._pkMap) { setTimeout(function () { m._pkMap.invalidateSize(); }, 0); }
          });
        }
      });
    }
    group.querySelectorAll('input[name="where"]').forEach(function (r) { r.addEventListener("change", sync); });
    sync();
  }

  // Position markers that CSP-safe markup cannot style inline.
  function initPositions(root) {
    root.querySelectorAll("[data-pos]").forEach(function (el) {
      var v = parseFloat(el.dataset.pos);
      if (!isNaN(v)) { el.style.left = Math.max(0, Math.min(100, v)) + "%"; }
    });
  }

  document.addEventListener("DOMContentLoaded", function () {
    // The search panel is a disclosure on phones; on wide screens it is
    // always open beside the results.
    if (window.matchMedia && window.matchMedia("(min-width: 64rem)").matches) {
      document.querySelectorAll("details[data-open-desktop]").forEach(function (d) { d.open = true; });
    }
    initAll(document);
    initWhere(document);
    initPositions(document);
    document.body.addEventListener("htmx:afterSwap", function (e) { initPositions(e.target); });
    document.body.addEventListener("htmx:afterSwap", function (e) { initAll(e.target); });

    document.querySelectorAll("[data-geolocate]").forEach(function (btn) {
      if (!navigator.geolocation) { return; }
      btn.hidden = false;
      btn.addEventListener("click", function () {
        navigator.geolocation.getCurrentPosition(function (pos) {
          var el = document.getElementById(btn.dataset.geolocate);
          if (el && el._pkSetPin) { el._pkSetPin(pos.coords.latitude, pos.coords.longitude, true); }
        }, function () {
          btn.textContent = btn.dataset.failedText;
        }, { enableHighAccuracy: false, timeout: 10000, maximumAge: 600000 });
      });
    });

    document.querySelectorAll("select[data-recenter]").forEach(function (select) {
      select.addEventListener("change", function () {
        var option = select.options[select.selectedIndex];
        var el = document.getElementById(select.dataset.recenter);
        if (!option || !option.dataset.lat || !el || !el._pkMap) { return; }
        if (el._pkClearPin) { el._pkClearPin(); }
        el._pkMap.setView([parseFloat(option.dataset.lat), parseFloat(option.dataset.lng)], 12);
      });
    });
  });
})();
