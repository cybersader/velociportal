package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
)

type Identity struct {
	Login      string
	Name       string
	ProfilePic string
}

type contextKey struct{}

var identityKey contextKey

func setIdentityResponseCacheHeaders(header http.Header) {
	header.Set("Cache-Control", "no-store, max-age=0")
	header.Set("Pragma", "no-cache")
	header.Set("Vary", "Tailscale-User-Login, Tailscale-User-Name, Tailscale-User-Profile-Pic")
}

func IdentityFromContext(ctx context.Context) *Identity {
	id, ok := ctx.Value(identityKey).(*Identity)
	if !ok {
		return nil
	}
	return id
}

func IdentityMiddleware(trustedCIDR *net.IPNet, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setIdentityResponseCacheHeaders(w.Header())
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		if percent := strings.LastIndexByte(host, '%'); percent >= 0 {
			host = host[:percent]
		}
		ip := net.ParseIP(host)
		if ip == nil || trustedCIDR == nil || !trustedCIDR.Contains(ip) {
			slog.Debug("identity: rejected untrusted source", "remote", host)
			http.Error(w, "untrusted source", http.StatusForbidden)
			return
		}

		login := r.Header.Get("Tailscale-User-Login")
		if login == "" {
			http.Error(w, "no identity", http.StatusUnauthorized)
			return
		}
		slog.Debug("identity: request from trusted proxy", "remote", host)

		id := &Identity{
			Login:      login,
			Name:       r.Header.Get("Tailscale-User-Name"),
			ProfilePic: r.Header.Get("Tailscale-User-Profile-Pic"),
		}
		ctx := context.WithValue(r.Context(), identityKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
