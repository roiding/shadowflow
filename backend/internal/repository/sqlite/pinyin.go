package sqlite

import (
	"database/sql/driver"
	"strings"
	"unicode"

	"github.com/mozillazg/go-pinyin"
	"modernc.org/sqlite"
)

// Scalar functions must be registered before the first connection opens; the
// package-level registration applies to every "sqlite" driver connection.
func init() {
	if err := sqlite.RegisterDeterministicScalarFunction("pinyin_full", 1, pinyinScalarFunc(pinyinFull)); err != nil {
		panic(err)
	}
	if err := sqlite.RegisterDeterministicScalarFunction("pinyin_initials", 1, pinyinScalarFunc(pinyinInitials)); err != nil {
		panic(err)
	}
}

var pinyinArgs = pinyin.NewArgs()

// go-pinyin picks one default reading per character, which is often wrong for
// finance and geography words; these overrides are matched greedily first.
var pinyinPhraseOverrides = map[string][2]string{
	"银行": {"yinhang", "yh"},
	"行业": {"hangye", "hy"},
	"重庆": {"chongqing", "cq"},
	"重组": {"chongzu", "cz"},
	"成都": {"chengdu", "cd"},
	"厦门": {"xiamen", "xm"},
	"长沙": {"changsha", "cs"},
	"长春": {"changchun", "cc"},
	"长江": {"changjiang", "cj"},
	"音乐": {"yinyue", "yy"},
}

func pinyinScalarFunc(convert func(string) string) func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
	return func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		if len(args) == 0 || args[0] == nil {
			return nil, nil
		}
		text, ok := args[0].(string)
		if !ok {
			return nil, nil
		}
		return convert(text), nil
	}
}

func pinyinFull(text string) string {
	full, _ := pinyinParts(text)
	return full
}

func pinyinInitials(text string) string {
	_, initials := pinyinParts(text)
	return initials
}

// Hanzi become tone-less pinyin; Latin letters and digits pass through so
// names like "万科A" are searchable as "wankea" or "wka".
func pinyinParts(text string) (string, string) {
	runes := []rune(text)
	var full, initials strings.Builder
	for index := 0; index < len(runes); index++ {
		if converted, length, ok := overridePinyin(runes[index:]); ok {
			full.WriteString(converted[0])
			initials.WriteString(converted[1])
			index += length - 1
			continue
		}
		char := runes[index]
		switch {
		case char >= 'a' && char <= 'z' || char >= '0' && char <= '9':
			full.WriteRune(char)
			initials.WriteRune(char)
		case char >= 'A' && char <= 'Z':
			lower := unicode.ToLower(char)
			full.WriteRune(lower)
			initials.WriteRune(lower)
		case unicode.Is(unicode.Han, char):
			syllables := pinyin.SinglePinyin(char, pinyinArgs)
			if len(syllables) == 0 || syllables[0] == "" {
				continue
			}
			full.WriteString(syllables[0])
			initials.WriteByte(syllables[0][0])
		}
	}
	return full.String(), initials.String()
}

func overridePinyin(rest []rune) ([2]string, int, bool) {
	maxLength := 4
	if len(rest) < maxLength {
		maxLength = len(rest)
	}
	for length := maxLength; length >= 2; length-- {
		if converted, ok := pinyinPhraseOverrides[string(rest[:length])]; ok {
			return converted, length, true
		}
	}
	return [2]string{}, 0, false
}
