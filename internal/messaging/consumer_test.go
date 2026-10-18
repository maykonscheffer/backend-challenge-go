package messaging

import (
	"encoding/json"
	"example.com/wallet/internal/domain"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestEnvelopeValidationAndFingerprint(t *testing.T) {
	e := Envelope{MessageID: "message", Type: "WagerTransactionRequested", OccurredAt: time.Now().UTC(), Data: Data{Operation: domain.Operation{ProviderID: "provider-a", ExternalID: "operation", PlayerID: uuid.NewString(), WalletID: uuid.NewString(), RoundID: "r", GameID: "g", Kind: "BET", Money: domain.MoneyJSON{Amount: "1.00", Currency: "BRL"}}, Key: "key"}}
	b, _ := json.Marshal(e)
	parsed, h, err := Decode(string(b))
	if err != nil || parsed.Data.Fingerprint() != e.Data.Fingerprint() {
		t.Fatal("financial hash differs", err)
	}
	if _, _, err = Decode(string(b) + "{}"); err == nil {
		t.Fatal("trailing JSON accepted")
	}
	e.Data.Key = "different"
	b, _ = json.Marshal(e)
	_, h2, err := Decode(string(b))
	if err != nil || h == h2 {
		t.Fatal("inbox failed to include transport key")
	}
	e.Data.Kind = "OPENING"
	b, _ = json.Marshal(e)
	if _, _, err = Decode(string(b)); err == nil {
		t.Fatal("external opening accepted")
	}
}
