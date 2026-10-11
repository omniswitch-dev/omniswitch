package catalog

import "testing"

func TestForProviderIsCaseInsensitiveAndScoped(t *testing.T) {
	openai := ForProvider("OpenAI")
	if len(openai) == 0 {
		t.Fatal("ForProvider(OpenAI) returned no models")
	}
	for _, m := range openai {
		if m.Provider != "openai" {
			t.Fatalf("ForProvider(OpenAI) leaked entry for provider %q", m.Provider)
		}
	}
}

func TestLookupFindsKnownModelAndMissesUnknown(t *testing.T) {
	m, ok := Lookup("openai", "gpt-4o-mini")
	if !ok {
		t.Fatal("Lookup(openai, gpt-4o-mini) = not found, want found")
	}
	if m.InputPerMillion != 0.15 || m.OutputPerMillion != 0.60 {
		t.Fatalf("Lookup(openai, gpt-4o-mini) pricing = %+v, want 0.15/0.60", m)
	}
	if m.SourceURL == "" || m.VerifiedOn == "" {
		t.Fatalf("catalog entry %+v missing provenance (source_url/verified_on)", m)
	}

	if _, ok := Lookup("openai", "does-not-exist"); ok {
		t.Fatal("Lookup(openai, does-not-exist) = found, want not found")
	}
}

func TestAllEntriesDocumentProvenance(t *testing.T) {
	entries := All()
	if len(entries) == 0 {
		t.Fatal("All() returned no catalog entries")
	}
	for _, m := range entries {
		if m.Provider == "" || m.ID == "" {
			t.Fatalf("catalog entry missing provider/id: %+v", m)
		}
		if m.SourceURL == "" || m.VerifiedOn == "" {
			t.Fatalf("catalog entry %s/%s missing source_url/verified_on", m.Provider, m.ID)
		}
	}
}
