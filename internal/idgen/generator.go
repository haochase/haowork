package idgen

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"
)

type Generator struct {
	Now  func() time.Time
	Rand io.Reader
}

func New() Generator {
	return Generator{Now: time.Now, Rand: rand.Reader}
}

func (g Generator) New(prefix string) (string, error) {
	if prefix == "" {
		return "", fmt.Errorf("id prefix is required")
	}
	var entropy [6]byte
	if _, err := io.ReadFull(g.Rand, entropy[:]); err != nil {
		return "", fmt.Errorf("generate id entropy: %w", err)
	}
	stamp := g.Now().UTC().Format("20060102T150405.000000000Z")
	return fmt.Sprintf("%s-%s-%s", strings.ToUpper(prefix), stamp, strings.ToUpper(hex.EncodeToString(entropy[:]))), nil
}
