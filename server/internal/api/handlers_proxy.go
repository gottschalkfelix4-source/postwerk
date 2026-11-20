package api

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"postwerk/internal/store"
	"postwerk/internal/tlsutil"
)

// ---- detection ----

func (s *Server) proxyDetect(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	ci := s.resolveClient(r)
	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		xff = r.Header.Get("X-Real-IP")
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	fhost := r.Header.Get("X-Forwarded-Host")
	via := xff != "" || proto != "" || fhost != ""
	opt := func(v string) *string {
		if v == "" {
			return nil
		}
		return &v
	}
	var suggestProxy, suggestURL *string
	if via {
		suggestProxy = &ci.remote
		h := fhost
		if h == "" {
			h = r.Host
		}
		p := strings.ToLower(strings.TrimSpace(strings.Split(proto, ",")[0]))
		if p == "" {
			p = "https"
		}
		if h != "" {
			u := p + "://" + strings.Split(h, ",")[0]
			suggestURL = &u
		}
	}
	return map[string]any{
		"viaProxy": via, "remoteIp": ci.remote, "clientIp": ci.client, "trusted": ci.trusted,
		"forwardedFor": opt(xff), "forwardedProto": opt(proto), "forwardedHost": opt(fhost),
		"requestHost": r.Host, "suggestedTrustedProxy": suggestProxy, "suggestedPublicUrl": suggestURL,
	}, nil
}

// ---- guides ----

type proxyFile struct {
	Name     string `json:"name"`
	Language string `json:"language"`
	Content  string `json:"content"`
}

type proxyCert struct {
	RecommendedMode      string  `json:"recommendedMode"`
	HostPathHint         *string `json:"hostPathHint"`
	ContainerPathExample *string `json:"containerPathExample"`
	MountHint            string  `json:"mountHint"`
}

type proxyGuide struct {
	Kind        string      `json:"kind"`
	Title       string      `json:"title"`
	Steps       []string    `json:"steps"`
	Files       []proxyFile `json:"files"`
	Certificate proxyCert   `json:"certificate"`
	Notes       []string    `json:"notes"`
}

func strp(s string) *string { return &s }

const permNote = "Postwerk läuft im Container als Benutzer PUID (Unraid: 99). Private Schlüssel von Proxys sind oft nur für root lesbar. Zeigt die Zertifikatsauswahl „Zugriff verweigert“, im Postwerk-Template PUID=0 setzen."

func (s *Server) proxyGuide(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	kind := r.URL.Query().Get("kind")
	upstream := strings.TrimRight(strings.TrimSpace(r.URL.Query().Get("upstream")), "/")
	if upstream == "" {
		upstream = "http://<Server-IP>:8880"
	}
	if !strings.Contains(upstream, "://") {
		upstream = "http://" + upstream
	}
	up, err := url.Parse(upstream)
	if err != nil || up.Host == "" {
		return nil, invalid("Ungültige Upstream-Adresse")
	}
	upHost, upPort, err := net.SplitHostPort(up.Host)
	if err != nil {
		upHost, upPort = up.Host, "80"
	}
	host := s.Settings.Get().Server.Hostname
	names := []string{host}
	if ds, err := s.Store.ListDomains(r.Context()); err == nil {
		for _, d := range ds {
			names = append(names, "mta-sts."+d.Name, "autoconfig."+d.Name)
		}
	}
	certMount := "Den Zertifikatsordner des Proxys schreibgeschützt nach /certs in den Postwerk-Container einbinden (Unraid: Template-Feld „Zertifikate vom Reverse-Proxy“, Compose: `- <ordner>:/certs:ro`)."
	common := []string{
		"Die Mail-Ports 25, 465, 587 und 995 gehen NICHT über den Reverse-Proxy, sondern per Portfreigabe im Router direkt an Postwerk.",
		"Den HTTPS-Port von Postwerk (8843) brauchst du hinter einem Proxy nicht freizugeben.",
		"Zusätzlich zum Hostnamen sollten mta-sts.<domain> und autoconfig.<domain> auf Postwerk zeigen (MTA-STS-Policy, Thunderbird-Autokonfiguration).",
	}
	g := proxyGuide{Kind: kind, Files: []proxyFile{}, Notes: common}
	switch kind {
	case "npm":
		g.Title = "Nginx Proxy Manager"
		g.Steps = []string{
			"In NPM Hosts → Proxy Hosts → Add Proxy Host öffnen.",
			"Domain Names: `" + strings.Join(names, "`, `") + "`.",
			fmt.Sprintf("Scheme `%s`, Forward Hostname/IP `%s`, Forward Port `%s`. „Block Common Exploits“ aktivieren.", up.Scheme, upHost, upPort),
			"Reiter SSL: „Request a new SSL Certificate“, „Force SSL“ und „HTTP/2 Support“ aktivieren und speichern.",
			"Reiter Advanced: die Konfiguration unten einfügen (große Uploads für Import und Anhänge).",
			"In NPM unter SSL Certificates die Nummer des neuen Zertifikats merken (Ordner `npm-<Nummer>`).",
			"Den letsencrypt-Ordner von NPM schreibgeschützt nach `/certs` in Postwerk einbinden und im Schritt TLS „Zertifikat vom Reverse-Proxy übernehmen“ wählen.",
		}
		g.Files = append(g.Files, proxyFile{Name: "Advanced (Custom Nginx Configuration)", Language: "nginx", Content: "client_max_body_size 2100m;\nproxy_request_buffering off;\nproxy_read_timeout 3600s;\nproxy_send_timeout 3600s;\n"})
		g.Certificate = proxyCert{RecommendedMode: "files",
			HostPathHint:         strp("Unraid: /mnt/user/appdata/Nginx-Proxy-Manager-Official/letsencrypt bzw. /mnt/user/appdata/NginxProxyManager/letsencrypt (je nach Template)"),
			ContainerPathExample: strp("/certs/live/npm-1/fullchain.pem"), MountHint: certMount}
		g.Notes = append(g.Notes, "NPM beantwortet die Let's-Encrypt-Prüfung selbst – Postwerk kann hinter NPM kein eigenes Zertifikat per ACME holen. Deshalb übernimmt Postwerk das Zertifikat von NPM.", permNote)
	case "traefik":
		g.Title = "Traefik"
		rule := []string{}
		for _, n := range names {
			rule = append(rule, "Host(`"+n+"`)")
		}
		g.Steps = []string{
			"Die dynamische Konfiguration unten im Traefik-File-Provider ablegen (oder als Docker-Labels übernehmen, wenn Postwerk im selben Docker-Netz läuft).",
			"Entrypoint (`websecure`) und Zertifikats-Resolver (`letsencrypt`) an deine Traefik-Konfiguration anpassen.",
			"Traefik speichert Zertifikate in `acme.json` mit Rechten 600 – Postwerk kann die Datei nur lesen, wenn es als root läuft. Empfohlen: `traefik-certs-dumper` schreibt die Zertifikate als PEM-Dateien in einen Ordner, den du nach `/certs` in Postwerk einbindest.",
			"Im Schritt TLS „Zertifikat vom Reverse-Proxy übernehmen“ wählen.",
		}
		g.Files = append(g.Files,
			proxyFile{Name: "dynamic/postwerk.yml", Language: "yaml", Content: fmt.Sprintf(`http:
  routers:
    postwerk:
      rule: "%s"
      entryPoints: [websecure]
      service: postwerk
      tls:
        certResolver: letsencrypt
  services:
    postwerk:
      loadBalancer:
        servers:
          - url: "%s"
`, strings.Join(rule, " || "), upstream)},
			proxyFile{Name: "Docker-Labels (gleiches Docker-Netz)", Language: "yaml", Content: fmt.Sprintf(`labels:
  - traefik.enable=true
  - traefik.http.routers.postwerk.rule=%s
  - traefik.http.routers.postwerk.entrypoints=websecure
  - traefik.http.routers.postwerk.tls.certresolver=letsencrypt
  - traefik.http.services.postwerk.loadbalancer.server.port=8080
`, strings.Join(rule, " || "))},
			proxyFile{Name: "docker-compose: traefik-certs-dumper", Language: "yaml", Content: `  certs-dumper:
    image: ldez/traefik-certs-dumper:v2.8.3
    command: file --version v2 --watch --source /traefik/acme.json --dest /out --domain-subdir
    volumes:
      - /mnt/user/appdata/traefik:/traefik:ro
      - /mnt/user/appdata/postwerk-certs:/out
`})
		g.Certificate = proxyCert{RecommendedMode: "files",
			HostPathHint:         strp("acme.json liegt meist unter /mnt/user/appdata/traefik/; mit traefik-certs-dumper z. B. /mnt/user/appdata/postwerk-certs"),
			ContainerPathExample: strp("/certs/" + host + "/certificate.crt"), MountHint: certMount}
		g.Notes = append(g.Notes, "Traefik beantwortet die Let's-Encrypt-Prüfung selbst – Postwerk übernimmt daher das Zertifikat von Traefik.", permNote)
	case "caddy":
		g.Title = "Caddy"
		g.Steps = []string{
			"Den Block unten ins Caddyfile übernehmen und Caddy neu laden (`caddy reload`).",
			"Caddy holt das Zertifikat automatisch. Den Caddy-Datenordner (`/data` im Caddy-Container) schreibgeschützt nach `/certs` in Postwerk einbinden.",
			"Im Schritt TLS „Zertifikat vom Reverse-Proxy übernehmen“ wählen – Postwerk findet `" + host + ".crt` und `.key`.",
		}
		g.Files = append(g.Files, proxyFile{Name: "Caddyfile", Language: "caddy", Content: fmt.Sprintf(`%s {
	request_body {
		max_size 2GB
	}
	reverse_proxy %s
}
`, strings.Join(names, ", "), up.Host)})
		g.Certificate = proxyCert{RecommendedMode: "files",
			HostPathHint:         strp("Unraid: /mnt/user/appdata/caddy/data (darin caddy/certificates/…)"),
			ContainerPathExample: strp("/certs/caddy/certificates/acme-v02.api.letsencrypt.org-directory/" + host + "/" + host + ".crt"), MountHint: certMount}
		g.Notes = append(g.Notes, permNote)
	case "nginx", "swag":
		if kind == "swag" {
			g.Title = "SWAG (LinuxServer)"
			g.Steps = []string{
				"Die Datei unten als `/config/nginx/proxy-confs/postwerk.subdomain.conf` im SWAG-Ordner anlegen.",
				"In SWAG die Subdomains `mail`, `mta-sts` und `autoconfig` ins Zertifikat aufnehmen (Variable `SUBDOMAINS`) oder ein Wildcard-Zertifikat nutzen; SWAG neu starten.",
				"Den Ordner `etc/letsencrypt` aus dem SWAG-Appdata schreibgeschützt nach `/certs` in Postwerk einbinden.",
				"Im Schritt TLS „Zertifikat vom Reverse-Proxy übernehmen“ wählen.",
			}
			g.Files = append(g.Files, proxyFile{Name: "postwerk.subdomain.conf", Language: "nginx", Content: fmt.Sprintf(`server {
    listen 443 ssl;
    listen [::]:443 ssl;
    server_name %s;

    include /config/nginx/ssl.conf;
    client_max_body_size 2100m;

    location / {
        include /config/nginx/proxy.conf;
        include /config/nginx/resolver.conf;
        set $upstream_app %s;
        set $upstream_port %s;
        set $upstream_proto %s;
        proxy_pass $upstream_proto://$upstream_app:$upstream_port;
        proxy_request_buffering off;
        proxy_read_timeout 3600s;
    }
}
`, strings.Join(names, " "), upHost, upPort, up.Scheme)})
			g.Certificate = proxyCert{RecommendedMode: "files",
				HostPathHint:         strp("Unraid: /mnt/user/appdata/swag/etc/letsencrypt"),
				ContainerPathExample: strp("/certs/live/" + domainOfHost(host) + "/fullchain.pem"), MountHint: certMount}
		} else {
			g.Title = "nginx"
			g.Steps = []string{
				"Den Server-Block unten in die nginx-Konfiguration übernehmen und Pfade zu deinem Zertifikat anpassen.",
				"Zertifikat z. B. mit certbot für alle Namen ausstellen (`certbot certonly --webroot -w /var/www/certbot -d " + strings.Join(names, " -d ") + "`).",
				"`/etc/letsencrypt` schreibgeschützt nach `/certs` in Postwerk einbinden und im Schritt TLS „Zertifikat vom Reverse-Proxy übernehmen“ wählen.",
				"`nginx -t && nginx -s reload`",
			}
			g.Files = append(g.Files, proxyFile{Name: "postwerk.conf", Language: "nginx", Content: fmt.Sprintf(`server {
    listen 80;
    listen [::]:80;
    server_name %[1]s;
    location /.well-known/acme-challenge/ { root /var/www/certbot; }
    location / { return 301 https://$host$request_uri; }
}

server {
    listen 443 ssl;
    listen [::]:443 ssl;
    http2 on;
    server_name %[1]s;

    ssl_certificate     /etc/letsencrypt/live/%[2]s/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/%[2]s/privkey.pem;

    client_max_body_size 2100m;

    location / {
        proxy_pass %[3]s;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-Host $host;
        proxy_request_buffering off;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}
`, strings.Join(names, " "), host, upstream)})
			g.Certificate = proxyCert{RecommendedMode: "files", HostPathHint: strp("/etc/letsencrypt"),
				ContainerPathExample: strp("/certs/live/" + host + "/fullchain.pem"), MountHint: certMount}
		}
		g.Notes = append(g.Notes, permNote)
	default:
		g.Kind = "other"
		g.Title = "Anderer Reverse-Proxy"
		g.Steps = []string{
			"Alle Anfragen für `" + strings.Join(names, "`, `") + "` per HTTP an `" + upstream + "` weiterleiten.",
			"Die Header `Host`, `X-Forwarded-For`, `X-Forwarded-Proto` und `X-Forwarded-Host` setzen.",
			"Uploads bis 2 GB erlauben und lange Timeouts (1 Stunde) einstellen, damit Importe nicht abbrechen.",
			"Zertifikat des Proxys nach `/certs` in Postwerk einbinden oder – falls Port 80 direkt bei Postwerk ankommt – Let's Encrypt in Postwerk nutzen.",
		}
		g.Certificate = proxyCert{RecommendedMode: "files", MountHint: certMount}
		g.Notes = append(g.Notes, permNote)
	}
	return g, nil
}

func domainOfHost(h string) string {
	if i := strings.IndexByte(h, '.'); i >= 0 && strings.Count(h, ".") >= 2 {
		return h[i+1:]
	}
	return h
}

// ---- certificates ----

func (s *Server) tlsCertificates(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	if _, err := os.Stat(s.Cfg.CertsDir); err != nil {
		return []tlsutil.Found{}, nil
	}
	found := tlsutil.Scan(s.Cfg.CertsDir, s.Settings.Get().Server.Hostname)
	if found == nil {
		found = []tlsutil.Found{}
	}
	return found, nil
}

func (s *Server) tlsTest(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	var req struct {
		CertPath string `json:"certPath"`
		KeyPath  string `json:"keyPath"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	host := s.Settings.Get().Server.Hostname
	c, err := tlsutil.LoadPair(strings.TrimSpace(req.CertPath), strings.TrimSpace(req.KeyPath), host)
	if err != nil {
		msg := err.Error()
		if os.IsPermission(err) || strings.Contains(msg, "permission denied") {
			msg = "Zugriff verweigert (permission denied): " + msg
		}
		return map[string]any{"ok": false, "names": []string{}, "notAfter": nil, "matchesHostname": false, "error": msg}, nil
	}
	info := tlsutil.Describe(c)
	return map[string]any{"ok": true, "names": info.Names, "notAfter": info.NotAfter, "matchesHostname": tlsutil.Covers(*c, host)}, nil
}
