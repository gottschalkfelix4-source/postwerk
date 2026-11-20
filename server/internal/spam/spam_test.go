package spam

import (
	"context"
	"fmt"
	"testing"

	"postwerk/internal/mailauth"
	"postwerk/internal/message"
	"postwerk/internal/store"
)

func TestBayesLearnsAndScores(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	settings, _ := st.LoadSettings(ctx, "mail.example.test")
	cfg := settings.Get()
	cfg.Spam.DNSBL.Enabled = false
	cfg.Spam.Bayes.MinTokens = 20
	settings.Save(ctx, cfg)
	f := &Filter{Store: st, Settings: settings}

	for i := 0; i < 15; i++ {
		f.Train(ctx, Tokenize(fmt.Sprintf("Gewinnen Sie jetzt Bitcoin Casino Bonus gratis Angebot %d", i)), true, 1)
		f.Train(ctx, Tokenize(fmt.Sprintf("Protokoll vom Projektmeeting Montag Agenda Budget Planung %d", i)), false, 1)
	}
	score := func(subject, text string) Verdict {
		p := &message.Parsed{Subject: subject, Text: text, MessageID: "x@y", From: store.Address{Email: "a@b.test"}}
		p.Date = p.Date.AddDate(2026, 0, 0)
		return f.Score(ctx, Input{Auth: mailauth.Result{SPF: "none", DKIM: "none", DMARC: "none"}, Parsed: p})
	}
	spam := score("Bonus", "Gewinnen Sie jetzt gratis Bitcoin im Casino, exklusives Angebot")
	ham := score("Meeting", "Anbei das Protokoll vom Projektmeeting mit Agenda und Budget-Planung")
	if spam.BayesP < 0.9 {
		t.Errorf("spam probability too low: %.3f (%v)", spam.BayesP, spam.Rules)
	}
	if ham.BayesP > 0.1 {
		t.Errorf("ham probability too high: %.3f (%v)", ham.BayesP, ham.Rules)
	}
	if spam.Score <= ham.Score {
		t.Errorf("spam score %.1f should exceed ham score %.1f", spam.Score, ham.Score)
	}
}

func TestDisplayNameSpoof(t *testing.T) {
	if !displayNameSpoof(store.Address{Name: "PayPal Service", Email: "alert@evil.test"}) {
		t.Error("brand in display name from foreign domain should be flagged")
	}
	if displayNameSpoof(store.Address{Name: "PayPal", Email: "service@paypal.de"}) {
		t.Error("genuine brand domain must not be flagged")
	}
	if displayNameSpoof(store.Address{Name: "Marketing Team", Email: "news@shop.test"}) {
		t.Error("ordinary name must not be flagged")
	}
}
