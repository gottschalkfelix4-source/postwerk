package delivery

import (
	"context"
	"errors"
	"fmt"

	"postwerk/internal/atrest"
	"postwerk/internal/message"
	"postwerk/internal/store"
)

// sealForStorage is the last step before a message for an account is
// written: with encrypted storage enabled for the account, raw is replaced
// by its encrypted form (drafts excluded) and parsed by the parse of the
// encrypted message, so stored metadata only holds the clear headers.
// Every path that stores a message for an account (delivery, Sieve,
// imports, IMAP APPEND, sent copies) goes through StoreMessage or
// ImportMessage and therefore through this function.
func (s *Service) sealForStorage(ctx context.Context, accountID, mailboxID int64, raw []byte, parsed *message.Parsed) ([]byte, *message.Parsed, bool, error) {
	enc, err := s.Store.GetMailEncryption(ctx, accountID)
	if errors.Is(err, store.ErrNotFound) {
		return raw, parsed, false, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	if mb, err := s.Store.MailboxOwned(ctx, accountID, mailboxID); err == nil && mb.Role != nil && *mb.Role == "drafts" {
		return raw, parsed, false, nil
	}
	out := raw
	if !atrest.IsEncrypted(raw) {
		out, err = atrest.Encrypt(raw, atrest.Options{Kind: enc.Kind, PublicKey: []byte(enc.PublicKey), HideSubject: enc.HideSubject})
		if err != nil {
			// Never store the plaintext instead: the sender retries later.
			return nil, nil, false, fmt.Errorf("encrypt message for account %d: %w", accountID, err)
		}
	}
	p, err := message.Parse(out, false)
	if err != nil {
		p = &message.Parsed{}
	}
	return out, p, true, nil
}

// sealedMeta strips content-derived metadata from an encrypted message.
func sealedMeta(m *store.Message) {
	m.Preview = ""
	m.HasAttachments = false
	m.AIDone = true // AI, Paperless and embeddings cannot read encrypted mail
	m.SearchText = m.Subject + " " + m.From.Name + " " + m.From.Email + " " + addrText(m.To)
}
