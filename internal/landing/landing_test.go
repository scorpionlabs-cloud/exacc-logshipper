package landing

import (
	"example.com/exacc-log-shipper/internal/config"
	"testing"
)

func TestValidKey(t *testing.T) {
	for _, v := range []string{"../etc/passwd", "/etc/passwd", "a/../b", "a//b", "a\\b"} {
		if ValidKey(v) == nil {
			t.Errorf("accepted unsafe key %q", v)
		}
	}
	if ValidKey("v1/org=a/x.jsonl.gz") != nil {
		t.Fatal("rejected valid key")
	}
}
func TestLocalRoundTrip(t *testing.T) {
	s, err := Open(config.Storage{Backend: "filesystem", RootDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Put("v1/a.txt", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("v1/a.txt")
	if err != nil || string(got) != "hello" {
		t.Fatalf("unexpected %s %v", got, err)
	}
	k, err := s.List("v1/")
	if err != nil || len(k) != 1 {
		t.Fatalf("list %v %v", k, err)
	}
}
