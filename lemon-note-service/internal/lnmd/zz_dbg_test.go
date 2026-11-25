package lnmd

import (
	"fmt"
	"testing"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

func TestDbg(t *testing.T) {
	for _, md := range []string{"1. a\n\n   <callout>\n   -\n   </callout>\n2. b", "1. a\n\n   -\n\n   x\n2. b"} {
		nz := normalize(md)
		fmt.Printf("=== normalized:\n%s", nz.src)
		doc := mdParser().Parse(text.NewReader(nz.src))
		doc.Dump(nz.src, 0)
		_ = ast.Walk
		bs, _ := Parse(md)
		fmt.Println(js(bs))
	}
}
