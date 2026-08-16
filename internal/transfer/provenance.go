package transfer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/haochase/haowork/internal/trace"
)

// TraceStoreProvenanceVerifier accepts a trace entry only when its exact bytes
// are the serialisation of the identified record in the local trace ledger.
type TraceStoreProvenanceVerifier struct {
	Store trace.Store
}

func (verifier TraceStoreProvenanceVerifier) VerifyProvenance(ctx context.Context, entry Entry) error {
	if verifier.Store == nil || entry.Type != EntryTrace || entry.Provenance.Source != "trace-ledger" || !strings.HasPrefix(entry.Path, "trace/") || !strings.HasSuffix(entry.Path, ".json") {
		return errors.New("trace provenance is not trusted")
	}
	id := strings.TrimSuffix(strings.TrimPrefix(entry.Path, "trace/"), ".json")
	if id == "" || strings.Contains(id, "/") {
		return errors.New("trace provenance path is invalid")
	}
	records, err := verifier.Store.ReadAll(ctx)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.ID != id {
			continue
		}
		canonical, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if bytes.Equal(entry.Data, canonical) {
			return nil
		}
		return errors.New("trace entry bytes do not match the trace ledger")
	}
	return errors.New("trace entry is absent from the trace ledger")
}
