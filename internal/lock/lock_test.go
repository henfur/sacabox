package lock

import "testing"

func TestValidName(t *testing.T) {
	for name, ok := range map[string]bool{
		"dev": true, "tmp-0a1b2c3d": true, "ops-2": true,
		"": false, "../../tmp/x": false, "Dev": false, "-x": false, "a/b": false,
	} {
		if ValidName(name) != ok {
			t.Errorf("ValidName(%q) = %v", name, !ok)
		}
	}
}

func TestAcquireRejectsBadName(t *testing.T) {
	t.Setenv("SACABOX_LOCK_DIR", t.TempDir())
	if _, err := Acquire("../escape"); err == nil {
		t.Error("lock path outside the lock dir accepted")
	}
	release, err := Acquire("dev")
	if err != nil {
		t.Fatal(err)
	}
	release()
}
