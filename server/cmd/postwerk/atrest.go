package main

import (
	"log/slog"

	"postwerk/internal/store"
	"postwerk/internal/vault"
)

// logMasterKey reports where the master key for secrets at rest comes from.
func logMasterKey(st *store.Store, log *slog.Logger) {
	k := st.Vault
	log = log.With("component", "security", "source", k.Source, "fingerprint", k.Fingerprint())
	switch {
	case k.Generated:
		log.Warn("master key generated – keep a copy of this file outside the server; without it encrypted secrets cannot be restored",
			"file", k.File)
	case k.Source == vault.SourceEnv:
		log.Info("master key loaded from POSTWERK_MASTER_KEY")
	default:
		log.Info("master key loaded", "file", k.File, "in_data_dir", k.InDataDir)
	}
	if st.BlobEncryption() {
		log.Info("blob encryption enabled", "key_in_data_dir", k.InDataDir)
	}
}
