package render

import "testing"

func TestEnvFileContent(t *testing.T) {
	got := string(EnvFile(map[string]string{"B": "x y", "A": "p$$\"#"}))
	if want := "A=p$$\"#\nB=x y\n"; got != want {
		t.Errorf("EnvFile = %q, want %q", got, want)
	}
	if b := EnvFile(nil); b == nil || len(b) != 0 {
		t.Errorf("EnvFile(nil) = %#v, want empty non-nil slice", b)
	}
	if p := EnvFilePath("/s/secrets", "web"); p != "/s/secrets/web.env" {
		t.Errorf("EnvFilePath = %q", p)
	}
}
