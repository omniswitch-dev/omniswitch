package store

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestModelAccessPolicyPersistsAndMigrates(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := APIKey{ID: "key", Name: "test", KeyHash: "hash", CreatedAt: time.Now(), Enabled: true, AllowedModels: []string{"public-alias", "test-model"}}
	if err := st.InsertAPIKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	byID, err := st.GetAPIKeyByID(ctx, key.ID)
	if err != nil {
		t.Fatal(err)
	}
	byHash, err := st.GetAPIKeyByHash(ctx, key.KeyHash)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := st.ListAPIKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, loaded := range []APIKey{byID, byHash, listed[0]} {
		if !reflect.DeepEqual(loaded.AllowedModels, key.AllowedModels) {
			t.Fatalf("allowed_models = %v", loaded.AllowedModels)
		}
	}
	// Reproduce an existing database from before the model-access migration.
	if _, err := st.wdb.Exec("ALTER TABLE api_keys DROP COLUMN allowed_models"); err != nil {
		t.Fatal(err)
	}
	if err := st.migrate(); err != nil {
		t.Fatal(err)
	}
	legacy, err := st.GetAPIKeyByID(ctx, key.ID)
	if err != nil || len(legacy.AllowedModels) != 0 {
		t.Fatalf("legacy policy/error = %v/%v", legacy.AllowedModels, err)
	}
	if _, err := st.wdb.Exec("UPDATE api_keys SET allowed_models = 'broken'"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetAPIKeyByHash(ctx, key.KeyHash); err == nil {
		t.Fatal("corrupt policy must fail closed")
	}
}
