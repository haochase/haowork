package agentteamsbinding

import (
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"time"
)

const (
	BindingDocumentSchemaVersion = 1
	DefaultBindingTTL            = 2 * time.Minute
	MaxBindingTTL                = 5 * time.Minute
	maxClockSkew                 = 5 * time.Second
)

type ExpiredBindingPolicy int

const (
	RejectExpiredBindings ExpiredBindingPolicy = iota
	DropExpiredBindings
)

type BindingDocument struct {
	SchemaVersion int                    `json:"schema_version"`
	Bindings      []BindingDocumentEntry `json:"bindings"`
}

type BindingDocumentEntry struct {
	ConsumerName     string                   `json:"consumer_name"`
	CredentialSHA256 string                   `json:"credential_sha256"`
	Function         string                   `json:"function,omitempty"`
	ObservedAt       string                   `json:"observed_at"`
	ExpiresAt        string                   `json:"expires_at"`
	Principal        BindingDocumentPrincipal `json:"principal"`
}

type BindingDocumentPrincipal struct {
	LogicalActorID       string `json:"logical_actor_id"`
	RuntimePrincipalID   string `json:"runtime_principal_id"`
	EnvironmentID        string `json:"environment_id"`
	AgentTeamsInstanceID string `json:"agentteams_instance_id"`
	BindingRevision      int    `json:"binding_revision"`
}

func DecodeBindingDocument(input io.Reader) (BindingDocument, error) {
	if input == nil {
		return BindingDocument{}, errors.New("runtime consumer binding document is required")
	}
	var document BindingDocument
	decoder := json.NewDecoder(io.LimitReader(input, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return BindingDocument{}, errors.New("invalid runtime consumer binding document")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return BindingDocument{}, errors.New("runtime consumer binding document must contain one JSON value")
	}
	return document, nil
}

func NormalizeBindingDocument(document BindingDocument, now time.Time, expiryPolicy ExpiredBindingPolicy) (BindingDocument, error) {
	if document.SchemaVersion != BindingDocumentSchemaVersion || now.IsZero() ||
		(expiryPolicy != RejectExpiredBindings && expiryPolicy != DropExpiredBindings) {
		return BindingDocument{}, errors.New("invalid runtime consumer binding document")
	}
	now = now.UTC()
	normalized := BindingDocument{SchemaVersion: BindingDocumentSchemaVersion, Bindings: make([]BindingDocumentEntry, 0, len(document.Bindings))}
	seenConsumers := make(map[string]struct{}, len(document.Bindings))
	seenActors := make(map[string]struct{}, len(document.Bindings))
	seenDigests := make(map[string]struct{}, len(document.Bindings))
	type runtimeTuple struct {
		principal, environment, instance string
		revision                         int
	}
	seenRuntimeTuples := make(map[runtimeTuple]struct{}, len(document.Bindings))
	for _, entry := range document.Bindings {
		observedAt, observedOK := strictUTCTime(entry.ObservedAt)
		expiresAt, expiresOK := strictUTCTime(entry.ExpiresAt)
		if !observedOK || !expiresOK || observedAt.After(now.Add(maxClockSkew)) || !expiresAt.After(observedAt) || expiresAt.Sub(observedAt) > MaxBindingTTL {
			return BindingDocument{}, errors.New("invalid runtime consumer binding lifetime")
		}
		if !expiresAt.After(now) {
			if expiryPolicy == DropExpiredBindings {
				continue
			}
			return BindingDocument{}, errors.New("runtime consumer binding is expired")
		}
		entry.ConsumerName = strings.TrimSpace(entry.ConsumerName)
		entry.CredentialSHA256 = strings.TrimSpace(entry.CredentialSHA256)
		entry.Principal.LogicalActorID = strings.TrimSpace(entry.Principal.LogicalActorID)
		entry.Principal.RuntimePrincipalID = strings.TrimSpace(entry.Principal.RuntimePrincipalID)
		entry.Principal.EnvironmentID = strings.TrimSpace(entry.Principal.EnvironmentID)
		entry.Principal.AgentTeamsInstanceID = strings.TrimSpace(entry.Principal.AgentTeamsInstanceID)
		if !dnsSubdomain(entry.ConsumerName) || !sha256Hex(entry.CredentialSHA256) ||
			!validIdentityText(entry.Principal.LogicalActorID) || !validMatrixPrincipal(entry.Principal.RuntimePrincipalID) ||
			!dnsLabel(entry.Principal.EnvironmentID) || !dnsSubdomain(entry.Principal.AgentTeamsInstanceID) || entry.Principal.BindingRevision < 1 {
			return BindingDocument{}, errors.New("invalid runtime consumer binding identity")
		}
		tuple := runtimeTuple{entry.Principal.RuntimePrincipalID, entry.Principal.EnvironmentID, entry.Principal.AgentTeamsInstanceID, entry.Principal.BindingRevision}
		if duplicate(seenConsumers, entry.ConsumerName) || duplicate(seenActors, entry.Principal.LogicalActorID) ||
			duplicate(seenDigests, entry.CredentialSHA256) {
			return BindingDocument{}, errors.New("runtime consumer binding identities must be unique")
		}
		if _, exists := seenRuntimeTuples[tuple]; exists {
			return BindingDocument{}, errors.New("runtime consumer principal binding tuples must be unique")
		}
		seenRuntimeTuples[tuple] = struct{}{}
		normalized.Bindings = append(normalized.Bindings, entry)
	}
	sort.Slice(normalized.Bindings, func(i, j int) bool {
		if normalized.Bindings[i].ConsumerName == normalized.Bindings[j].ConsumerName {
			return normalized.Bindings[i].Principal.LogicalActorID < normalized.Bindings[j].Principal.LogicalActorID
		}
		return normalized.Bindings[i].ConsumerName < normalized.Bindings[j].ConsumerName
	})
	return normalized, nil
}

func duplicate(seen map[string]struct{}, value string) bool {
	if _, exists := seen[value]; exists {
		return true
	}
	seen[value] = struct{}{}
	return false
}

func strictUTCTime(value string) (time.Time, bool) {
	if value == "" || !strings.HasSuffix(value, "Z") {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return parsed, err == nil && parsed.Location() == time.UTC && parsed.Format(time.RFC3339Nano) == value
}

func validIdentityText(value string) bool {
	if value == "" || len(value) > 253 || strings.ContainsAny(value, " \t\r\n\x00") {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character == 0x7f {
			return false
		}
	}
	return true
}

func validMatrixPrincipal(value string) bool {
	if !validIdentityText(value) || !strings.HasPrefix(value, "@") {
		return false
	}
	separator := strings.IndexByte(value, ':')
	return separator > 1 && separator < len(value)-1
}
