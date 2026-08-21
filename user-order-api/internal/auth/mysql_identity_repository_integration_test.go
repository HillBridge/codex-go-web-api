package auth

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestMySQLRepositoryFindsCredentialByEmail(t *testing.T) {
	db := openMySQLTestDatabase(t)
	repo := NewMySQLRepository(db)
	email := fmt.Sprintf("auth-identity-%d@example.com", time.Now().UnixNano())
	created, err := repo.CreateIdentity(context.Background(), NewIdentity{Name: "Ada", Email: email, PasswordHash: "bcrypt-hash", Role: RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DELETE FROM users WHERE id = ?", created.ID) })
	found, err := repo.FindIdentityByEmail(context.Background(), "  "+email+"  ")
	if err != nil || found.ID != created.ID || found.PasswordHash != "bcrypt-hash" || found.Role != RoleAdmin {
		t.Fatalf("FindIdentityByEmail() = (%+v, %v)", found, err)
	}
}
