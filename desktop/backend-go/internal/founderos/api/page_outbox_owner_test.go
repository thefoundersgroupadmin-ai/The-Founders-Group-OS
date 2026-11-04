package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/connectors"
)

func TestOwnerAddressesAreSendAsMailboxAndFirstOwnerOnly(t *testing.T) {
	planted := filepath.Join(t.TempDir(), "planted.env")
	if err := os.WriteFile(planted, []byte("INBOX_1_FROM=me@own.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FOUNDEROS_OWNER_EMAIL", " boss@own.example , teammate@own.example")
	got := ownerAddresses(&Deps{Resolver: connectors.Resolver{Planted: planted}})
	if len(got) != 2 || got[0] != "me@own.example" || got[1] != "boss@own.example" {
		t.Fatalf("owner addresses = %q", got)
	}
}
