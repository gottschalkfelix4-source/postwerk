package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"postwerk/internal/auth"
	"postwerk/internal/config"
	"postwerk/internal/store"
)

const usage = `Verwendung: postwerk [befehl]

  serve                         Server starten (Standard)
  version                       Version ausgeben
  backup <zielordner>           Konsistente Sicherung von Datenbank und Mails anlegen
  reset-password <e-mail> <pw>  Passwort eines Kontos setzen (und 2FA zurücksetzen)
`

func command(cfg *config.Config, args []string) error {
	ctx := context.Background()
	switch args[0] {
	case "backup":
		if len(args) != 2 {
			return errors.New("Zielordner fehlt\n\n" + usage)
		}
		return backup(ctx, cfg, args[1])
	case "reset-password":
		if len(args) != 3 {
			return errors.New("E-Mail und Passwort angeben\n\n" + usage)
		}
		return resetPassword(ctx, cfg, args[1], args[2])
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	}
	return fmt.Errorf("unbekannter Befehl %q\n\n%s", args[0], usage)
}

func resetPassword(ctx context.Context, cfg *config.Config, email, pw string) error {
	if len(pw) < 10 {
		return errors.New("das Passwort muss mindestens 10 Zeichen haben")
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	a, err := st.GetAccountByEmail(ctx, email)
	if err != nil {
		return fmt.Errorf("konto %s: %w", email, err)
	}
	h, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	f, empty := false, ""
	if err := st.UpdateAccount(ctx, a.ID, store.AccountPatch{PasswordHash: &h, TOTPEnabled: &f, TOTPSecret: &empty}); err != nil {
		return err
	}
	_ = st.DeleteAccountSessions(ctx, a.ID)
	fmt.Printf("Passwort für %s gesetzt, 2FA zurückgesetzt.\n", a.Email)
	return nil
}

// backup writes a consistent SQLite snapshot (VACUUM INTO) plus all blobs,
// TLS material and DKIM keys (which live in the database) into dir/<timestamp>.
func backup(ctx context.Context, cfg *config.Config, dir string) error {
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	target := filepath.Join(dir, "postwerk-"+time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(target, 0o750); err != nil {
		return err
	}
	dbPath := filepath.Join(target, "postwerk.db")
	if _, err := st.DB.ExecContext(ctx, `VACUUM INTO ?`, dbPath); err != nil {
		return fmt.Errorf("datenbank sichern: %w", err)
	}
	n := 0
	for _, sub := range []string{"blobs", "tls", "acme"} {
		src := filepath.Join(cfg.DataDir, sub)
		if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
			continue
		}
		c, err := copyTree(src, filepath.Join(target, sub))
		if err != nil {
			return fmt.Errorf("%s sichern: %w", sub, err)
		}
		n += c
	}
	fmt.Printf("Sicherung angelegt: %s (%d Dateien)\nWiederherstellen: Inhalt dieses Ordners als Datenverzeichnis verwenden.\n", target, n+1)
	return nil
}

func copyTree(src, dst string) (int, error) {
	n := 0
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o750)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, in); err != nil {
			f.Close()
			return err
		}
		n++
		return f.Close()
	})
	return n, err
}
