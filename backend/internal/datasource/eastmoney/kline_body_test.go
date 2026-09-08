package eastmoney

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/roiding/shadowflow/internal/graymarket"
)

type truncatedKlineBody struct{}

func (truncatedKlineBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestDecodeKlineBodyRequiresCompleteBoundedResponse(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  io.Reader
		valid bool
	}{
		{"valid", strings.NewReader(`{"rc":0}`), true},
		{"truncated after JSON", io.MultiReader(strings.NewReader(`{"rc":0}`), truncatedKlineBody{}), false},
		{"trailing garbage", strings.NewReader(`{"rc":0}broken`), false},
		{"size limit", strings.NewReader(`{"rc":0}` + strings.Repeat(" ", 2<<20)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var payload stockKlineResponse
			err := decodeKlineBody(tc.body, &payload)
			if tc.valid && err != nil || !tc.valid && !errors.Is(err, graymarket.ErrDecode) {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}
