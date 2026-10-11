package pubsub

import "github.com/AreteAcademy/brevis/sdk/internal/core"

var _ core.Configured = Topic{}

// Configuration satisfies sdk.Configured: the topic, and whether attributes and
// an ordering key are set. Both are functions, so what they compute cannot be
// shown -- that they are set can.
func (t Topic) Configuration() []core.Setting {
	return core.Keep(
		core.Setting{Key: "project", Value: t.Project},
		core.Setting{Key: "topic", Value: t.Name},
		core.Setting{Key: "attributes", Value: core.SetByFunction(t.Attributes != nil)},
		core.Setting{Key: "ordering_key", Value: core.SetByFunction(t.OrderingKey != nil)},
	)
}
