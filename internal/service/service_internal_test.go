package service

import (
	"os"
	"reflect"
	"testing"
)

// The gateway never follows symlinks when changing ownership.
func TestDefaultChownDoesNotFollowSymlinks(t *testing.T) {
	s := New(nil, nil, nil, t.TempDir())
	if reflect.ValueOf(s.Chown).Pointer() != reflect.ValueOf(os.Lchown).Pointer() {
		t.Fatal("default Chown must be os.Lchown")
	}
}
