package agent

import "unicode"

// estimateTokens 粗略估算 token：中日韩字符约 1 个 1 token，其余约 4 字节 1 token。
func estimateTokens(s string) int {
	cjk, other := 0, 0
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			cjk++
		} else {
			other += len(string(r))
		}
	}
	return cjk + other/4 + 1
}

func estimateMessages(ms []llmMessage) int {
	n := 0
	for _, m := range ms {
		for _, p := range m.Parts {
			n += estimateTokens(p.Text) + estimateTokens(string(p.Args)) + 4
			if p.Type == "image" {
				n += 1200
			}
		}
	}
	return n
}
