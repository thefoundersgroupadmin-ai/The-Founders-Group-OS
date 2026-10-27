package toolbridge

import "testing"

func TestEveryMutationNeedsReviewAndRolesCannotEscalate(t *testing.T) {
	seen := map[string]bool{}
	for _, v := range Catalog {
		if seen[v.Name] {
			t.Fatal(v.Name)
		}
		seen[v.Name] = true
		if v.Method == "POST" && !v.Approval {
			t.Fatal("unreviewed mutation", v.Name)
		}
	}
	write, _ := Find("social_publish")
	if Allowed("Data Agent", "dept-tech", write) {
		t.Fatal("cross-role tool escalation")
	}
	if !Allowed("Social Agent", "dept-marketing-growth", write) {
		t.Fatal("assigned tool missing")
	}
	enrich, _ := Find("lusha_enrich")
	if !enrich.Approval || len(enrich.Keys) == 0 {
		t.Fatal("unbounded paid lookup")
	}
}
