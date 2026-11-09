package shinnyref_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-tickflow-go/refdata/shinnyref"
)

const validContract = `{"class":"FUTURE","exchange_id":"SHFE","product_id":"rb","delivery_year":2027,"delivery_month":1,"volume_multiple":10,"price_tick":1,"expire_datetime":1800000000,"trading_time":{"day":[["09:00:00","15:00:00"]],"night":[]}}`

func TestPublicDecodeFailureClassification(t *testing.T) {
	if _, e := shinnyref.DecodeContract([]byte(validContract)); e != nil {
		t.Fatal(e)
	}
	cases := []struct {
		raw  string
		want error
	}{{`{"class":"OPTION"}`, shinnyref.ErrNotFuture}, {`{"class":"FUTURE"}`, shinnyref.ErrMissing}, {strings.Replace(validContract, "09:00:00", "invalid", 1), shinnyref.ErrBadOffset}, {strings.Replace(validContract, `[["09:00:00","15:00:00"]]`, `"invalid"`, 1), shinnyref.ErrBadSessions}}
	for _, c := range cases {
		_, e := shinnyref.DecodeContract([]byte(c.raw))
		if !errors.Is(fmt.Errorf("consumer: %w", e), c.want) {
			t.Fatalf("公共分类%v: %v", c.want, e)
		}
	}
}
func TestPublicFetchAndIntegrityClassification(t *testing.T) {
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	if _, e := z.Write([]byte("0123456789 complete snapshot")); e != nil {
		t.Fatal(e)
	}
	if e := z.Close(); e != nil {
		t.Fatal(e)
	}
	for _, mode := range []string{"status", "range", "encoding", "early", "corrupt", "complete"} {
		t.Run(mode, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "status" {
					w.WriteHeader(503)
					return
				}
				if mode == "range" {
					w.WriteHeader(206)
					return
				}
				if mode == "encoding" {
					fmt.Fprint(w, "plain")
					return
				}
				w.Header().Set("Content-Encoding", "gzip")
				raw := append([]byte(nil), b.Bytes()...)
				if mode == "corrupt" {
					raw[len(raw)-5] ^= 1
				}
				w.Write(raw)
			}))
			defer s.Close()
			r, e := shinnyref.Fetch(context.Background(), s.Client(), s.URL)
			switch mode {
			case "status", "range", "encoding":
				want := shinnyref.ErrBadStatus
				if mode == "range" {
					want = shinnyref.ErrRangeAsked
				}
				if mode == "encoding" {
					want = shinnyref.ErrNoGzip
				}
				if !errors.Is(e, want) {
					t.Fatalf("%v %v", want, e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if mode == "early" {
				p := make([]byte, 2)
				if _, e = io.ReadFull(r, p); e != nil {
					t.Fatal(e)
				}
				if e = r.Close(); !errors.Is(e, shinnyref.ErrNotReadToEOF) {
					t.Fatal(e)
				}
				return
			}
			_, readErr := io.ReadAll(r)
			closeErr := r.Close()
			if mode == "corrupt" {
				if readErr == nil || !errors.Is(closeErr, shinnyref.ErrStreamBroken) {
					t.Fatalf("read %v close %v", readErr, closeErr)
				}
			} else if readErr != nil || closeErr != nil {
				t.Fatalf("%v %v", readErr, closeErr)
			}
		})
	}
}
