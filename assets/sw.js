"use strict";

// Velociportal deliberately has no fetch handler and stores no portal responses.
// Authorization-derived services and machines must always come from the server.
self.addEventListener("install", function () {
  self.skipWaiting();
});

self.addEventListener("activate", function (event) {
  event.waitUntil(self.clients.claim());
});
