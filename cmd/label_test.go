package cmd

import (
	"reflect"
	"testing"
)

func TestApplyLabelArgs(t *testing.T) {
	got, err := applyLabelArgs(map[string]string{"a": "1", "b": "2"}, []string{"a=x", "b-", "c=", "d=e=f"})
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"a": "x", "c": "", "d": "e=f"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	for _, bad := range []string{"a", "=x", "-"} {
		if _, err := applyLabelArgs(nil, []string{bad}); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}
