package dockerx

import (
	"reflect"
	"slices"
	"testing"

	"github.com/moby/moby/client"
)

// The label filters scope every container query to the owner labels; a
// missing filter would let DeleteWorkspace or orphan removal touch every
// container on the host.
func TestContainerQueryFilters(t *testing.T) {
	for name, tc := range map[string]struct {
		got  client.Filters
		want []string
	}{
		"project":   {projectFilter("alice", "shop"), []string{"deploy-gateway.app=shop", "deploy-gateway.workspace=alice"}},
		"workspace": {workspaceFilter("alice"), []string{"deploy-gateway.workspace=alice"}},
		"managed":   {managedFilter(), []string{"deploy-gateway.managed=true"}},
	} {
		if len(tc.got) != 1 {
			t.Errorf("%s: filter terms %v, want only label", name, tc.got)
		}
		var labels []string
		for l := range tc.got["label"] {
			labels = append(labels, l)
		}
		slices.Sort(labels)
		if !reflect.DeepEqual(labels, tc.want) {
			t.Errorf("%s: labels %v, want %v", name, labels, tc.want)
		}
	}
}
