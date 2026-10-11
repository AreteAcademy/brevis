package execution

import (
	"encoding/json"
	"fmt"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
)

// WriterConfig is how a step's pipeline described itself, ready to store:
//
//	@brevis:{"type":"pipeline","v":1,"fields":[{"path":"target.dedup","value":"merge"}, ...]}
//
// The Go SDK prints it once, before its first phase. The fields are the shape
// a gateway's manifest uses, so /data stores, compares and shows both the same
// way. Hash is the ENGINE's (catalog.FieldsHash), never the step's.
type WriterConfig struct {
	Hash string
	Body []byte
}

// describeCeiling is the largest description kept, the SDK's own ceiling: a
// description is configuration, not data.
const describeCeiling = 64 << 10

// describe keeps a pipeline's description, or says why it did not.
//
// REFUSED WHOLE, like a manifest: half a configuration on the console would
// be a writer configured in a way it is not. Refused rather than redacted when
// it carries a credential (catalog.FindSecret), because storing it with the
// password blanked would hide the bug in the writer that sent it.
//
// The last description of an attempt wins. The SDK sends one; a step that
// sends two has changed its mind, and the later one is what ran.
func (c *stageCollector) describe(raw json.RawMessage, size int) {
	c.config = nil
	if size > describeCeiling {
		c.ConfigRefused = fmt.Sprintf("too large: %d bytes, over %d", size, describeCeiling)
		return
	}
	var fields []catalog.ManifestField
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) == 0 {
		c.ConfigRefused = "no fields to read"
		return
	}
	if err := catalog.CheckFields(fields); err != nil {
		c.ConfigRefused = err.Error()
		return
	}
	hash, body := catalog.FieldsHash(fields)
	c.config = &WriterConfig{Hash: hash, Body: body}
	c.ConfigRefused = ""
}

// Config is the attempt's description, or nil when there was none to keep.
func (c *stageCollector) Config() *WriterConfig { return c.config }
