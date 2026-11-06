package sqlite

import "testing"

func TestPinyinPartsCoversFinanceWordsAndLatinSuffixes(t *testing.T) {
	cases := []struct{ text, full, initials string }{
		{"平安银行", "pinganyinhang", "payh"},
		{"浦发银行", "pufayinhang", "pfyh"},
		{"万科A", "wankea", "wka"},
		{"重庆啤酒", "chongqingpijiu", "cqpj"},
		{"行业龙头", "hangyelongtou", "hylt"},
		{"AI算力", "aisuanli", "aisl"},
	}
	for _, item := range cases {
		full, initials := pinyinParts(item.text)
		if full != item.full || initials != item.initials {
			t.Fatalf("%s: got full=%q initials=%q, want %q/%q", item.text, full, initials, item.full, item.initials)
		}
	}
}
