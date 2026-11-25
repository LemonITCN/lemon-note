package app

import (
	"crypto/tls"
	"errors"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/config"
	"golang.org/x/crypto/acme/autocert"
)

func acmeTLS(b *config.Bootstrap, dir string) (*tls.Config, error) {
	if len(b.TLS.ACMEDomains) == 0 {
		return nil, errors.New("启用自动证书需要配置 acme_domains")
	}
	m := &autocert.Manager{Prompt: autocert.AcceptTOS, Cache: autocert.DirCache(dir), HostPolicy: autocert.HostWhitelist(b.TLS.ACMEDomains...), Email: b.TLS.ACMEEmail}
	cfg := m.TLSConfig()
	cfg.MinVersion = tls.VersionTLS12
	return cfg, nil
}
