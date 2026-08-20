package testdb

import "testing"

func TestValidateDSNRejectsDevelopmentDatabase(t *testing.T) {
	err := ValidateDSN("app:app_password@tcp(127.0.0.1:3307)/user_order_api?parseTime=true")
	if err == nil {
		t.Fatal("ValidateDSN() error = nil, want development database rejection")
	}
}

func TestValidateDSNAcceptsDedicatedTestDatabase(t *testing.T) {
	err := ValidateDSN("app:app_password@tcp(127.0.0.1:3307)/user_order_api_test?parseTime=true")
	if err != nil {
		t.Fatalf("ValidateDSN() error = %v", err)
	}
}
