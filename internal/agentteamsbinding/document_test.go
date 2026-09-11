package agentteamsbinding

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeBindingDocumentRejectsDuplicateIdentityDimensions(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	base := documentEntry("worker-a", "AGT-A", "@a:matrix.test", strings.Repeat("a", 64), now)
	for _, test := range []struct {
		name   string
		mutate func(*BindingDocumentEntry)
	}{
		{name: "consumer", mutate: func(entry *BindingDocumentEntry) {
			entry.Principal.LogicalActorID = "AGT-B"
			entry.Principal.RuntimePrincipalID = "@b:matrix.test"
			entry.CredentialSHA256 = strings.Repeat("b", 64)
		}},
		{name: "logical actor", mutate: func(entry *BindingDocumentEntry) {
			entry.ConsumerName = "worker-b"
			entry.Principal.RuntimePrincipalID = "@b:matrix.test"
			entry.CredentialSHA256 = strings.Repeat("b", 64)
		}},
		{name: "digest", mutate: func(entry *BindingDocumentEntry) {
			entry.ConsumerName = "worker-b"
			entry.Principal.LogicalActorID = "AGT-B"
			entry.Principal.RuntimePrincipalID = "@b:matrix.test"
		}},
		{name: "runtime tuple", mutate: func(entry *BindingDocumentEntry) {
			entry.ConsumerName = "worker-b"
			entry.Principal.LogicalActorID = "AGT-B"
			entry.CredentialSHA256 = strings.Repeat("b", 64)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			second := base
			test.mutate(&second)
			if _, err := NormalizeBindingDocument(BindingDocument{SchemaVersion: BindingDocumentSchemaVersion, Bindings: []BindingDocumentEntry{base, second}}, now, RejectExpiredBindings); err == nil {
				t.Fatal("duplicate binding identity was accepted")
			}
		})
	}
}

func TestNormalizeBindingDocumentRejectsDigestAndLifetimeDrift(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		mutate func(*BindingDocumentEntry)
	}{
		{name: "uppercase digest", mutate: func(entry *BindingDocumentEntry) { entry.CredentialSHA256 = strings.Repeat("A", 64) }},
		{name: "missing observed", mutate: func(entry *BindingDocumentEntry) { entry.ObservedAt = "" }},
		{name: "future observed", mutate: func(entry *BindingDocumentEntry) {
			entry.ObservedAt = now.Add(time.Minute).Format(time.RFC3339Nano)
			entry.ExpiresAt = now.Add(2 * time.Minute).Format(time.RFC3339Nano)
		}},
		{name: "expired", mutate: func(entry *BindingDocumentEntry) {
			entry.ObservedAt = now.Add(-3 * time.Minute).Format(time.RFC3339Nano)
			entry.ExpiresAt = now.Add(-time.Minute).Format(time.RFC3339Nano)
		}},
		{name: "excessive ttl", mutate: func(entry *BindingDocumentEntry) {
			entry.ExpiresAt = now.Add(MaxBindingTTL + time.Second).Format(time.RFC3339Nano)
		}},
		{name: "non utc", mutate: func(entry *BindingDocumentEntry) { entry.ObservedAt = "2026-09-05T20:00:00+08:00" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry := documentEntry("worker-a", "AGT-A", "@a:matrix.test", strings.Repeat("a", 64), now)
			test.mutate(&entry)
			if _, err := NormalizeBindingDocument(BindingDocument{SchemaVersion: BindingDocumentSchemaVersion, Bindings: []BindingDocumentEntry{entry}}, now, RejectExpiredBindings); err == nil {
				t.Fatal("invalid digest or lifetime was accepted")
			}
		})
	}
}

func TestNormalizeBindingDocumentDropsExpiredAndSortsCanonicalBindings(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	active := documentEntry("worker-b", "AGT-B", "@b:matrix.test", strings.Repeat("b", 64), now)
	active2 := documentEntry("worker-a", "AGT-A", "@a:matrix.test", strings.Repeat("a", 64), now)
	expired := documentEntry("worker-old", "AGT-OLD", "@old:matrix.test", strings.Repeat("c", 64), now.Add(-4*time.Minute))
	normalized, err := NormalizeBindingDocument(BindingDocument{SchemaVersion: BindingDocumentSchemaVersion, Bindings: []BindingDocumentEntry{active, expired, active2}}, now, DropExpiredBindings)
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized.Bindings) != 2 || normalized.Bindings[0].ConsumerName != "worker-a" || normalized.Bindings[1].ConsumerName != "worker-b" {
		t.Fatalf("normalized bindings = %#v", normalized.Bindings)
	}
}

func documentEntry(consumer, actor, principal, digest string, observed time.Time) BindingDocumentEntry {
	return BindingDocumentEntry{
		ConsumerName: consumer, CredentialSHA256: digest,
		ObservedAt: observed.UTC().Format(time.RFC3339Nano), ExpiresAt: observed.UTC().Add(DefaultBindingTTL).Format(time.RFC3339Nano),
		Principal: BindingDocumentPrincipal{LogicalActorID: actor, RuntimePrincipalID: principal, EnvironmentID: "public", AgentTeamsInstanceID: "haowork-msn-001", BindingRevision: 2},
	}
}
