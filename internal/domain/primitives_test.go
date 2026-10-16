package domain

import "testing"

func TestUUIDv7(t *testing.T) {
	id := NewUUIDv7()
	if !IsValidUUIDv7(id) {
		t.Fatalf("expected valid UUIDv7, got %s", id)
	}

	invalid := "00000000-0000-4000-8000-000000000000"
	if IsValidUUIDv7(invalid) {
		t.Fatalf("expected invalid for UUIDv4, got valid")
	}
}

func TestValidateAmountMinor(t *testing.T) {
	if err := ValidateAmountMinor(0, false); err == nil {
		t.Fatalf("expected error for 0 when allowZero is false")
	}
	if err := ValidateAmountMinor(0, true); err != nil {
		t.Fatalf("expected nil for 0 when allowZero is true: %v", err)
	}
	if err := ValidateAmountMinor(-10, true); err == nil {
		t.Fatalf("expected error for negative amount")
	}
	if err := ValidateAmountMinor(MaxSafeAmountMinor+1, true); err == nil {
		t.Fatalf("expected error for overflow amount")
	}
	if err := ValidateAmountMinor(10050, false); err != nil {
		t.Fatalf("expected valid for 10050: %v", err)
	}
}

func TestConvertCurrencySafe(t *testing.T) {
	// 100.00 EUR (10000 minor) * 4.321500 (4321500 micros) = 432.15 PLN (43215 minor)
	res, err := ConvertCurrencySafe(10000, 4321500)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != 43215 {
		t.Fatalf("expected 43215, got %d", res)
	}

	// Test large amount without 64-bit overflow
	// 50 billion PLN * 2.0 = 100 billion
	largeAmount := int64(50_000_000_000_00)
	rate := int64(2_000_000)
	res, err = ConvertCurrencySafe(largeAmount, rate)
	if err != nil {
		t.Fatalf("unexpected error for large amount: %v", err)
	}
	if res != 100_000_000_000_00 {
		t.Fatalf("expected 10000000000000, got %d", res)
	}
}
