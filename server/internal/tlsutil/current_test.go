package tlsutil

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"postwerk/internal/store"
)

func TestCurrentCertificate(t *testing.T) {
	dir := t.TempDir()
	host := "mail.example.test"
	settings := store.TLSSettings{Mode: "selfsigned"}
	d := &Dynamic{DataDir: dir, Settings: func() store.TLSSettings { return settings }, Hostname: func() string { return host }}
	ctx := context.Background()

	cs, err := d.Current(ctx)
	if err != nil || !cs.SelfSigned || cs.Problem != "" || cs.Leaf == nil {
		t.Fatalf("selfsigned = %+v, %v", cs, err)
	}

	// Files mode with a valid pair.
	notAfter := time.Now().Add(10 * 24 * time.Hour).Truncate(time.Second)
	c, k := makeCert(t, []string{host}, notAfter)
	write(t, filepath.Join(dir, "c.pem"), c)
	write(t, filepath.Join(dir, "k.pem"), k)
	settings = store.TLSSettings{Mode: "files", Files: store.TLSFilesSettings{CertPath: filepath.Join(dir, "c.pem"), KeyPath: filepath.Join(dir, "k.pem")}}
	cs, err = d.Current(ctx)
	if err != nil || cs.SelfSigned || !cs.Leaf.NotAfter.Equal(notAfter) {
		t.Fatalf("files = %+v, %v", cs, err)
	}

	// Files mode with a missing file falls back and explains why.
	settings.Files.CertPath = filepath.Join(dir, "fehlt.pem")
	d.file = nil
	cs, err = d.Current(ctx)
	if err != nil || !cs.SelfSigned || cs.Problem == "" {
		t.Fatalf("missing file = %+v, %v", cs, err)
	}

	// ACME without a cached certificate never starts an order.
	settings = store.TLSSettings{Mode: "acme", ACME: store.ACMESettings{Enabled: true, DirectoryURL: "http://127.0.0.1:1/directory"}}
	cs, err = d.Current(ctx)
	if err != nil || !cs.SelfSigned || cs.Problem == "" {
		t.Fatalf("acme empty cache = %+v, %v", cs, err)
	}
	// With a cached certificate it is read from the cache.
	write(t, filepath.Join(dir, "acme", host), append(k, c...))
	cs, err = d.Current(ctx)
	if err != nil || cs.SelfSigned || !cs.Leaf.NotAfter.Equal(notAfter) {
		t.Fatalf("acme cached = %+v, %v", cs, err)
	}
}
