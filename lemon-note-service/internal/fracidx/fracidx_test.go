package fracidx

import "testing"

// 向量取自 rocicorp/fractional-indexing 的官方测试。
func TestBetweenVectors(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{"", "", "a0"},
		{"", "a0", "Zz"},
		{"", "Zz", "Zy"},
		{"a0", "", "a1"},
		{"a1", "", "a2"},
		{"a0", "a1", "a0V"},
		{"a1", "a2", "a1V"},
		{"a0V", "a1", "a0l"},
		{"Zz", "a0", "ZzV"},
		{"Zz", "a1", "a0"},
		{"", "Y00", "Xzzz"},
		{"bzz", "", "c000"},
		{"a0", "a0V", "a0G"},
		{"a0", "a0G", "a08"},
		{"b125", "b129", "b127"},
		{"a0", "a1V", "a1"},
		{"Zz", "a01", "a0"},
		{"", "a0V", "a0"},
		{"", "b999", "b99"},
		{"zzzzzzzzzzzzzzzzzzzzzzzzzzy", "", "zzzzzzzzzzzzzzzzzzzzzzzzzzz"},
		{"zzzzzzzzzzzzzzzzzzzzzzzzzzz", "", "zzzzzzzzzzzzzzzzzzzzzzzzzzzV"},
	}
	for _, c := range cases {
		got, err := Between(c.a, c.b)
		if err != nil {
			t.Fatalf("Between(%q,%q) error: %v", c.a, c.b, err)
		}
		if got != c.want {
			t.Fatalf("Between(%q,%q)=%q want %q", c.a, c.b, got, c.want)
		}
	}
	for _, bad := range [][2]string{{"a00", ""}, {"a00", "a1"}, {"0", "1"}, {"a1", "a0"}} {
		if _, err := Between(bad[0], bad[1]); err == nil {
			t.Fatalf("Between(%q,%q) 应报错", bad[0], bad[1])
		}
	}
}

func TestNBetween(t *testing.T) {
	ks, err := NBetween("", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a0", "a1", "a2", "a3", "a4"}
	for i := range want {
		if ks[i] != want[i] {
			t.Fatalf("got %v", ks)
		}
	}
	ks, err = NBetween("a0", "a1", 20)
	if err != nil {
		t.Fatal(err)
	}
	prev := "a0"
	for _, k := range ks {
		if !(k > prev && k < "a1") {
			t.Fatalf("顺序错误 %v", ks)
		}
		prev = k
	}
}
