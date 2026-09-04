package main

import (
	"encoding/json"
	"image"
	_ "image/png"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPWAStaticAssetsAreEmbeddedAndPrivacySafe(t *testing.T) {
	static, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		t.Fatalf("fs.Sub() error = %v", err)
	}
	handler := staticAssetHandler(static)

	t.Run("manifest", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/static/manifest.json", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if contentType := rec.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
			t.Fatalf("Content-Type = %q", contentType)
		}
		var manifest struct {
			ID       string `json:"id"`
			StartURL string `json:"start_url"`
			Scope    string `json:"scope"`
			Display  string `json:"display"`
			Icons    []struct {
				Src     string `json:"src"`
				Sizes   string `json:"sizes"`
				Type    string `json:"type"`
				Purpose string `json:"purpose"`
			} `json:"icons"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &manifest); err != nil {
			t.Fatalf("manifest decode error = %v", err)
		}
		if manifest.ID != "/" || manifest.StartURL != "/" || manifest.Scope != "/" || manifest.Display != "standalone" {
			t.Fatalf("manifest navigation fields = %#v", manifest)
		}
		if len(manifest.Icons) != 3 || manifest.Icons[2].Purpose != "maskable" {
			t.Fatalf("manifest icons = %#v", manifest.Icons)
		}
	})

	t.Run("service worker", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/static/sw.js", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if got := rec.Header().Get("Service-Worker-Allowed"); got != "/" {
			t.Fatalf("Service-Worker-Allowed = %q", got)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Fatalf("Cache-Control = %q", got)
		}
		worker := rec.Body.String()
		for _, forbidden := range []string{`addEventListener("fetch"`, "caches.", "caches[", "CacheStorage", "offline"} {
			if strings.Contains(worker, forbidden) {
				t.Fatalf("service worker contains forbidden caching behavior marker %q", forbidden)
			}
		}
		for _, required := range []string{`addEventListener("install"`, `addEventListener("activate"`} {
			if !strings.Contains(worker, required) {
				t.Fatalf("service worker omitted %q", required)
			}
		}
	})
}

func TestPWAIconDimensions(t *testing.T) {
	static, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		t.Fatalf("fs.Sub() error = %v", err)
	}
	for path, want := range map[string]image.Point{
		"icons/apple-touch-icon-180.png": {X: 180, Y: 180},
		"icons/icon-192.png":             {X: 192, Y: 192},
		"icons/icon-512.png":             {X: 512, Y: 512},
		"icons/icon-512-maskable.png":    {X: 512, Y: 512},
	} {
		t.Run(path, func(t *testing.T) {
			file, err := static.Open(path)
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			defer file.Close()
			config, format, err := image.DecodeConfig(file)
			if err != nil {
				t.Fatalf("DecodeConfig() error = %v", err)
			}
			if format != "png" || config.Width != want.X || config.Height != want.Y {
				t.Fatalf("icon = %s %dx%d, want png %dx%d", format, config.Width, config.Height, want.X, want.Y)
			}
		})
	}
}
