package testkit

import (
	"fmt"
	"time"
)

type Clock struct{ Value time.Time }

func (c Clock) Now() time.Time { return c.Value }

type IDs struct{ Next int }

func (g *IDs) New(prefix string) (string, error) {
	g.Next++
	return fmt.Sprintf("%s-%03d", prefix, g.Next), nil
}
