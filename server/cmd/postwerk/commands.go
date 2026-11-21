package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"postwerk/internal/auth"
	"postwerk/internal/backup"
	"postwerk/internal/config"
	"postwerk/internal/store"
)

const usage = `Verwendung: postwerk [befehl]

  serve                         Server starten (Standard)
  version                       Version ausgeben
  backup [zielordner]           Archiv des gesamten Systems anlegen (Standard: <daten>/backups;
                                verschlüsselt, wenn in der Oberfläche ein Backup-Passwort gesetzt ist)
  restore <archiv> [passwort]   Archiv beim nächsten Start einspielen (danach Container neu starten)
  reset-password <e-mail> <pw>  Passwort eines Kontos setzen (und 2FA zurücksetzen)
  setup-token                   Setup-Token für die Ersteinrichtung anzeigen
  http-port open|redirect|auto-close
                                Verhalten von Port 80 setzen (Notfall: "open" öffnet ihn wieder)
`

func command(cfg *config.Config, args []string) error {
	ctx := context.Background()
	switch args[0] {
	case "backup":
		if len(args) > 2 {
			return errors.New("zu viele Argumente\n\n" + usage)
		}
		dir := filepath.Join(cfg.DataDir, "backups")
		if len(args) == 2 {
			dir = args[1]
		}
		return createBackup(ctx, cfg, dir)
	case "restore":
		if len(args) < 2 || len(args) > 3 {
			return errors.New("Archiv angeben\n\n" + usage)
		}
		pw := ""
		if len(args) == 3 {
			pw = args[2]
		}
		return prepareRestore(ctx, cfg, args[1], pw)
	case "reset-password":
		if len(args) != 3 {
			return errors.New("E-Mail und Passwort angeben\n\n" + usage)
		}
		return resetPassword(ctx, cfg, args[1], args[2])
	case "http-port":
		if len(args) != 2 {
			return errors.New("Verwendung: postwerk http-port open|redirect|auto-close")
		}
		return setHTTPPort(ctx, cfg, args[1])
	case "setup-token":
		return showSetupToken(ctx, cfg)
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
	if err := st.UpdateAccount(ctx, a.ID, store.AccountPatch{PasswordHash: &h, TOTPEnabled: &f, TOTPSecret: &empty, MustChangePassword: &f}); err != nil {
		return err
	}
	_ = st.DeleteAccountSessions(ctx, a.ID)
	_ = os.Remove(filepath.Join(cfg.DataDir, "admin-password.txt"))
	fmt.Printf("Passwort für %s gesetzt, 2FA zurückgesetzt.\n", a.Email)
	return nil
}

// createBackup writes an archive of the whole system (database snapshot,
// mails, certificates) into dir. It runs safely next to a live server.
func createBackup(ctx context.Context, cfg *config.Config, dir string) error {
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	settings, err := st.LoadSettings(ctx, cfg.Hostname)
	if err != nil {
		return err
	}
	set := settings.Get()
	password := ""
	if set.Backup.Encrypt {
		password = set.Backup.Password
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	name := backup.NewName(time.Now(), password != "")
	tmp := filepath.Join(dir, name+".part")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	runID, runErr := st.StartBackupRun(ctx, "manual", dir)
	finish := func(size int64, files int, err error) {
		if runErr == nil {
			_ = st.FinishBackupRun(ctx, runID, name, size, files, err)
		}
	}
	man := backup.Manifest{Version: version, Hostname: set.Server.Hostname, CreatedAt: time.Now().UTC()}
	man.Domains, man.Accounts, man.Messages, _ = st.BackupCounts(ctx)
	n, err := backup.Create(ctx, f, backup.Source{DataDir: cfg.DataDir, DB: st.DB, Manifest: man, Exclude: append([]string{dir}, backup.KeyExcludes(st, cfg.DataDir)...)}, password)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	target := filepath.Join(dir, name)
	if err == nil {
		err = os.Rename(tmp, target)
	}
	if err != nil {
		os.Remove(tmp)
		finish(0, 0, err)
		return err
	}
	chownLike(dir, cfg.DataDir)
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	finish(info.Size(), n, nil)
	enc := "unverschlüsselt"
	if password != "" {
		enc = "verschlüsselt"
	}
	fmt.Printf("Sicherung angelegt: %s (%d Dateien, %.1f MB, %s)\n", target, n, float64(info.Size())/(1<<20), enc)
	return nil
}

// prepareRestore unpacks an archive; it is swapped in on the next start.
func prepareRestore(ctx context.Context, cfg *config.Config, archive, password string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	if password == "" {
		if st, err := store.Open(cfg.DataDir); err == nil {
			if settings, err := st.LoadSettings(ctx, cfg.Hostname); err == nil {
				password = settings.Get().Backup.Password
			}
			st.Close()
		}
	}
	man, err := backup.PrepareRestore(ctx, f, password, cfg.DataDir, filepath.Base(archive))
	if err != nil {
		return err
	}
	chownLike(backup.StagingDir(cfg.DataDir), cfg.DataDir)
	chownLike(filepath.Join(cfg.DataDir, "restore-pending"), cfg.DataDir)
	fmt.Printf("Archiv vom %s geprüft (%d Konten, %d Nachrichten).\nJetzt den Container neu starten: docker compose restart postwerk\nDie bisherigen Daten bleiben in %s/pre-restore-<zeit> erhalten.\n",
		man.CreatedAt.Local().Format("02.01.2006 15:04"), man.Accounts, man.Messages, cfg.DataDir)
	return nil
}

// showSetupToken prints the token for the setup assistant or explains why
// none is needed.
func showSetupToken(ctx context.Context, cfg *config.Config) error {
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	n, err := st.CountAccounts(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		fmt.Println("Postwerk ist bereits eingerichtet – ein Setup-Token wird nicht mehr gebraucht.")
		if admins := adminEmails(ctx, st); len(admins) > 0 {
			fmt.Println("Anmelden mit:", strings.Join(admins, ", "))
		}
		fmt.Println("Passwort vergessen?  postwerk reset-password <e-mail> '<neues-passwort>'")
		return nil
	}
	data, err := os.ReadFile(filepath.Join(cfg.DataDir, "setup-token.txt"))
	if err != nil {
		fmt.Println("Noch kein Setup-Token vorhanden: Der Server hat noch nicht gestartet oder startet gerade.")
		fmt.Println("Kurz warten und den Befehl erneut ausführen.")
		return nil
	}
	fmt.Println("Setup-Token:", strings.TrimSpace(string(data)))
	fmt.Println("Weboberfläche öffnen und den Token im Setup-Assistenten eingeben.")
	return nil
}

// setHTTPPort changes the port-80 policy; the running server picks it up
// within a minute.
func setHTTPPort(ctx context.Context, cfg *config.Config, mode string) error {
	switch mode {
	case "open", "redirect", "auto-close":
	default:
		return errors.New("erlaubt: open, redirect, auto-close")
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	settings, err := st.LoadSettings(ctx, cfg.Hostname)
	if err != nil {
		return err
	}
	cur := settings.Get()
	cur.TLS.HTTPPort = mode
	if err := settings.Save(ctx, cur); err != nil {
		return err
	}
	fmt.Printf("Port 80 auf %q gesetzt – der Server übernimmt das innerhalb einer Minute.\n", mode)
	return nil
}
