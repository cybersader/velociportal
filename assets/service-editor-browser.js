/* Browser-only presentation overrides. No identity, catalog, tokens or API calls. */
(function () {
  "use strict";
  window.__velociportalBrowserEditor = function (scope, select) {
    if (!/^[a-f0-9]{64}$/.test(scope)) throw new Error("invalid_scope");
    var key = "velociportal-service-presentation-v1:" + scope;
    var paths = Object.create(null);
    Array.from(select.options).forEach(function (option) { paths[option.value] = option.getAttribute("data-icon-path"); });
    function fail() { throw new Error("invalid_fields"); }
    function validString(value) {
      if (typeof value !== "string" || /[\u0000-\u001f\u007f-\u009f]/.test(value) || value.trim() !== value) fail();
      // UTF-8 encoding silently replaces lone surrogates; reject before measuring.
      for (var i = 0; i < value.length; i++) {
        var c = value.charCodeAt(i);
        if (c >= 0xd800 && c <= 0xdbff) {
          var next = value.charCodeAt(++i);
          if (!(next >= 0xdc00 && next <= 0xdfff)) fail();
        } else if (c >= 0xdc00 && c <= 0xdfff) fail();
      }
      return value;
    }
    function bytes(value) { return new TextEncoder().encode(value).length; }
    function validURL(value) {
      validString(value);
      if (bytes(value) > 2048 || !/^https?:\/\//.test(value) || /[^\x21-\x7e]|\\/.test(value) || /%(?:0[0-9a-f]|1[0-9a-f]|7f)|%c2%(?:8[0-9a-f]|9[0-9a-f])/i.test(value) || /%(?![0-9a-f]{2})/i.test(value)) fail();
      var parsed;
      try { parsed = new URL(value); } catch (_) { fail(); }
      var authority = value.slice(value.indexOf("://") + 3).split(/[/?#]/)[0];
      if (!parsed.hostname || parsed.username || parsed.password || authority !== parsed.host || /[*%@]/.test(authority)) fail();
      // Deliberately conservative: reject WHATWG normalization (backslashes,
      // shorthand IPs, dot segments, host case/IDNA/default-port rewriting).
      // Go url.Parse and WHATWG are different; this is a safety subset, not parity.
      var tail = value.slice(value.indexOf("://") + 3 + authority.length);
      var expected = value;
      if (!tail || tail[0] === "?" || tail[0] === "#") expected = value.slice(0, value.length - tail.length) + "/" + tail;
      if (parsed.href !== expected) fail();
      return value;
    }
    function fields(value) {
      if (!value || typeof value !== "object" || Array.isArray(value) || Object.keys(value).some(function (k) { return ["name", "url", "icon"].indexOf(k) === -1; })) fail();
      var result = Object.create(null);
      Object.keys(value).forEach(function (k) {
        var v = validString(value[k]);
        if (k === "name" && Array.from(v).length > 120) fail();
        if (k === "url" && v) validURL(v);
        if (k === "icon" && !Object.prototype.hasOwnProperty.call(paths, v)) fail();
        if (v) result[k] = v;
      });
      return result;
    }
    function empty() { return { version: 1, services: Object.create(null) }; }
    function decode(raw) {
      if (raw === null) return empty();
      if (typeof raw !== "string" || bytes(raw) > 256 * 1024) fail();
      var data = JSON.parse(raw);
      if (!data || typeof data !== "object" || Array.isArray(data) || data.version !== 1 || Object.keys(data).sort().join(",") !== "services,version" || !data.services || typeof data.services !== "object" || Array.isArray(data.services)) fail();
      var ids = Object.keys(data.services);
      if (ids.length > 1024) fail();
      var result = empty();
      ids.forEach(function (id) {
        if (!/^[1-9][0-9]*$/.test(id) || !Number.isSafeInteger(Number(id))) fail();
        result.services[id] = fields(data.services[id]);
      });
      return result;
    }
    function read() { return decode(window.localStorage.getItem(key)); }
    function card(root, id) { return root.querySelector('[data-service-id="' + id + '"]'); }
    function defaults(root, id) {
      var item = card(root, id);
      if (!item) throw new Error("unavailable");
      return { name: item.getAttribute("data-default-name"), url: item.getAttribute("data-default-url"), icon: item.getAttribute("data-default-icon") };
    }
    function applyCard(item, override) {
      var name = override.name || item.getAttribute("data-default-name");
      var url = override.url || item.getAttribute("data-default-url");
      var icon = override.icon || item.getAttribute("data-default-icon") || "";
      var main = item.querySelector(".service-main"), linked = !!url;
      if ((main.tagName === "A") !== linked) {
        var replacement = document.createElement(linked ? "a" : "div");
        replacement.className = "service-main" + (linked ? "" : " card-unlinked");
        while (main.firstChild) replacement.appendChild(main.firstChild);
        main.replaceWith(replacement); main = replacement;
      }
      if (linked) main.setAttribute("href", url);
      // Server defaults use Go's already-rendered fields, not WHATWG reparsing.
      // A valid Go URL can be outside this adapter's conservative input subset.
      var parsed = override.url ? new URL(override.url) : null;
      var hostname = parsed ? parsed.hostname : item.getAttribute("data-default-hostname") || "";
      var scheme = parsed ? parsed.protocol.slice(0, -1) : item.getAttribute("data-default-scheme") || "";
      item.querySelector(".card-name").textContent = name;
      item.querySelector(".badge").textContent = linked ? scheme : "link needed";
      item.querySelector(".service-icon").setAttribute("src", paths[icon] || paths[""]);
      item.setAttribute("data-service", name);
      item.setAttribute("data-service-hostname", hostname);
      item.querySelector("[data-edit-service]").setAttribute("aria-label", "Edit service: " + name);
      var domain = item.querySelector(".service-domain");
      if (override.url && hostname !== name) {
        if (!domain) { domain = document.createElement("span"); domain.className = "service-domain"; item.querySelector(".card-head").appendChild(domain); }
        domain.textContent = hostname;
      } else if (domain) domain.remove();
    }
    function apply(root, restoreOnly) {
      var data = empty();
      if (!restoreOnly) { try { data = read(); } catch (_) { /* Browsing survives blocked/corrupt storage. */ } }
      root.querySelectorAll("[data-default-name][data-service-id]").forEach(function (item) {
        applyCard(item, data.services[item.getAttribute("data-service-id")] || {});
      });
      if (window.__velociportalApplyPortalSearch) window.__velociportalApplyPortalSearch();
    }
    function load(root, id) {
      var entry = read().services[id] || {};
      return { fields: { name: entry.name || "", url: entry.url || "", icon: entry.icon || "" }, defaults: defaults(root, id) };
    }
    function save(root, id, value, reset) {
      // Re-read now, preserving other services/tabs. Simultaneous writes are not
      // transactions. Eligibility is checked by the controller immediately before.
      defaults(root, id);
      var entry = reset ? {} : fields(value), data = read();
      if (Object.keys(entry).length) data.services[id] = entry;
      else delete data.services[id];
      if (Object.keys(data.services).length > 1024) fail();
      var raw = JSON.stringify(data);
      if (bytes(raw) > 256 * 1024) fail();
      if (!Object.keys(data.services).length) window.localStorage.removeItem(key);
      else window.localStorage.setItem(key, raw);
      apply(root);
    }
    return { key: key, load: load, save: save, apply: apply };
  };
}());
