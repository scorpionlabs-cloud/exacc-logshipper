package model

import "testing"

func TestManifestSignature(t *testing.T) {
	m := Manifest{BatchID: "test-batch", PayloadKey: "v1/a.jsonl.gz", PayloadSHA256: "abcdef", EventCount: 2}
	if err := m.Sign("0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	if err := m.Verify("0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	m.PayloadKey = "v1/altered.jsonl.gz"
	if err := m.Verify("0123456789abcdef0123456789abcdef"); err == nil {
		t.Fatal("tampered manifest passed verification")
	}
}
func TestClassify(t *testing.T) {
	s, c := Classify("ORA-00257 archiver error")
	if s != "ERROR" || c != "ORA-00257" {
		t.Fatalf("unexpected %s %s", s, c)
	}
}
