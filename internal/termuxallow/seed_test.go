package termuxallow_test

import (
	"os"
	"testing"

	"github.com/DSamuelHodge/dispatcher-go/internal/termuxallow"
	"github.com/DSamuelHodge/dispatcher-go/internal/verbs"
)

func TestRepoSeedCatalogLoads(t *testing.T) {
	// Ensure every seed verb argv is allowlisted (M7).
	cat, err := verbs.Load("../../verbs.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.ByName) < 70 {
		t.Fatalf("seed too small: %d", len(cat.ByName))
	}
	// New coverage verbs (M8)
	for _, want := range []string{"sensor.read", "saf.read", "wifi.enable", "keystore.sign", "nfc.write", "dialog.confirm", "job.schedule", "stt.listen", "storage.get", "notification.channel"} {
		if _, ok := cat.ByName[want]; !ok {
			t.Errorf("missing verb %s", want)
		}
	}
	for name, v := range cat.ByName {
		if err := termuxallow.ValidateArgv(v.Argv); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if v.Tier == verbs.TierA && termuxallow.IsMutating(v.Argv[0]) {
			t.Errorf("%s tier A uses mutating %s", name, v.Argv[0])
		}
	}
	// aliases
	if _, ok := cat.Get("wifi.connection-info"); !ok {
		t.Fatal("wifi alias")
	}
}

func TestOS(t *testing.T) {
	// sanity for CI platforms
	_ = os.Getenv("PATH")
}
