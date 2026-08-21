package auth

import (
	"context"
	"errors"
	"testing"
)

func TestMemoryIdentityRepositoryFindsRegisteredCredentialByNormalizedEmail(t *testing.T) {
	repo := NewMemoryIdentityRepository()
	created, err := repo.CreateIdentity(context.Background(), NewIdentity{Name: " Ada ", Email: " ADA@example.com ", PasswordHash: "hash", Role: RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	if created.Role != RoleUser || created.AuthVersion != 1 {
		t.Fatalf("created identity = %+v", created)
	}
	found, err := repo.FindIdentityByEmail(context.Background(), "ada@example.com")
	if err != nil || found.ID != created.ID || found.PasswordHash != "hash" {
		t.Fatalf("FindIdentityByEmail() = (%+v, %v)", found, err)
	}
	_, err = repo.CreateIdentity(context.Background(), NewIdentity{Name: "Again", Email: "ada@example.com", PasswordHash: "hash", Role: RoleUser})
	if !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("duplicate create error = %v, want ErrEmailTaken", err)
	}
}
