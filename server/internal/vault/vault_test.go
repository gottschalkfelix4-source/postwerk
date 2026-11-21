package vault

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEncryptDecryptString(t *testing.T) {
	k, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	enc := k.Encrypt("geheim")
	if !strings.HasPrefix(enc, Prefix) || strings.Contains(enc, "geheim") {
		t.Fatalf("not encrypted: %q", enc)
	}
	if k.Encrypt(enc) != enc {
		t.Fatal("double encryption")
	}
	if k.Encrypt("") != "" {
		t.Fatal("empty value must stay empty")
	}
	if enc == k.Encrypt("geheim") {
		t.Fatal("nonce reuse: identical ciphertexts")
	}
	got, err := k.Decrypt(enc)
	if err != nil || got != "geheim" {
		t.Fatalf("decrypt = %q, %v", got, err)
	}
	if got, err := k.Decrypt("plain"); err != nil || got != "plain" {
		t.Fatalf("legacy plaintext = %q, %v", got, err)
	}
	other, _ := Generate()
	if _, err := other.Decrypt(enc); err != ErrWrongKey {
		t.Fatalf("wrong key: err = %v", err)
	}
	if _, err := k.Decrypt(Prefix + "!!"); err != ErrWrongKey {
		t.Fatalf("garbage: err = %v", err)
	}
}

func TestLoadSources(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("POSTWERK_MASTER_KEY", "")
	t.Setenv("POSTWERK_MASTER_KEY_FILE", "")

	k1, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if k1.Source != SourceDataFile || !k1.InDataDir || !k1.Generated {
		t.Fatalf("data file key: %+v", k1)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, KeyFileName))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("key file mode: %v %v", info.Mode(), err)
		}
	}
	k2, err := Load(dir)
	if err != nil || k2.Generated {
		t.Fatalf("second load: %v generated=%v", err, k2.Generated)
	}
	if got, _ := k2.Decrypt(k1.Encrypt("x")); got != "x" {
		t.Fatal("reloaded key differs")
	}

	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i)
	}
	b64 := base64.StdEncoding.EncodeToString(raw)
	t.Setenv("POSTWERK_MASTER_KEY", b64)
	ke, err := Load(dir)
	if err != nil || ke.Source != SourceEnv || ke.InDataDir {
		t.Fatalf("env key: %+v %v", ke, err)
	}
	t.Setenv("POSTWERK_MASTER_KEY", "zu-kurz")
	if _, err := Load(dir); err == nil {
		t.Fatal("invalid env key accepted")
	}

	t.Setenv("POSTWERK_MASTER_KEY", "")
	outside := filepath.Join(t.TempDir(), "mk")
	os.WriteFile(outside, []byte(b64+"\n"), 0o600)
	t.Setenv("POSTWERK_MASTER_KEY_FILE", outside)
	kf, err := Load(dir)
	if err != nil || kf.Source != SourceEnvFile || kf.InDataDir {
		t.Fatalf("env file key: %+v %v", kf, err)
	}
	if got, _ := kf.Decrypt(ke.Encrypt("same")); got != "same" {
		t.Fatal("env and file key with same bytes differ")
	}
	inside := filepath.Join(dir, "keys", "mk")
	os.MkdirAll(filepath.Dir(inside), 0o700)
	os.WriteFile(inside, []byte(b64), 0o600)
	t.Setenv("POSTWERK_MASTER_KEY_FILE", inside)
	if kf, err := Load(dir); err != nil || !kf.InDataDir {
		t.Fatalf("file inside data dir: %+v %v", kf, err)
	}
}

type inner struct {
	Token string `json:"token" secret:"true"`
	Name  string `json:"name"`
}

type sample struct {
	APIKey string `secret:"true"`
	Plain  string
	Nested inner
	Anon   struct {
		Password string `secret:"true"`
	}
	Ptr   *inner
	List  []inner
	Map   map[string]inner
	Names map[string]string
}

func TestFields(t *testing.T) {
	k, _ := Generate()
	s := sample{APIKey: "k1", Plain: "p", Nested: inner{Token: "t1", Name: "n"}, Ptr: &inner{Token: "t2"},
		List: []inner{{Token: "t3"}}, Map: map[string]inner{"a": {Token: "t4"}}, Names: map[string]string{"x": "y"}}
	s.Anon.Password = "pw"
	if n := k.EncryptFields(&s); n != 6 {
		t.Fatalf("encrypted %d fields, want 6", n)
	}
	for _, v := range []string{s.APIKey, s.Nested.Token, s.Anon.Password, s.Ptr.Token, s.List[0].Token, s.Map["a"].Token} {
		if !IsEncrypted(v) {
			t.Fatalf("field not encrypted: %q", v)
		}
	}
	if s.Plain != "p" || s.Nested.Name != "n" || s.Names["x"] != "y" {
		t.Fatal("untagged field changed")
	}
	if n := k.EncryptFields(&s); n != 0 {
		t.Fatal("re-encryption")
	}
	plain, err := k.DecryptFields(&s)
	if err != nil || plain != 0 {
		t.Fatalf("decrypt: plain=%d err=%v", plain, err)
	}
	if s.APIKey != "k1" || s.Nested.Token != "t1" || s.Anon.Password != "pw" || s.Ptr.Token != "t2" || s.List[0].Token != "t3" || s.Map["a"].Token != "t4" {
		t.Fatalf("roundtrip failed: %+v", s)
	}
	if plain, _ := k.DecryptFields(&s); plain != 6 {
		t.Fatalf("legacy plaintext count = %d", plain)
	}
	paths := strings.Join(SecretPaths(sample{}), ",")
	if paths != "APIKey,Nested.Token,Anon.Password,Ptr.Token,List.Token,Map.Token" {
		t.Fatalf("paths = %s", paths)
	}
}
