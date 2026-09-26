package keys

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateBucket(t *testing.T) {
	for _, ok := range []string{"scans", "mri-2026", "abc"} {
		if err := ValidateBucket(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "ab", "Scans", "-scan", "scan-", "sc_an", "../x", strings.Repeat("a", 64)} {
		if err := ValidateBucket(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestValidateKey(t *testing.T) {
	for _, ok := range []string{"a", "patients/42/ct.dcm", "../../etc/passwd", "ünïcode"} {
		if err := ValidateKey(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "a\x00b", "line\nbreak", string([]byte{0xff}), strings.Repeat("k", MaxKeyLen+1)} {
		if err := ValidateKey(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestValidateHash(t *testing.T) {
	if err := ValidateHash(HashHex([]byte("x"))); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "../../x", strings.Repeat("A", 64), strings.Repeat("a", 63)} {
		if err := ValidateHash(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestValidateMetadata(t *testing.T) {
	if err := ValidateMetadata(map[string]string{"patient-id": "P-1", "modality": "CT"}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMetadata(map[string]string{"Bad Key": "v"}); err == nil {
		t.Fatal("bad key accepted")
	}
	if err := ValidateMetadata(map[string]string{"k": strings.Repeat("v", MaxMetaValLen+1)}); err == nil {
		t.Fatal("oversized value accepted")
	}
}

func FuzzValidateKey(f *testing.F) {
	f.Add("patients/1/scan.dcm")
	f.Add("\x00")
	f.Fuzz(func(t *testing.T, k string) {
		if ValidateKey(k) == nil && (len(k) == 0 || len(k) > MaxKeyLen || strings.ContainsRune(k, 0)) {
			t.Fatalf("accepted invalid key %q", k)
		}
	})
}

func FuzzValidateHash(f *testing.F) {
	f.Add(HashHex([]byte("scan")))
	f.Add("../etc/passwd")
	f.Fuzz(func(t *testing.T, h string) {
		if ValidateHash(h) == nil && (len(h) != 64 || strings.ContainsAny(h, "/.\\")) {
			t.Fatalf("accepted unsafe hash %q", h)
		}
	})
}
