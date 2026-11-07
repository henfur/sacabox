package guest

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestTreeTar(t *testing.T) {
	src, other := t.TempDir(), t.TempDir()
	must(t, os.MkdirAll(filepath.Join(src, ".config/fish"), 0o755))
	must(t, os.WriteFile(filepath.Join(other, "config.fish"), []byte("set x 1\n"), 0o444))
	must(t, os.Symlink(filepath.Join(other, "config.fish"), filepath.Join(src, ".config/fish/config.fish")))

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	must(t, addTreeContents(tw, src, "", uid))
	must(t, tw.Close())

	got := map[string]*tar.Header{}
	tr := tar.NewReader(&buf)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		must(t, err)
		got[h.Name] = h
	}
	f := got[".config/fish/config.fish"]
	if f == nil || f.Typeflag != tar.TypeReg {
		t.Fatalf("symlink not dereferenced: %v", got)
	}
	if f.Mode != 0o644 || f.Uid != uid {
		t.Errorf("mode %o uid %d", f.Mode, f.Uid)
	}
	if got[".config/"] == nil || got[".config/fish/"] == nil {
		t.Errorf("directories missing: %v", got)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSanitized(t *testing.T) {
	var buf bytes.Buffer
	w := &sanitized{w: &buf}
	// OSC 52 clipboard write, a C1 CSI split across writes, normal UTF-8 kept
	_, _ = w.Write([]byte("ok\x1b]52;c;aGk=\x07 \xc2"))
	_, _ = w.Write([]byte("\x9b31m caf\xc3\xa9 \xc2\xa0x\r\n"))
	if got, want := buf.String(), "ok]52;c;aGk= 31m caf\xc3\xa9 \xc2\xa0x\r\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
