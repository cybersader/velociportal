package main

// This finite registry is the only mapping from metadata IDs to local assets.
// Labels and paths are static, never inferred from a private name or URL.
type serviceIcon struct {
	ID    string
	Label string
	Path  string
}

var serviceIcons = []serviceIcon{
	{"generic", "Generic service", "/static/service-icons/generic.svg"},
	{"adguard-home", "AdGuard Home", "/static/service-icons/adguard-home.png"},
	{"gitea", "Gitea", "/static/service-icons/gitea.png"},
	{"grafana", "Grafana", "/static/service-icons/grafana.png"},
	{"home-assistant", "Home Assistant", "/static/service-icons/home-assistant.png"},
	{"immich", "Immich", "/static/service-icons/immich.png"},
	{"jellyfin", "Jellyfin", "/static/service-icons/jellyfin.png"},
	{"nextcloud", "Nextcloud", "/static/service-icons/nextcloud.png"},
	{"nginx-proxy-manager", "Nginx Proxy Manager", "/static/service-icons/nginx-proxy-manager.png"},
	{"paperless-ngx", "Paperless-ngx", "/static/service-icons/paperless-ngx.png"},
	{"portainer", "Portainer", "/static/service-icons/portainer.png"},
	{"qbittorrent", "qBittorrent", "/static/service-icons/qbittorrent.png"},
	{"uptime-kuma", "Uptime Kuma", "/static/service-icons/uptime-kuma.png"},
}

var serviceIconIDs = func() []string {
	ids := make([]string, len(serviceIcons))
	for i, icon := range serviceIcons {
		ids[i] = icon.ID
	}
	return ids
}()

func serviceIconForID(id string) serviceIcon {
	for _, icon := range serviceIcons {
		if icon.ID == id {
			return icon
		}
	}
	return serviceIcons[0]
}
