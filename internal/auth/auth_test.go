package auth

import "testing"

func TestArgon2idHashingAndVerification(t *testing.T) {
	pass := "P@ssword123!"
	encoded, err := HashPassword(pass)
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}

	valid, err := VerifyPassword(pass, encoded)
	if err != nil {
		t.Fatalf("VerifyPassword failed: %v", err)
	}
	if !valid {
		t.Fatalf("expected password verification to succeed")
	}

	invalid, err := VerifyPassword("WrongPassword", encoded)
	if err != nil {
		t.Fatalf("VerifyPassword unexpected error: %v", err)
	}
	if invalid {
		t.Fatalf("expected verification to fail for wrong password")
	}
}

func TestCSPRNGTokenAndPAT(t *testing.T) {
	token, err := GenerateCSPRNGToken()
	if err != nil {
		t.Fatalf("GenerateCSPRNGToken failed: %v", err)
	}
	if len(token) < 40 {
		t.Fatalf("token too short: %s", token)
	}

	pat, hash, err := GeneratePAT()
	if err != nil {
		t.Fatalf("GeneratePAT failed: %v", err)
	}
	if len(pat) < 48 || pat[:8] != "bal_pat_" {
		t.Fatalf("invalid PAT format: %s", pat)
	}
	if hash != HashToken(pat) {
		t.Fatalf("hash mismatch")
	}
}
