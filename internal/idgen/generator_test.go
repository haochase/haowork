package idgen_test

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/idgen"
)

func TestGeneratorNewUsesUTCClockAndUppercaseEntropy(t *testing.T) {
	generator := idgen.Generator{
		Now:  func() time.Time { return time.Date(2026, 8, 5, 1, 2, 3, 0, time.UTC) },
		Rand: bytes.NewReader([]byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05}),
	}

	got, err := generator.New("prj")
	if err != nil {
		t.Fatal(err)
	}
	const want = "PRJ-20260805T010203.000000000Z-000102030405"
	if got != want {
		t.Fatalf("New() = %q, want %q", got, want)
	}
}

func TestGeneratorNewRejectsEmptyPrefix(t *testing.T) {
	generator := idgen.New()

	if _, err := generator.New(""); err == nil {
		t.Fatal("New() succeeded, want prefix error")
	}
}

func TestGeneratorNewReturnsEntropyReaderError(t *testing.T) {
	generator := idgen.Generator{
		Now:  time.Now,
		Rand: failingReader{},
	}

	if _, err := generator.New("PRJ"); err == nil {
		t.Fatal("New() succeeded, want entropy error")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("entropy unavailable")
}
